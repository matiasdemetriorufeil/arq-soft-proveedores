package fav_test

// Test de contrato de GET /v1/disponibilidad, la operación que FAV agregó en su
// contrato 1.2.0. Se valida contra la copia exacta de ese archivo, no contra la
// 1.1.0 que usan los demás tests de contrato: la 1.1.0 no tiene esta operación.
//
// Contra el FAV real:
//
//	FAV_URL_CONTRATO=https://... FAV_API_KEY=... go test ./internal/fav/ -run ContratoDisponibilidad -v

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/routers"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/favmock"
)

// La operación tiene que seguir en el contrato vigente. Si FAV la saca, este
// test avisa: no se fija la versión exacta a propósito, porque entonces habría
// que editarlo en cada release suyo y dejaría de avisar lo que importa.
func TestContratoDisponibilidadSigueEnElContratoVigente(t *testing.T) {
	doc := cargarContrato(t)
	if doc.Paths.Find("/v1/disponibilidad") == nil {
		t.Fatalf("el contrato vigente (%s) no declara GET /v1/disponibilidad", doc.Info.Version)
	}
	t.Logf("validado contra el contrato %s de FAV", doc.Info.Version)
}

// Nuestra request y la respuesta del mock tienen que cumplir el contrato: el
// parámetro variantes, el header de la clave y el esquema de la respuesta.
func TestContratoDisponibilidadNuestraRequestYLaRespuestaCumplen(t *testing.T) {
	doc := cargarContrato(t)
	var router routers.Router
	var errValidacion error

	m, err := favmock.Nuevo(clavePrueba)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grabadora := httptest.NewRecorder()
		m.Handler().ServeHTTP(grabadora, r)
		res := grabadora.Result()
		cuerpo := grabadora.Body.Bytes()
		if err := validarIdaYVuelta(t, router, r, res.StatusCode, res.Header, cuerpo); err != nil {
			errValidacion = err
		}
		for k, vs := range res.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		w.Write(cuerpo)
	}))
	defer srv.Close()
	router = routerPara(t, doc, srv.URL)

	variantes := []string{"var-remera-basica-blanca-m", "var-jean-mom-celeste-38"}
	stock, errCons := fav.NuevoCliente(srv.URL, clavePrueba, nil).
		ConsultarDisponibilidad(context.Background(), variantes, "corr-contrato")
	if errCons != nil {
		t.Fatalf("la consulta falló: %v", errCons)
	}
	if errValidacion != nil {
		t.Fatalf("la ida y vuelta no cumple el contrato 1.2.0: %v", errValidacion)
	}
	for _, v := range variantes {
		if _, ok := stock[v]; !ok {
			t.Errorf("falta %s en el mapa de stock", v)
		}
	}
}

// El contrato acota variantes a 1..200. Nuestro tope tiene que ser el mismo,
// porque es lo que define el troceado.
func TestContratoDisponibilidadElTopeDeVariantesCoincide(t *testing.T) {
	doc := cargarContrato(t)
	if fav.MaxVariantesPorConsulta != favmock.MaxVariantesDisponibilidad {
		t.Errorf("el cliente trocea en %d y el mock acepta %d: tienen que coincidir",
			fav.MaxVariantesPorConsulta, favmock.MaxVariantesDisponibilidad)
	}
	desc := doc.Paths.Find("/v1/disponibilidad").Get.Parameters.GetByInAndName("query", "variantes").Description
	if !strings.Contains(desc, "200") {
		t.Errorf("el contrato ya no menciona el tope de 200 en la descripción de variantes: %q", desc)
	}
}

// FAV declaró el 404 de /v1/disponibilidad en su contrato 1.3.0. Antes estaba
// solo en su README ("o FAV apagó temporalmente la operación") y no en el
// OpenAPI, así que un cliente que validara estrictamente contra el contrato lo
// habría leído como respuesta fuera de contrato. Se lo reportamos y lo
// agregaron; este test fija las dos mitades: que lo declaran y que nuestro
// cliente lo trata como "operación apagada" y no como una caída.
func TestContratoDisponibilidadDeclaraEl404YLoManejamos(t *testing.T) {
	doc := cargarContrato(t)
	if doc.Paths.Find("/v1/disponibilidad").Get.Responses.Status(http.StatusNotFound) == nil {
		t.Errorf("el contrato %s volvió a no declarar el 404 de /v1/disponibilidad", doc.Info.Version)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"codigo":"NO_ENCONTRADO"}`))
	}))
	defer srv.Close()

	_, err := fav.NuevoCliente(srv.URL, clavePrueba, nil).
		ConsultarDisponibilidad(context.Background(), []string{"var-a"}, "")
	if !errors.Is(err, fav.ErrDisponibilidadApagada) {
		t.Errorf("err = %v; quiero ErrDisponibilidadApagada", err)
	}
	if errors.Is(err, fav.ErrNoDisponible) {
		t.Error("un 404 no es una caída de FAV: no tiene que ser ErrNoDisponible")
	}
}

// Contra el FAV real, solo si se piden las credenciales por variable de
// entorno. Usa el catálogo real para armar la lista de variantes.
func TestContratoDisponibilidadContraElFAVReal(t *testing.T) {
	base, clave := os.Getenv("FAV_URL_CONTRATO"), os.Getenv("FAV_API_KEY")
	if base == "" || clave == "" {
		t.Skip("sin FAV_URL_CONTRATO y FAV_API_KEY: se omite la verificación contra el FAV real")
	}
	c := fav.NuevoCliente(base, clave, nil)
	res, err := c.ListarProductos(context.Background(), "contrato-disponibilidad")
	if err != nil {
		t.Fatalf("no se pudo bajar el catálogo real: %v", err)
	}
	var variantes []string
	for _, p := range res.Catalogo.Items {
		for _, v := range p.Variantes {
			variantes = append(variantes, v.VarianteID)
		}
	}
	if len(variantes) == 0 {
		t.Fatal("el catálogo real no trajo variantes")
	}
	stock, err := c.ConsultarDisponibilidad(context.Background(), variantes, "contrato-disponibilidad")
	if err != nil {
		t.Fatalf("la disponibilidad real falló: %v", err)
	}
	if len(stock) != len(variantes) {
		t.Errorf("stock para %d variantes; se pidieron %d", len(stock), len(variantes))
	}
	cuerpo, _ := json.Marshal(stock)
	t.Logf("disponibilidad real de %d variantes: %s", len(stock), cuerpo)
}
