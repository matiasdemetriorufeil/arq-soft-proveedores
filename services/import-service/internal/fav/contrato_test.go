package fav_test

// Test de contrato sobre la capacidad que consumimos (consigna, defensa
// individual): todo se valida contra la copia exacta del OpenAPI de FAV v1.1.0
// que está en docs/contracts/consumidos/. Si FAV publica un cambio
// incompatible y se actualiza esa copia, o si nuestro cliente o el mock se
// desvían, estos tests fallan.
//
// Contra el FAV real (cuando tengamos su URL):
//
//	FAV_URL_CONTRATO=https://... FAV_API_KEY=... go test ./internal/fav/ -run Contrato -v

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/favmock"
)

const rutaContrato = "../../../../docs/contracts/consumidos/fav-catalogo-pedidos-v1.1.0.openapi.yaml"

func init() {
	// Los errores de FAV vienen como application/problem+json.
	openapi3filter.RegisterBodyDecoder("application/problem+json", func(body io.Reader, _ http.Header,
		_ *openapi3.SchemaRef, _ openapi3filter.EncodingFn) (any, error) {
		var v any
		err := json.NewDecoder(body).Decode(&v)
		return v, err
	})
}

func cargarContrato(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(rutaContrato)
	if err != nil {
		t.Fatalf("no se pudo leer el contrato de FAV: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("el contrato de FAV no es un OpenAPI válido: %v", err)
	}
	return doc
}

// routerPara apunta el contrato a la URL del servidor que se prueba.
func routerPara(t *testing.T, doc *openapi3.T, base string) routers.Router {
	t.Helper()
	doc.Servers = openapi3.Servers{{URL: strings.TrimRight(base, "/")}}
	r, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var opciones = &openapi3filter.Options{
	// La seguridad se valida aparte: acá alcanza con que el header exista.
	AuthenticationFunc: func(_ context.Context, in *openapi3filter.AuthenticationInput) error {
		if in.RequestValidationInput.Request.Header.Get("X-API-Key") == "" {
			return in.NewError(nil)
		}
		return nil
	},
	IncludeResponseStatus: true,
	MultiError:            true,
}

// validarIdaYVuelta valida la request contra el contrato y, si hay respuesta,
// también la respuesta.
func validarIdaYVuelta(t *testing.T, router routers.Router, req *http.Request, status int, header http.Header, cuerpo []byte) error {
	t.Helper()
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("la ruta %s %s no está en el contrato: %v", req.Method, req.URL.Path, err)
	}
	entrada := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route, Options: opciones}
	if err := openapi3filter.ValidateRequest(context.Background(), entrada); err != nil {
		return err
	}
	return openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: entrada,
		Status:                 status,
		Header:                 header,
		Body:                   io.NopCloser(bytes.NewReader(cuerpo)),
		Options:                opciones,
	})
}

func TestContratoNuestroClienteEntiendeElEjemploDelContrato(t *testing.T) {
	doc := cargarContrato(t)
	ej := doc.Components.Examples["Catalogo"]
	if ej == nil || ej.Value == nil {
		t.Fatal("el contrato no trae el ejemplo Catalogo")
	}
	cuerpo, _ := json.Marshal(ej.Value.Value)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cuerpo)
	}))
	defer srv.Close()

	res, err := fav.NuevoCliente(srv.URL, "clave", nil).ListarProductos(context.Background(), "")
	if err != nil {
		t.Fatalf("nuestro cliente rechazó el ejemplo del propio contrato: %v", err)
	}
	n := dominio.Normalizar(res.Catalogo, dominio.ReglasFAV(0))
	if len(n.Items) != 3 || len(n.Descartes) != 0 {
		t.Errorf("del ejemplo salieron %d ofertas y %d descartes; quiero 3 y 0", len(n.Items), len(n.Descartes))
	}
}

func TestContratoNuestraRequestCumpleElContrato(t *testing.T) {
	doc := cargarContrato(t)
	var router routers.Router
	var errReq error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := router.FindRoute(r)
		if err != nil {
			errReq = err
		} else {
			errReq = openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
				Request: r, PathParams: params, Route: route, Options: opciones,
			})
		}
		w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()
	router = routerPara(t, doc, srv.URL)

	if _, err := fav.NuevoCliente(srv.URL, "clave-del-grupo", nil).ListarProductos(context.Background(), "corr-123"); err != nil {
		t.Fatal(err)
	}
	if errReq != nil {
		t.Errorf("la request de nuestro cliente no cumple el contrato: %v", errReq)
	}
}

func TestContratoElMockRespondeSegunElContrato(t *testing.T) {
	doc := cargarContrato(t)
	mock, err := favmock.Nuevo("clave-mock")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()
	router := routerPara(t, doc, srv.URL)

	casos := []struct {
		modo, clave string
		status      int
		cumple      bool
	}{
		{favmock.ModoNormal, "clave-mock", 200, true},
		{favmock.ModoNormal, "otra", 401, true},
		{favmock.ModoCaido, "clave-mock", 503, true},
		{favmock.ModoLimite, "clave-mock", 429, true},
		{favmock.ModoError, "clave-mock", 500, true},
		// El modo inválido existe para probar que el cliente rechaza un
		// catálogo fuera de contrato; el validador también lo tiene que ver.
		{favmock.ModoInvalido, "clave-mock", 200, false},
	}
	for _, c := range casos {
		t.Run(c.modo+"/"+c.clave, func(t *testing.T) {
			cambiar, _ := http.NewRequest(http.MethodPost, srv.URL+"/_mock/modo", strings.NewReader(`{"modo":"`+c.modo+`"}`))
			if r, err := http.DefaultClient.Do(cambiar); err != nil || r.StatusCode != 200 {
				t.Fatalf("no se pudo cambiar el modo: %v", err)
			}

			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/productos", nil)
			req.Header.Set("X-API-Key", c.clave)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			cuerpo, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.status {
				t.Fatalf("status = %d, quiero %d", resp.StatusCode, c.status)
			}
			err = validarIdaYVuelta(t, router, req, resp.StatusCode, resp.Header, cuerpo)
			if c.cumple && err != nil {
				t.Errorf("la respuesta del mock no cumple el contrato de FAV: %v\n%s", err, cuerpo)
			}
			if !c.cumple && err == nil {
				t.Error("el validador no detectó una respuesta fuera de contrato")
			}
		})
	}
}

func TestContratoContraFAVReal(t *testing.T) {
	base, clave := os.Getenv("FAV_URL_CONTRATO"), os.Getenv("FAV_API_KEY")
	if base == "" || clave == "" {
		t.Skip("sin FAV_URL_CONTRATO y FAV_API_KEY: el test contra el FAV desplegado se saltea")
	}
	doc := cargarContrato(t)
	router := routerPara(t, doc, base)

	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/v1/productos", nil)
	req.Header.Set("X-API-Key", clave)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("FAV no respondió: %v", err)
	}
	cuerpo, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := validarIdaYVuelta(t, router, req, resp.StatusCode, resp.Header, cuerpo); err != nil {
		t.Errorf("la respuesta de FAV no cumple su propio contrato v1.1.0: %v", err)
	}
	if resp.StatusCode == 200 {
		var cat fav.Catalogo
		json.Unmarshal(cuerpo, &cat)
		t.Logf("FAV devolvió %d productos", len(cat.Items))
	}
}
