package fav

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	var ultimo error
	for intento := 1; intento <= 1+ReintentosMaximos; intento++ {
		res, espera, err := c.unIntento(ctx, correlationID)
		if err == nil {
			res.Intentos = intento
			return res, nil
		}
		if !esReintentable(err) {
			return Resultado{}, err
		}
		ultimo = err
		if intento > ReintentosMaximos {
			break
		}
		if espera == 0 {
			espera = time.Duration(1<<(intento-1)) * 500 * time.Millisecond // 0,5 s, 1 s, 2 s
		}
		if err := c.dormir(ctx, espera); err != nil {
			return Resultado{}, fmt.Errorf("%w: %v", ErrNoDisponible, err)
		}
	}
	return Resultado{}, fmt.Errorf("%w: %v", ErrNoDisponible, ultimo)
}

type errReintentable struct{ causa error }

func (e errReintentable) Error() string { return e.causa.Error() }
func (e errReintentable) Unwrap() error { return e.causa }

func esReintentable(err error) bool {
	var r errReintentable
	return errors.As(err, &r)
}

func (c *Cliente) unIntento(ctx context.Context, correlationID string) (Resultado, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutPorIntento)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/productos", nil)
	if err != nil {
		return Resultado{}, 0, err
	}
	req.Header.Set("X-API-Key", c.clave)
	req.Header.Set("Accept", "application/json")
	if correlationID != "" {
		req.Header.Set("X-Correlation-Id", correlationID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
			return Resultado{}, 0, err
		}
		return Resultado{}, 0, errReintentable{fmt.Errorf("llamar a FAV: %w", err)}
	}
	defer resp.Body.Close()
	cuerpo, err := io.ReadAll(io.LimitReader(resp.Body, tamanoMaximo))
	if err != nil {
		return Resultado{}, 0, errReintentable{fmt.Errorf("leer respuesta de FAV: %w", err)}
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var cat Catalogo
		if err := json.Unmarshal(cuerpo, &cat); err != nil {
			return Resultado{}, 0, fmt.Errorf("%w: %v", ErrRespuestaInvalida, err)
		}
		if err := validar(cat); err != nil {
			return Resultado{}, 0, err
		}
		return Resultado{Catalogo: cat, Crudo: cuerpo, CorrelationID: resp.Header.Get("X-Correlation-Id")}, 0, nil
	case http.StatusUnauthorized:
		return Resultado{}, 0, ErrNoAutenticado
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return Resultado{}, retryAfter(resp.Header.Get("Retry-After")),
			errReintentable{fmt.Errorf("FAV respondió %d %s", resp.StatusCode, codigoDe(cuerpo))}
	default:
		return Resultado{}, 0, fmt.Errorf("%w: status %d %s", ErrRespuestaInvalida, resp.StatusCode, codigoDe(cuerpo))
	}
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
