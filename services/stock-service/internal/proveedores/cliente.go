// Package proveedores es el cliente de la capacidad que publica
// proveedores-service: GET /api/v1/proveedores/recomendados del contrato
// [docs/contracts/openapi.yaml]. Es el único paquete de stock-service que
// conoce el transporte de esa llamada; la capa de negocio lo recibe como una
// interfaz, igual que el repositorio.
//
// La llamada sale directo a una réplica por la red interna, no por el
// api-gateway: el gateway es el punto de entrada de los consumidores externos.
package proveedores

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Valores de ADR-005 para la llamada stock-service → proveedores-service. Es un
// GET idempotente y sin efectos, así que reintentarlo es seguro.
const (
	// TimeoutPorIntento es el tiempo máximo de cada intento.
	TimeoutPorIntento = 1 * time.Second
	// ReintentosMaximos son los reintentos después del primer intento: 3
	// intentos en total.
	ReintentosMaximos = 2
	// tamanoMaximo protege de una respuesta desproporcionada: el contrato
	// acota el array en 50 elementos.
	tamanoMaximo = 1 << 20
	// maxIdleConnsPorHost mantiene conexiones abiertas con la réplica: el
	// ciclo hace una llamada por SKU bajo el mínimo, y con el transporte por
	// defecto (2 por host) se abriría una conexión nueva todo el tiempo.
	maxIdleConnsPorHost = 8
	// limiteRecomendaciones es el `limite` que pide el ciclo: solo necesita al
	// más barato, y el contrato ya devuelve el array ordenado por costo total
	// ascendente.
	limiteRecomendaciones = 1
)

// esperas es la espera antes de cada reintento (ADR-005), a la que se le suma
// jitter para que dos réplicas que fallan juntas no reintenten a la vez.
var esperas = []time.Duration{100 * time.Millisecond, 300 * time.Millisecond}

// Errores del cliente. La capa de negocio los distingue para decidir el motivo
// que queda guardado en la orden.
var (
	// ErrSinOfertas: el producto existe pero ningún proveedor tiene stock
	// suficiente para la cantidad pedida (200 con []). No es una falla.
	ErrSinOfertas = errors.New("ningún proveedor con stock suficiente")
	// ErrProductoNoEncontrado: el SKU no existe para proveedores-service
	// (404). No se reintenta: no es transitorio.
	ErrProductoNoEncontrado = errors.New("proveedores-service no conoce el producto")
	// ErrNoAutorizado: la clave fue rechazada (401). No se reintenta: es un
	// problema de configuración, no una caída.
	ErrNoAutorizado = errors.New("proveedores-service rechazó la clave (401)")
	// ErrNoDisponible: no respondió, o respondió un error reintentable, y se
	// agotaron los reintentos.
	ErrNoDisponible = errors.New("proveedores-service no está disponible")
	// ErrRespuestaInvalida: la respuesta no cumple el contrato.
	ErrRespuestaInvalida = errors.New("la respuesta de proveedores-service no cumple el contrato")
)

// Recomendacion es un elemento del array que devuelve el contrato: una oferta
// de un proveedor ya evaluada para la cantidad pedida.
type Recomendacion struct {
	ProveedorID       int64   `json:"proveedor_id"`
	ProveedorNombre   string  `json:"proveedor_nombre"`
	PrecioUnitario    float64 `json:"precio_unitario"`
	DescuentoPct      float64 `json:"descuento_pct"`
	DescuentoAplicado bool    `json:"descuento_aplicado"`
	Flete             float64 `json:"flete"`
	StockDisponible   int     `json:"stock_disponible"`
	CostoTotal        float64 `json:"costo_total"`
}

// Cliente llama a GET /api/v1/proveedores/recomendados.
type Cliente struct {
	base   string
	clave  string
	http   *http.Client
	dormir func(context.Context, time.Duration) error
}

// NuevoCliente arma el cliente. `transport` puede ser nil; en el servicio es el
// de otelhttp, para que la llamada salga con traceparent y quede en la misma
// traza que el ciclo.
func NuevoCliente(base, clave string, transport http.RoundTripper) *Cliente {
	if transport == nil {
		transport = TransportePropio()
	}
	return &Cliente{
		base:   strings.TrimRight(base, "/"),
		clave:  clave,
		http:   &http.Client{Transport: transport},
		dormir: dormirContexto,
	}
}

