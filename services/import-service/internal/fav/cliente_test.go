package fav

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const catalogoMinimo = `{"items":[{"productoId":"p1","nombre":"Remera","categoria":"remeras",
 "variantes":[{"varianteId":"v1","talle":"M","color":"Blanco","precio":15000}]}]}`

func clienteDe(t *testing.T, h http.HandlerFunc) (*Cliente, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NuevoCliente(srv.URL, "clave-fav", nil)
	esperas := &[]time.Duration{}
	c.dormir = func(_ context.Context, d time.Duration) error {
		*esperas = append(*esperas, d)
		return nil
	}
	return c, esperas
}

func TestListarProductosMandaLaClaveYDevuelveElCatalogo(t *testing.T) {
	c, _ := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/productos" {
			t.Errorf("pidió %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "clave-fav" {
			t.Errorf("X-API-Key = %q", r.Header.Get("X-API-Key"))
		}
		if r.Header.Get("X-Correlation-Id") != "corr-1" {
			t.Errorf("X-Correlation-Id = %q", r.Header.Get("X-Correlation-Id"))
		}
		w.Header().Set("X-Correlation-Id", "corr-1")
		w.Write([]byte(catalogoMinimo))
	})
	res, err := c.ListarProductos(context.Background(), "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Catalogo.Items) != 1 || res.Catalogo.Items[0].Variantes[0].Precio != 15000 {
		t.Errorf("catálogo = %+v", res.Catalogo)
	}
	if res.Intentos != 1 || len(res.Crudo) == 0 {
		t.Errorf("intentos = %d, crudo = %d bytes", res.Intentos, len(res.Crudo))
	}
}

func TestListarProductosReintentaLosErroresTransitorios(t *testing.T) {
	for _, status := range []int{429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var n atomic.Int32
			c, esperas := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
				if n.Add(1) < 3 {
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(status)
					fmt.Fprintf(w, `{"type":"x","title":"x","status":%d,"codigo":"NO_DISPONIBLE"}`, status)
					return
				}
				w.Write([]byte(catalogoMinimo))
			})
			res, err := c.ListarProductos(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			if res.Intentos != 3 {
				t.Errorf("intentos = %d, quiero 3", res.Intentos)
			}
			if len(*esperas) != 2 || (*esperas)[1] <= (*esperas)[0] {
				t.Errorf("esperas = %v, quiero 2 crecientes", *esperas)
			}
		})
	}
}

func TestListarProductosRespetaRetryAfterConTope(t *testing.T) {
	var n atomic.Int32
	c, esperas := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(catalogoMinimo))
	})
	if _, err := c.ListarProductos(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if (*esperas)[0] != esperaMaxima {
		t.Errorf("espera = %v, quiero el tope %v", (*esperas)[0], esperaMaxima)
	}
}

func TestListarProductosAgotaReintentosYDevuelveNoDisponible(t *testing.T) {
	var n atomic.Int32
	c, _ := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	_, err := c.ListarProductos(context.Background(), "")
	if !errors.Is(err, ErrNoDisponible) {
		t.Fatalf("err = %v, quiero ErrNoDisponible", err)
	}
	if n.Load() != 1+ReintentosMaximos {
		t.Errorf("llamó %d veces, quiero %d", n.Load(), 1+ReintentosMaximos)
	}
}

func TestListarProductosNoReintentaUnaClaveInvalida(t *testing.T) {
	var n atomic.Int32
	c, _ := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.ListarProductos(context.Background(), "")
	if !errors.Is(err, ErrNoAutenticado) {
		t.Fatalf("err = %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("llamó %d veces: un 401 no se reintenta", n.Load())
	}
}

func TestListarProductosCortaPorTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("espera el timeout real de 3 s")
	}
	var n atomic.Int32
	c, _ := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(TimeoutPorIntento + 2*time.Second):
			}
			return
		}
		w.Write([]byte(catalogoMinimo))
	})
	inicio := time.Now()
	res, err := c.ListarProductos(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Intentos != 2 {
		t.Errorf("intentos = %d, quiero 2", res.Intentos)
	}
	if d := time.Since(inicio); d > TimeoutPorIntento+time.Second {
		t.Errorf("tardó %v: el timeout por intento no cortó", d)
	}
}

func TestListarProductosRechazaUnCatalogoFueraDeContrato(t *testing.T) {
	casos := map[string]string{
		"no es json":        `{`,
		"sin items":         `{}`,
		"producto sin id":   `{"items":[{"nombre":"x","categoria":"c","variantes":[{"varianteId":"v","talle":"M","color":"N","precio":1}]}]}`,
		"sin variantes":     `{"items":[{"productoId":"p","nombre":"x","categoria":"c","variantes":[]}]}`,
		"status inesperado": "",
	}
	for nombre, cuerpo := range casos {
		t.Run(nombre, func(t *testing.T) {
			c, _ := clienteDe(t, func(w http.ResponseWriter, r *http.Request) {
				if cuerpo == "" {
					w.WriteHeader(http.StatusTeapot)
					return
				}
				w.Write([]byte(cuerpo))
			})
			_, err := c.ListarProductos(context.Background(), "")
			if !errors.Is(err, ErrRespuestaInvalida) {
				t.Errorf("err = %v, quiero ErrRespuestaInvalida", err)
			}
		})
	}
}
