package fav_test

// Test de contrato de lo que stock-service consume de FAV, contra las copias
// exactas de su OpenAPI en docs/contracts/consumidos/.
//
// Los pedidos y la consulta de estado se validan contra la **1.3.0**, que es la
// vigente. El webhook se valida contra la **1.1.0**, que es la última versión
// que lo especificó: FAV lo quitó en la 1.2.0 y no lo devolvió en la 1.3.0, así
// que nuestro receptor existe pero no tiene quien lo llame. Se conserva esa
// verificación porque el código sigue en el servicio y porque, si FAV alguna vez
// lo publica, este test dice exactamente contra qué lo probamos.
//
// Las respuestas del mock de FAV contra el mismo contrato se validan en
// services/import-service/internal/fav/contrato_pedidos_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
)

// rutaContrato es la versión VIGENTE de FAV: pedidos y consulta de estado.
const rutaContrato = "../../../../docs/contracts/consumidos/fav-catalogo-pedidos-v1.3.0.openapi.yaml"

// rutaContratoConWebhook es la última versión que especificó el webhook.
const rutaContratoConWebhook = "../../../../docs/contracts/consumidos/fav-catalogo-pedidos-v1.1.0.openapi.yaml"

func cargarContrato(t *testing.T) *openapi3.T {
	t.Helper()
	return cargarContratoDe(t, rutaContrato)
}

func cargarContratoDe(t *testing.T, ruta string) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", ruta, err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("%s no es un OpenAPI válido: %v", ruta, err)
	}
	return doc
}

func ejemplo(t *testing.T, doc *openapi3.T, nombre string) []byte {
	t.Helper()
	ej := doc.Components.Examples[nombre]
	if ej == nil || ej.Value == nil {
		t.Fatalf("el contrato no trae el ejemplo %s", nombre)
	}
	b, _ := json.Marshal(ej.Value.Value)
	return b
}

var opcionesContrato = &openapi3filter.Options{
	AuthenticationFunc: func(_ context.Context, in *openapi3filter.AuthenticationInput) error {
		if in.RequestValidationInput.Request.Header.Get("X-API-Key") == "" {
			return in.NewError(nil)
		}
		return nil
	},
	MultiError: true,
}

// servidorQueValida responde `respuesta` y guarda si la request cumplió el
// contrato.
func servidorQueValida(t *testing.T, doc *openapi3.T, status int, respuesta []byte) (*httptest.Server, *error) {
	t.Helper()
	var errReq error = errors.New("no llegó ninguna request")
	srv := httptest.NewServer(nil)
	doc.Servers = openapi3.Servers{{URL: srv.URL}}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		route, params, err := router.FindRoute(r)
		if err != nil {
			errReq = err
		} else {
			errReq = openapi3filter.ValidateRequest(r.Context(), &openapi3filter.RequestValidationInput{
				Request: r, PathParams: params, Route: route, Options: opcionesContrato,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(respuesta)
	})
	t.Cleanup(srv.Close)
	return srv, &errReq
}

func TestContratoNuestroPedidoCumpleElContrato(t *testing.T) {
	doc := cargarContrato(t)
	srv, errReq := servidorQueValida(t, doc, 201, ejemplo(t, doc, "Pedido"))
	p, err := fav.NuevoCliente(srv.URL, "clave-grupo", nil).CrearPedido(context.Background(), "3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90-1",
		fav.PedidoNuevo{
			ReferenciaExterna: "3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90",
			Items:             []fav.ItemPedido{{VarianteID: "var-remera-basica-blanca-m", Cantidad: fav.MaxUnidadesPorVariante}},
		})
	if err != nil {
		t.Fatalf("nuestro cliente no entiende el ejemplo Pedido del contrato: %v", err)
	}
	if *errReq != nil {
		t.Errorf("nuestro POST /v1/pedidos no cumple el contrato de FAV: %v", *errReq)
	}
	if p.PedidoID != "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f" || p.Estado != "PENDIENTE_DE_PAGO" || p.Total != 87000 {
		t.Errorf("pedido leído del ejemplo = %+v", p)
	}

	// El validador tiene que ver un pedido fuera de contrato: por eso la capa
	// de servicio no deja salir más de 10 unidades por variante.
	srv, errReq = servidorQueValida(t, cargarContrato(t), 201, ejemplo(t, doc, "Pedido"))
	fav.NuevoCliente(srv.URL, "clave-grupo", nil).CrearPedido(context.Background(), "orden-0001-1",
		fav.PedidoNuevo{Items: []fav.ItemPedido{{VarianteID: "v", Cantidad: fav.MaxUnidadesPorVariante + 1}}})
	if *errReq == nil {
		t.Error("el validador no detectó un pedido de 11 unidades, fuera de contrato")
	}
}

func TestContratoNuestraConsultaCumpleElContrato(t *testing.T) {
	doc := cargarContrato(t)
	srv, errReq := servidorQueValida(t, doc, 200, ejemplo(t, doc, "PedidoPagado"))
	p, err := fav.NuevoCliente(srv.URL, "clave-grupo", nil).ConsultarPedido(context.Background(), "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f")
	if err != nil {
		t.Fatalf("nuestro cliente no entiende el ejemplo PedidoPagado: %v", err)
	}
	if *errReq != nil {
		t.Errorf("nuestro GET /v1/pedidos/{pedidoId} no cumple el contrato: %v", *errReq)
	}
	if p.Estado != "PAGADO" {
		t.Errorf("estado = %s; quiero PAGADO", p.Estado)
	}
}

// Cada error de ejemplo del contrato se traduce al error que corresponde.
func TestContratoErroresDelContrato(t *testing.T) {
	doc := cargarContrato(t)
	respuestas := doc.Paths.Find("/v1/pedidos").Post.Responses
	ejemploDe := func(status, nombre string) []byte {
		r := respuestas.Value(status)
		var ej *openapi3.ExampleRef
		if r.Value.Content != nil {
			ej = r.Value.Content.Get("application/problem+json").Examples[nombre]
		}
		if ej == nil || ej.Value == nil {
			t.Fatalf("el contrato no trae el ejemplo %s del %s", nombre, status)
		}
		b, _ := json.Marshal(ej.Value.Value)
		return b
	}
	casos := []struct {
		status        int
		texto, nombre string
		quiero        error
	}{
		{409, "409", "stockInsuficiente", fav.ErrStockInsuficiente},
		{409, "409", "claveReutilizada", fav.ErrPedidoRechazado},
		{400, "400", "varianteInexistente", fav.ErrPedidoRechazado},
		{400, "400", "pedidoInvalido", fav.ErrPedidoRechazado},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			srv, _ := servidorQueValida(t, cargarContrato(t), c.status, ejemploDe(c.texto, c.nombre))
			_, err := fav.NuevoCliente(srv.URL, "k", nil).CrearPedido(context.Background(), "orden-0001-1",
				fav.PedidoNuevo{Items: []fav.ItemPedido{{VarianteID: "v", Cantidad: 1}}})
			if !errors.Is(err, c.quiero) {
				t.Errorf("err = %v; quiero %v", err, c.quiero)
			}
		})
	}
}

