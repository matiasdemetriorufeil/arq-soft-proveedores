package fav

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Valores de ADR-005 y ADR-009 para las llamadas a FAV.
const (
	TimeoutPorIntento = 3 * time.Second
	ReintentosMaximos = 3
	// esperaMaxima acota el Retry-After que mande FAV: la sincronización
	// siguiente es en 5 minutos, no tiene sentido esperar más que eso acá.
	esperaMaxima = 30 * time.Second
	// tamanoMaximo protege de una respuesta desproporcionada. El catálogo
	// completo es del orden de cientos de productos.
	tamanoMaximo = 20 << 20
)

var (
	// ErrNoAutenticado: FAV rechazó la clave (401). No se reintenta y no es
	// una caída: hay que revisar la configuración.
	ErrNoAutenticado = errors.New("FAV rechazó la clave del grupo (401 NO_AUTENTICADO)")
	// ErrNoDisponible: FAV no respondió o respondió un error reintentable
	// (timeout, 429, 500, 503) después de agotar los reintentos.
	ErrNoDisponible = errors.New("FAV no está disponible")
	// ErrRespuestaInvalida: la respuesta no cumple el contrato.
	ErrRespuestaInvalida = errors.New("la respuesta de FAV no cumple el contrato")
)

// Cliente llama a GET /v1/productos de FAV.
type Cliente struct {
	base   string
	clave  string
	http   *http.Client
	dormir func(context.Context, time.Duration) error
}

// NuevoCliente arma el cliente. transport puede ser nil; en el servicio es el
// de otelhttp, para que la llamada salga con traceparent y quede en la traza.
func NuevoCliente(base, clave string, transport http.RoundTripper) *Cliente {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Cliente{
		base:   strings.TrimRight(base, "/"),
		clave:  clave,
		http:   &http.Client{Transport: transport},
		dormir: dormirContexto,
	}
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

// Resultado trae el catálogo decodificado y el cuerpo crudo, que import-service
// guarda tal cual en MongoDB.
type Resultado struct {
	Catalogo      Catalogo
	Crudo         []byte
	CorrelationID string
	Intentos      int
}

// ListarProductos baja el catálogo completo. Reintenta hasta ReintentosMaximos
// veces ante timeout, error de red, 429, 500 o 503, con espera creciente (o la
// que pida Retry-After). Es un GET sin efectos: reintentarlo es seguro.
func (c *Cliente) ListarProductos(ctx context.Context, correlationID string) (Resultado, error) {
	var res Resultado
	err := c.conReintentos(ctx, func(intento int) (time.Duration, error) {
		r, espera, err := c.unIntento(ctx, correlationID)
		if err != nil {
			return espera, err
		}
		r.Intentos = intento
		res = r
		return 0, nil
	})
	if err != nil {
		return Resultado{}, err
	}
	return res, nil
}

type errReintentable struct{ causa error }

func (e errReintentable) Error() string { return e.causa.Error() }
func (e errReintentable) Unwrap() error { return e.causa }

func esReintentable(err error) bool {
	var r errReintentable
	return errors.As(err, &r)
}

// unIntentoRuta hace un GET y traduce lo que todas las operaciones interpretan
// igual: el 401, los status reintentables y la lectura del cuerpo. El 200 y los
// demás status vuelven tal cual, porque cada operación los lee distinto (el
// catálogo valida su esquema; la disponibilidad trata el 404 como "apagada").
func (c *Cliente) unIntentoRuta(ctx context.Context, ruta, correlationID string) (int, []byte, string, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutPorIntento)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+ruta, nil)
	if err != nil {
		return 0, nil, "", 0, err
	}
	req.Header.Set("X-API-Key", c.clave)
	req.Header.Set("Accept", "application/json")
	if correlationID != "" {
		req.Header.Set("X-Correlation-Id", correlationID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return 0, nil, "", 0, err
		}
		return 0, nil, "", 0, errReintentable{fmt.Errorf("llamar a FAV: %w", err)}
	}
	defer resp.Body.Close()
	cuerpo, err := io.ReadAll(io.LimitReader(resp.Body, tamanoMaximo))
	if err != nil {
		return 0, nil, "", 0, errReintentable{fmt.Errorf("leer respuesta de FAV: %w", err)}
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return resp.StatusCode, cuerpo, "", 0, ErrNoAutenticado
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return resp.StatusCode, cuerpo, "", retryAfter(resp.Header.Get("Retry-After")),
			errReintentable{fmt.Errorf("FAV respondió %d %s", resp.StatusCode, codigoDe(cuerpo))}
	}
	return resp.StatusCode, cuerpo, resp.Header.Get("X-Correlation-Id"), 0, nil
}

