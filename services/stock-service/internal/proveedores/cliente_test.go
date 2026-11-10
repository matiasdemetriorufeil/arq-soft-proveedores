package proveedores_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
)

// dosOfertas es el ejemplo del contrato (docs/contracts/openapi.yaml), ya
// ordenado por costo_total ascendente: eso es lo que el contrato garantiza y
// por eso el más barato es el primero (RN-16).
const dosOfertas = `[
  {"proveedor_id":3,"proveedor_nombre":"Cordoba Energia SA","precio_unitario":189573.99,
   "descuento_pct":15,"descuento_aplicado":true,"flete":43000,"stock_disponible":394,
   "costo_total":1654378.92},
  {"proveedor_id":6,"proveedor_nombre":"Fotovoltaica Pampeana SRL","precio_unitario":199645.5,
   "descuento_pct":3,"descuento_aplicado":true,"flete":64500,"stock_disponible":62,
   "costo_total":2001061.35}
]`

// TestMasBaratoTomaElPrimero: camino feliz. Verifica además la request que
// sale: ruta, método, X-API-Key y los parámetros del contrato.
func TestMasBaratoTomaElPrimero(t *testing.T) {
	var recibida *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recibida = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(dosOfertas))
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave-de-prueba", nil)
	rec, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10)
	if err != nil {
		t.Fatalf("MasBarato: %v", err)
	}
	if rec.ProveedorID != 3 || rec.CostoTotal != 1654378.92 {
		t.Errorf("recomendación = %+v; se esperaba la primera del array (la más barata)", rec)
	}
	if rec.ProveedorNombre != "Cordoba Energia SA" {
		t.Errorf("proveedor_nombre = %q", rec.ProveedorNombre)
	}

	if recibida == nil {
		t.Fatal("el servidor no recibió ninguna request")
	}
	if recibida.Method != http.MethodGet {
		t.Errorf("método = %q; se esperaba GET", recibida.Method)
	}
	if recibida.URL.Path != "/api/v1/proveedores/recomendados" {
		t.Errorf("path = %q; no es el del contrato", recibida.URL.Path)
	}
	if clave := recibida.Header.Get("X-API-Key"); clave != "clave-de-prueba" {
		t.Errorf("X-API-Key = %q; se esperaba la clave configurada", clave)
	}
	esperados := url.Values{"producto": {"PAN-550W-MONO"}, "cantidad": {"10"}, "limite": {"1"}}
	for k, v := range esperados {
		if recibida.URL.Query().Get(k) != v[0] {
			t.Errorf("query %s = %q; se esperaba %q", k, recibida.URL.Query().Get(k), v[0])
		}
	}
}

// TestReintentaAnte503YAciertaAlSegundo: el 503 es transitorio, así que se
// reintenta (es un GET idempotente).
func TestReintentaAnte503YAciertaAlSegundo(t *testing.T) {
	var intentos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		intentos++
		if intentos == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"NO_DISPONIBLE"}`))
			return
		}
		_, _ = w.Write([]byte(dosOfertas))
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave", nil)
	rec, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10)
	if err != nil {
		t.Fatalf("MasBarato: %v", err)
	}
	if rec.ProveedorID != 3 {
		t.Errorf("recomendación = %+v", rec)
	}
	if intentos != 2 {
		t.Errorf("intentos = %d; se esperaban 2 (el 503 y el que anduvo)", intentos)
	}
}

// TestSeAgotanLosReintentos: 3 intentos en total (1 + ReintentosMaximos) y
// después ErrNoDisponible.
func TestSeAgotanLosReintentos(t *testing.T) {
	var intentos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		intentos++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave", nil)
	_, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10)
	if !errors.Is(err, proveedores.ErrNoDisponible) {
		t.Fatalf("error = %v; se esperaba ErrNoDisponible", err)
	}
	if intentos != 1+proveedores.ReintentosMaximos {
		t.Errorf("intentos = %d; se esperaban %d", intentos, 1+proveedores.ReintentosMaximos)
	}
}

// TestNoSeReintentaEl404: el 404 no es transitorio (el SKU no existe para
// proveedores-service), así que se devuelve al primer intento.
func TestNoSeReintentaEl404(t *testing.T) {
	var intentos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		intentos++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"PRODUCTO_NO_ENCONTRADO"}`))
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave", nil)
	_, err := cli.MasBarato(context.Background(), "NO-EXISTE", 5)
	if !errors.Is(err, proveedores.ErrProductoNoEncontrado) {
		t.Fatalf("error = %v; se esperaba ErrProductoNoEncontrado", err)
	}
	if intentos != 1 {
		t.Errorf("intentos = %d; el 404 no se reintenta", intentos)
	}
}

// TestListaVaciaEsSinOfertas: 200 con [] significa que el producto existe pero
// nadie tiene stock suficiente. No es una falla y no se reintenta.
func TestListaVaciaEsSinOfertas(t *testing.T) {
	var intentos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		intentos++
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave", nil)
	_, err := cli.MasBarato(context.Background(), "MON-WIFI", 10000)
	if !errors.Is(err, proveedores.ErrSinOfertas) {
		t.Fatalf("error = %v; se esperaba ErrSinOfertas", err)
	}
	if intentos != 1 {
		t.Errorf("intentos = %d; una lista vacía no se reintenta", intentos)
	}
}

// TestNoSeReintentaEl401: la clave rechazada es un problema de configuración,
// no una caída.
func TestNoSeReintentaEl401(t *testing.T) {
	var intentos int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		intentos++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave-mala", nil)
	_, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10)
	if !errors.Is(err, proveedores.ErrNoAutorizado) {
		t.Fatalf("error = %v; se esperaba ErrNoAutorizado", err)
	}
	if intentos != 1 {
		t.Errorf("intentos = %d; el 401 no se reintenta", intentos)
	}
}

// TestRespuestaFueraDeContrato: un cuerpo que no es el array del contrato se
// rechaza y no se reintenta.
func TestRespuestaFueraDeContrato(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"no":"es un array"}`))
	}))
	defer srv.Close()

	cli := proveedores.NuevoCliente(srv.URL, "clave", nil)
	if _, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10); !errors.Is(err, proveedores.ErrRespuestaInvalida) {
		t.Errorf("error = %v; se esperaba ErrRespuestaInvalida", err)
	}
}

// TestSinServidorEsNoDisponible: el error de transporte se reintenta y termina
// en ErrNoDisponible, que es lo que el ciclo traduce al motivo de la orden.
func TestSinServidorEsNoDisponible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // nadie escucha en ese puerto

	cli := proveedores.NuevoCliente(base, "clave", nil)
	if _, err := cli.MasBarato(context.Background(), "PAN-550W-MONO", 10); !errors.Is(err, proveedores.ErrNoDisponible) {
		t.Errorf("error = %v; se esperaba ErrNoDisponible", err)
	}
}