// TransportePropio no es http.DefaultTransport: con MaxIdleConnsPerHost en 2,
// el ciclo abriría y cerraría conexiones a la misma réplica todo el tiempo.
// Está exportado para que el servicio lo pueda envolver con el transporte de
// otelhttp sin perder esta configuración.
func TransportePropio() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = maxIdleConnsPorHost
	t.MaxIdleConnsPerHost = maxIdleConnsPorHost
	t.IdleConnTimeout = 90 * time.Second
	return t
}

func dormirContexto(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// MasBarato devuelve el proveedor de menor costo total para esa cantidad
// (RN-16). El contrato garantiza el array ordenado por `costo_total`
// ascendente, así que es el primer elemento.
func (c *Cliente) MasBarato(ctx context.Context, sku string, cantidad int) (Recomendacion, error) {
	recs, err := c.Recomendados(ctx, sku, cantidad)
	if err != nil {
		return Recomendacion{}, err
	}
	return recs[0], nil
}

// Recomendados pide las ofertas del SKU para esa cantidad. Reintenta hasta
// ReintentosMaximos veces ante error de transporte, timeout, 429, 500, 502 o
// 503, con espera creciente más jitter. Un 404 y un 200 con [] no se
// reintentan: no son transitorios.
func (c *Cliente) Recomendados(ctx context.Context, sku string, cantidad int) ([]Recomendacion, error) {
	if strings.TrimSpace(sku) == "" {
		return nil, fmt.Errorf("%w: el SKU es obligatorio", ErrRespuestaInvalida)
	}
	if cantidad <= 0 {
		return nil, fmt.Errorf("%w: la cantidad tiene que ser mayor a 0", ErrRespuestaInvalida)
	}

	var ultimo error
	for intento := 0; intento <= ReintentosMaximos; intento++ {
		recs, err := c.unIntento(ctx, sku, cantidad)
		if err == nil {
			return recs, nil
		}
		if !esReintentable(err) {
			return nil, err
		}
		ultimo = err
		if intento == ReintentosMaximos {
			break
		}
		if err := c.dormir(ctx, conJitter(esperas[intento])); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoDisponible, err)
		}
	}
	return nil, fmt.Errorf("%w después de %d intentos: %v", ErrNoDisponible, ReintentosMaximos+1, ultimo)
}

// conJitter suma hasta un 20% de la espera, así dos réplicas que fallan al
// mismo tiempo no reintentan sincronizadas.
func conJitter(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

type errReintentable struct{ causa error }

func (e errReintentable) Error() string { return e.causa.Error() }
func (e errReintentable) Unwrap() error { return e.causa }

func esReintentable(err error) bool {
	var r errReintentable
	return errors.As(err, &r)
}

func (c *Cliente) unIntento(ctx context.Context, sku string, cantidad int) ([]Recomendacion, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutPorIntento)
	defer cancel()

	destino := c.base + "/api/v1/proveedores/recomendados?" + url.Values{
		"producto": {sku},
		"cantidad": {strconv.Itoa(cantidad)},
		"limite":   {strconv.Itoa(limiteRecomendaciones)},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, destino, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.clave)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Una cancelación del contexto de arriba (el apagado del servicio) no
		// es una falla del proveedor: no tiene sentido reintentarla.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, err
		}
		return nil, errReintentable{fmt.Errorf("llamar a proveedores-service: %w", err)}
	}
	defer resp.Body.Close()
	cuerpo, err := io.ReadAll(io.LimitReader(resp.Body, tamanoMaximo))
	if err != nil {
		return nil, errReintentable{fmt.Errorf("leer la respuesta de proveedores-service: %w", err)}
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var recs []Recomendacion
		if err := json.Unmarshal(cuerpo, &recs); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrRespuestaInvalida, err)
		}
		if len(recs) == 0 {
			return nil, ErrSinOfertas
		}
		return recs, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: '%s'", ErrProductoNoEncontrado, sku)
	case http.StatusUnauthorized:
		return nil, ErrNoAutorizado
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable:
		return nil, errReintentable{fmt.Errorf("proveedores-service respondió %d %s",
			resp.StatusCode, codigoDe(cuerpo))}
	default:
		return nil, fmt.Errorf("%w: status %d %s", ErrRespuestaInvalida, resp.StatusCode, codigoDe(cuerpo))
	}
}

// codigoDe saca el `code` del problem+json para que el motivo guardado en la
// orden diga algo útil.
func codigoDe(cuerpo []byte) string {
	var p struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(cuerpo, &p) == nil {
		return p.Code
	}
	return ""
}