// conReintentos corre intento hasta ReintentosMaximos veces más ante errores
// reintentables, con espera creciente o la que pida Retry-After.
func (c *Cliente) conReintentos(ctx context.Context, intento func(int) (time.Duration, error)) error {
	var ultimo error
	for n := 1; n <= 1+ReintentosMaximos; n++ {
		espera, err := intento(n)
		if err == nil {
			return nil
		}
		if !esReintentable(err) {
			return err
		}
		ultimo = err
		if n > ReintentosMaximos {
			break
		}
		if espera == 0 {
			espera = time.Duration(1<<(n-1)) * 500 * time.Millisecond // 0,5 s, 1 s, 2 s
		}
		if err := c.dormir(ctx, espera); err != nil {
			return fmt.Errorf("%w: %v", ErrNoDisponible, err)
		}
	}
	return fmt.Errorf("%w: %v", ErrNoDisponible, ultimo)
}

func (c *Cliente) unIntento(ctx context.Context, correlationID string) (Resultado, time.Duration, error) {
	status, cuerpo, corr, espera, err := c.unIntentoRuta(ctx, "/v1/productos", correlationID)
	if err != nil {
		return Resultado{}, espera, err
	}
	if status != http.StatusOK {
		return Resultado{}, 0, fmt.Errorf("%w: status %d %s", ErrRespuestaInvalida, status, codigoDe(cuerpo))
	}
	var cat Catalogo
	if err := json.Unmarshal(cuerpo, &cat); err != nil {
		return Resultado{}, 0, fmt.Errorf("%w: %v", ErrRespuestaInvalida, err)
	}
	if err := validar(cat); err != nil {
		return Resultado{}, 0, err
	}
	return Resultado{Catalogo: cat, Crudo: cuerpo, CorrelationID: corr}, 0, nil
}

// MaxVariantesPorConsulta es el tope de ids por llamada que fija el contrato de
// FAV para /v1/disponibilidad (de 1 a 200). Pasarse devuelve 400.
const MaxVariantesPorConsulta = 200

// ErrDisponibilidadApagada: FAV respondió 404 a /v1/disponibilidad. Su contrato
// dice que puede apagar esa operación temporalmente, así que **no es una
// caída**: quien llama sigue con el tope fijo por variante.
var ErrDisponibilidadApagada = errors.New("FAV no expone GET /v1/disponibilidad (404)")

// ConsultarDisponibilidad pide el stock real de cada variante y devuelve un
// mapa varianteID → unidades. Parte el pedido en tandas de
// MaxVariantesPorConsulta, porque el contrato no acepta más.
//
// Una variante que FAV no devuelve queda en 0, como dice su contrato: una
// variante inexistente o sin stock se informa con 0 y no con un error.
func (c *Cliente) ConsultarDisponibilidad(ctx context.Context, variantes []string, correlationID string) (map[string]int, error) {
	stock := make(map[string]int, len(variantes))
	for _, v := range variantes {
		stock[v] = 0
	}
	for i := 0; i < len(variantes); i += MaxVariantesPorConsulta {
		tanda := variantes[i:min(i+MaxVariantesPorConsulta, len(variantes))]
		ruta := "/v1/disponibilidad?variantes=" + url.QueryEscape(strings.Join(tanda, ","))
		err := c.conReintentos(ctx, func(int) (time.Duration, error) {
			status, cuerpo, _, espera, err := c.unIntentoRuta(ctx, ruta, correlationID)
			if err != nil {
				return espera, err
			}
			switch status {
			case http.StatusOK:
				var d Disponibilidad
				if err := json.Unmarshal(cuerpo, &d); err != nil {
					return 0, fmt.Errorf("%w: %v", ErrRespuestaInvalida, err)
				}
				for _, it := range d.Items {
					if it.Disponible > 0 {
						stock[it.VarianteID] = it.Disponible
					}
				}
				return 0, nil
			case http.StatusNotFound:
				return 0, ErrDisponibilidadApagada
			default:
				return 0, fmt.Errorf("%w: status %d %s", ErrRespuestaInvalida, status, codigoDe(cuerpo))
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return stock, nil
}

// validar revisa los campos obligatorios del contrato. Un catálogo que no los
// cumple no se usa: preferimos quedarnos con la última importación válida.
func validar(cat Catalogo) error {
	if cat.Items == nil {
		return fmt.Errorf("%w: falta items", ErrRespuestaInvalida)
	}
	for i, p := range cat.Items {
		if p.ProductoID == "" || p.Nombre == "" || p.Categoria == "" {
			return fmt.Errorf("%w: items[%d] sin productoId, nombre o categoria", ErrRespuestaInvalida, i)
		}
		if len(p.Variantes) == 0 {
			return fmt.Errorf("%w: items[%d] sin variantes", ErrRespuestaInvalida, i)
		}
	}
	return nil
}

func codigoDe(cuerpo []byte) string {
	var p Problema
	if json.Unmarshal(cuerpo, &p) == nil && p.Codigo != "" {
		return p.Codigo
	}
	return ""
}

func retryAfter(v string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || s <= 0 {
		return 0
	}
	d := time.Duration(s) * time.Second
	if d > esperaMaxima {
		d = esperaMaxima
	}
	return d
}