// El webhook no está en el contrato vigente: este test usa la 1.1.0, que fue la
// última que lo especificó. Ver la nota de arriba.
func TestContratoElAvisoDeEjemploVerificaYSeLee(t *testing.T) {
	doc := cargarContratoDe(t, rutaContratoConWebhook)
	cb := (*doc.Paths.Find("/v1/pedidos").Post.Callbacks["pedidoActualizado"].Value.Value("{$request.body#/notificacionUrl}")).Post
	var headerEjemplo string
	for _, p := range cb.Parameters {
		if p.Value.Name == "X-FAV-Firma" {
			headerEjemplo, _ = p.Value.Example.(string)
		}
	}
	if headerEjemplo != firmaVector {
		t.Fatalf("el ejemplo de X-FAV-Firma del contrato cambió: %q", headerEjemplo)
	}
	// El ejemplo serializado como lo envía FAV: en una línea, sin espacios y
	// con las claves en el orden del contrato.
	if !strings.Contains(string(ejemplo(t, doc, "EventoPagado")), `"3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b"`) {
		t.Fatal("el ejemplo EventoPagado del contrato cambió")
	}
	if err := fav.VerificarFirma(secretoMock, headerEjemplo, []byte(cuerpoVector), time.Unix(1792243201, 0)); err != nil {
		t.Errorf("nuestro receptor rechaza el vector de prueba del contrato: %v", err)
	}
	var v any
	json.Unmarshal([]byte(cuerpoVector), &v)
	if err := doc.Components.Schemas["EventoPedido"].Value.VisitJSON(v); err != nil {
		t.Errorf("el cuerpo del vector no cumple EventoPedido: %v", err)
	}
}

// Contra el FAV real, solo con credenciales por variable de entorno. Es una
// consulta de solo lectura con un id al azar: **no crea ningún pedido**, porque
// un POST /v1/pedidos aparta stock de verdad en su sistema durante 48 horas.
//
// Lo que verifica es justo lo que nos faltaba: que la operación que FAV había
// quitado en su 1.2.0 está de vuelta y desplegada, y que responde el código que
// declara su 1.3.0 (PEDIDO_INEXISTENTE) y no el genérico de ruta inexistente.
//
//	FAV_URL_CONTRATO=https://... FAV_API_KEY=... go test ./internal/fav/ -run ContratoConsultaReal -v
func TestContratoConsultaRealDePedidoInexistente(t *testing.T) {
	base, clave := os.Getenv("FAV_URL_CONTRATO"), os.Getenv("FAV_API_KEY")
	if base == "" || clave == "" {
		t.Skip("sin FAV_URL_CONTRATO y FAV_API_KEY: se omite la verificación contra el FAV real")
	}
	c := fav.NuevoCliente(base, clave, nil)
	_, err := c.ConsultarPedido(context.Background(), "00000000-0000-4000-8000-000000000000")
	if !errors.Is(err, fav.ErrPedidoInexistente) {
		t.Fatalf("err = %v; quiero ErrPedidoInexistente. Si da el error de ruta inexistente, "+
			"FAV todavía no desplegó su 1.3.0", err)
	}
	t.Log("GET /v1/pedidos/{pedidoId} está desplegado y responde PEDIDO_INEXISTENTE")
}
