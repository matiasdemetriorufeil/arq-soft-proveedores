package fav_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
)

const clavePrueba = "clave-de-prueba"

// servidorDisponibilidad responde /v1/disponibilidad con el stock del mapa y
// anota cada tanda que recibió, para poder comprobar el troceado.
func servidorDisponibilidad(t *testing.T, stock map[string]int, tandas *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/disponibilidad" {
			t.Errorf("ruta inesperada: %s", r.URL.Path)
		}
		ids := strings.Split(r.URL.Query().Get("variantes"), ",")
		if tandas != nil {
			*tandas = append(*tandas, ids)
		}
		var sb strings.Builder
		sb.WriteString(`{"items":[`)
		for i, id := range ids {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"varianteId":%q,"disponible":%d}`, id, stock[id])
		}
		sb.WriteString(`]}`)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(sb.String()))
	}))
}

func TestConsultarDisponibilidadDevuelveElStockPorVariante(t *testing.T) {
	stock := map[string]int{"var-a": 7, "var-b": 0, "var-c": 3}
	srv := servidorDisponibilidad(t, stock, nil)
	defer srv.Close()

	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	got, err := c.ConsultarDisponibilidad(context.Background(), []string{"var-a", "var-b", "var-c"}, "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	for id, quiero := range stock {
		if got[id] != quiero {
			t.Errorf("%s = %d; quiero %d", id, got[id], quiero)
		}
	}
}

// El contrato de FAV informa con 0 tanto la variante inexistente como la sin
// stock. Una variante que FAV no devuelve tiene que quedar en 0 y no ausente,
// porque quien normaliza usa el cero para dejar la oferta afuera del ranking.
func TestConsultarDisponibilidadLaVarianteQueFAVNoDevuelveQueda0(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"items":[{"varianteId":"var-a","disponible":4}]}`))
	}))
	defer srv.Close()

	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	got, err := c.ConsultarDisponibilidad(context.Background(), []string{"var-a", "var-fantasma"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got["var-a"] != 4 {
		t.Errorf("var-a = %d; quiero 4", got["var-a"])
	}
	v, presente := got["var-fantasma"]
	if !presente || v != 0 {
		t.Errorf("var-fantasma = (%d, presente=%v); quiero (0, true)", v, presente)
	}
}

// El contrato acepta entre 1 y 200 ids por llamada: con más hay que trocear o
// FAV devuelve 400.
func TestConsultarDisponibilidadTroceaEnTandasDe200(t *testing.T) {
	var tandas [][]string
	srv := servidorDisponibilidad(t, map[string]int{}, &tandas)
	defer srv.Close()

	ids := make([]string, 0, 450)
	for i := 0; i < 450; i++ {
		ids = append(ids, fmt.Sprintf("var-%03d", i))
	}
	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	got, err := c.ConsultarDisponibilidad(context.Background(), ids, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tandas) != 3 {
		t.Fatalf("tandas = %d; quiero 3 (200 + 200 + 50)", len(tandas))
	}
	for i, quiero := range []int{200, 200, 50} {
		if len(tandas[i]) != quiero {
			t.Errorf("tanda %d tiene %d ids; quiero %d", i, len(tandas[i]), quiero)
		}
	}
	if len(got) != 450 {
		t.Errorf("el mapa tiene %d variantes; quiero 450", len(got))
	}
}

// Su README avisa que FAV puede apagar la operación: el 404 tiene que volver
// como ErrDisponibilidadApagada y no como una caída, para que la importación
// siga con el tope fijo en lugar de no importar nada.
func TestConsultarDisponibilidadApagadaDevuelveSuPropioError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"codigo":"NO_ENCONTRADO"}`))
	}))
	defer srv.Close()

	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	_, err := c.ConsultarDisponibilidad(context.Background(), []string{"var-a"}, "")
	if !errors.Is(err, fav.ErrDisponibilidadApagada) {
		t.Errorf("err = %v; quiero ErrDisponibilidadApagada", err)
	}
	if errors.Is(err, fav.ErrNoDisponible) {
		t.Error("un 404 no es una caída de FAV: no tiene que ser ErrNoDisponible")
	}
}

func TestConsultarDisponibilidadReintentaAnte503YAciertaDespues(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"codigo":"NO_DISPONIBLE"}`))
			return
		}
		w.Write([]byte(`{"items":[{"varianteId":"var-a","disponible":9}]}`))
	}))
	defer srv.Close()

	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	got, err := c.ConsultarDisponibilidad(context.Background(), []string{"var-a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got["var-a"] != 9 {
		t.Errorf("var-a = %d; quiero 9", got["var-a"])
	}
	if n != 2 {
		t.Errorf("intentos = %d; quiero 2", n)
	}
}

func TestConsultarDisponibilidadClaveRechazadaNoSeReintenta(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"codigo":"NO_AUTENTICADO"}`))
	}))
	defer srv.Close()

	c := fav.NuevoCliente(srv.URL, clavePrueba, nil)
	if _, err := c.ConsultarDisponibilidad(context.Background(), []string{"var-a"}, ""); !errors.Is(err, fav.ErrNoAutenticado) {
		t.Errorf("err = %v; quiero ErrNoAutenticado", err)
	}
	if n != 1 {
		t.Errorf("intentos = %d; quiero 1: una clave rechazada no se arregla reintentando", n)
	}
}
