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
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/favmock"
)

const rutaContrato12 = "../../../../docs/contracts/consumidos/fav-catalogo-pedidos-v1.2.0.openapi.yaml"

func cargarContrato12(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(rutaContrato12)
	if err != nil {
		t.Fatalf("no se pudo leer el contrato 1.2.0 de FAV: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("el contrato 1.2.0 de FAV no es un OpenAPI válido: %v", err)
	}
	return doc
}

// La 1.2.0 tiene que traer la operación nueva. Si FAV la saca, este test avisa.
func TestContratoDisponibilidadExisteEnLa120(t *testing.T) {
	doc := cargarContrato12(t)
	if doc.Paths.Find("/v1/disponibilidad") == nil {
		t.Fatal("el contrato 1.2.0 no declara GET /v1/disponibilidad")
	}
	if v := doc.Info.Version; v != "1.2.0" {
		t.Errorf("info.version = %q; se esperaba 1.2.0", v)
	}
}

// Nuestra request y la respuesta del mock tienen que cumplir el contrato: el
// parámetro variantes, el header de la clave y el esquema de la respuesta.
func TestContratoDisponibilidadNuestraRequestYLaRespuestaCumplen(t *testing.T) {
	doc := cargarContrato12(t)
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
	doc := cargarContrato12(t)
	if fav.MaxVariantesPorConsulta != favmock.MaxVariantesDisponibilidad {
		t.Errorf("el cliente trocea en %d y el mock acepta %d: tienen que coincidir",
			fav.MaxVariantesPorConsulta, favmock.MaxVariantesDisponibilidad)
	}
	desc := doc.Paths.Find("/v1/disponibilidad").Get.Parameters.GetByInAndName("query", "variantes").Description
	if !strings.Contains(desc, "200") {
		t.Errorf("el contrato ya no menciona el tope de 200 en la descripción de variantes: %q", desc)
	}
}

// Hueco del contrato de FAV, encontrado el 11/11: su README documenta
// "404 NO_ENCONTRADO ... o FAV apagó temporalmente la operación
// (GET /v1/disponibilidad)", pero el OpenAPI 1.2.0 NO declara el 404 para esa
// ruta. Nuestro cliente lo trata igual, como ErrDisponibilidadApagada, porque
// confiar solo en el OpenAPI haría que un 404 se leyera como respuesta fuera de
// contrato y tirara la importación entera.
//
// Este test documenta la diferencia. Si FAV declara el 404, deja de fallar la
// condición y hay que actualizar el comentario, no el comportamiento.
func TestContratoDisponibilidadEl404NoEstaDeclaradoPeroLoManejamos(t *testing.T) {
	doc := cargarContrato12(t)
	resp := doc.Paths.Find("/v1/disponibilidad").Get.Responses
	if resp.Status(http.StatusNotFound) != nil {
		t.Log("FAV ahora declara el 404 en /v1/disponibilidad: se puede sacar la nota del ADR-009")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"codigo":"NO_ENCONTRADO"}`))
	}))
	defer srv.Close()

	_, err := fav.NuevoCliente(srv.URL, clavePrueba, nil).
		ConsultarDisponibilidad(context.Background(), []string{"var-a"}, "")
	if err == nil {
		t.Fatal("un 404 tiene que devolver error")
	}
	if !strings.Contains(err.Error(), "disponibilidad") {
		t.Errorf("err = %v; se esperaba el error propio de la operación apagada", err)
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
