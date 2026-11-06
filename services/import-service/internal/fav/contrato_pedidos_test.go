package fav_test

// Test de contrato de los pedidos en el mock de FAV: cada respuesta de
// POST /v1/pedidos y GET /v1/pedidos/{pedidoId}, y cada webhook que envía,
// tiene que validar contra la copia del OpenAPI de FAV v1.1.0. stock-service
// prueba su cliente y su receptor contra este mismo mock.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/favmock"
)

type respuesta struct {
	status int
	header http.Header
	cuerpo []byte
}

// pedirYValidar hace la request y valida ida y vuelta contra el contrato. La
// request se arma dos veces porque la primera consume el cuerpo al enviarse.
func pedirYValidar(t *testing.T, router routers.Router, base, metodo, ruta, cuerpo string, headers map[string]string) respuesta {
	t.Helper()
	armar := func() *http.Request {
		var body io.Reader
		if cuerpo != "" {
			body = strings.NewReader(cuerpo)
		}
		req, _ := http.NewRequest(metodo, base+ruta, body)
		if cuerpo != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req
	}
	resp, err := http.DefaultClient.Do(armar())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := validarIdaYVuelta(t, router, armar(), resp.StatusCode, resp.Header, b); err != nil {
		t.Errorf("%s %s → %d no cumple el contrato de FAV: %v\n%s", metodo, ruta, resp.StatusCode, err, b)
	}
	return respuesta{resp.StatusCode, resp.Header, b}
}

func codigoDe(t *testing.T, r respuesta) string {
	t.Helper()
	var p struct {
		Codigo string `json:"codigo"`
	}
	json.Unmarshal(r.cuerpo, &p)
	return p.Codigo
}

func TestContratoPedidosDelMock(t *testing.T) {
	doc := cargarContrato(t)
	mock, err := favmock.Nuevo("clave-mock")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()
	router := routerPara(t, doc, srv.URL)

	const cuerpo = `{"referenciaExterna":"orden-1","items":[{"varianteId":"var-remera-basica-blanca-m","cantidad":3},{"varianteId":"var-jean-mom-celeste-38","cantidad":1}]}`
	con := func(clave string) map[string]string {
		return map[string]string{"X-API-Key": "clave-mock", "Idempotency-Key": clave}
	}

	creado := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos", cuerpo, con("orden-1-1"))
	if creado.status != 201 {
		t.Fatalf("crear: status %d, quiero 201: %s", creado.status, creado.cuerpo)
	}
	var p struct {
		PedidoID string  `json:"pedidoId"`
		Estado   string  `json:"estado"`
		Total    float64 `json:"total"`
	}
	json.Unmarshal(creado.cuerpo, &p)
	if p.Estado != "PENDIENTE_DE_PAGO" || p.Total != 3*15000+42000 {
		t.Errorf("pedido creado = %+v; quiero PENDIENTE_DE_PAGO y total 87000", p)
	}

	t.Run("misma clave y mismo cuerpo devuelve el mismo pedido", func(t *testing.T) {
		r := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos", cuerpo, con("orden-1-1"))
		if r.status != 201 || !bytes.Equal(r.cuerpo, creado.cuerpo) {
			t.Errorf("status %d; el reintento tiene que devolver exactamente la misma respuesta", r.status)
		}
	})
	t.Run("misma clave con otro cuerpo es 409", func(t *testing.T) {
		r := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos", strings.Replace(cuerpo, `"cantidad":3`, `"cantidad":2`, 1), con("orden-1-1"))
		if r.status != 409 || codigoDe(t, r) != "CLAVE_IDEMPOTENCIA_REUTILIZADA" {
			t.Errorf("status %d %s; quiero 409 CLAVE_IDEMPOTENCIA_REUTILIZADA", r.status, r.cuerpo)
		}
	})
	t.Run("variante inexistente es 400", func(t *testing.T) {
		r := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos", `{"items":[{"varianteId":"var-no-existe","cantidad":1}]}`, con("clave-inexistente"))
		if r.status != 400 || codigoDe(t, r) != "VARIANTE_INEXISTENTE" {
			t.Errorf("status %d %s; quiero 400 VARIANTE_INEXISTENTE", r.status, r.cuerpo)
		}
	})
	t.Run("stock insuficiente es 409 y no aparta nada", func(t *testing.T) {
		http.Post(srv.URL+"/_mock/stock", "application/json", strings.NewReader(`{"varianteId":"var-buzo-canguro-gris-m","stock":2}`))
		r := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos",
			`{"items":[{"varianteId":"var-buzo-canguro-azul-l","cantidad":1},{"varianteId":"var-buzo-canguro-gris-m","cantidad":3}]}`, con("clave-stock-1"))
		if r.status != 409 || codigoDe(t, r) != "STOCK_INSUFICIENTE" {
			t.Errorf("status %d %s; quiero 409 STOCK_INSUFICIENTE", r.status, r.cuerpo)
		}
		// Todo o nada: la variante que sí alcanzaba tampoco se apartó, así que
		// se pueden pedir sus 30 unidades en tres pedidos de 10.
		for i, clave := range []string{"clave-stock-2", "clave-stock-3", "clave-stock-4"} {
			r = pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos",
				`{"items":[{"varianteId":"var-buzo-canguro-azul-l","cantidad":10}]}`, con(clave))
			if r.status != 201 {
				t.Errorf("pedido %d de 10 unidades: status %d; el 409 anterior apartó stock", i+1, r.status)
			}
		}
	})
	// Los dos casos de 400 PEDIDO_INVALIDO violan la request a propósito, así
	// que acá se valida solo la respuesta (la request no cumple el contrato).
	t.Run("sin Idempotency-Key o con 11 unidades es 400 PEDIDO_INVALIDO", func(t *testing.T) {
		for _, c := range []struct{ cuerpo, clave string }{
			{cuerpo, ""},
			{`{"items":[{"varianteId":"var-jean-mom-negro-40","cantidad":11}]}`, "clave-11-unidades"},
		} {
			req, _ := http.NewRequest("POST", srv.URL+"/v1/pedidos", strings.NewReader(c.cuerpo))
			req.Header.Set("X-API-Key", "clave-mock")
			if c.clave != "" {
				req.Header.Set("Idempotency-Key", c.clave)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 400 || codigoDe(t, respuesta{cuerpo: b}) != "PEDIDO_INVALIDO" {
				t.Errorf("status %d %s; quiero 400 PEDIDO_INVALIDO", resp.StatusCode, b)
			}
			validarSchema(t, doc, "Problema", b)
		}
	})
	t.Run("consulta del pedido y pedido inexistente", func(t *testing.T) {
		r := pedirYValidar(t, router, srv.URL, "GET", "/v1/pedidos/"+p.PedidoID, "", map[string]string{"X-API-Key": "clave-mock"})
		if r.status != 200 {
			t.Errorf("status %d", r.status)
		}
		r = pedirYValidar(t, router, srv.URL, "GET", "/v1/pedidos/7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f", "", map[string]string{"X-API-Key": "clave-mock"})
		if r.status != 404 || codigoDe(t, r) != "PEDIDO_INEXISTENTE" {
			t.Errorf("status %d; quiero 404 PEDIDO_INEXISTENTE", r.status)
		}
	})
	t.Run("FAV caído al pedir es 503", func(t *testing.T) {
		http.Post(srv.URL+"/_mock/modo", "application/json", strings.NewReader(`{"modo":"caido"}`))
		defer http.Post(srv.URL+"/_mock/modo", "application/json", strings.NewReader(`{"modo":"normal"}`))
		r := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos", cuerpo, con("clave-caido"))
		if r.status != 503 {
			t.Errorf("status %d; quiero 503", r.status)
		}
	})
}

func validarSchema(t *testing.T, doc *openapi3.T, nombre string, cuerpo []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(cuerpo, &v); err != nil {
		t.Fatalf("%s no es JSON: %v", nombre, err)
	}
	if err := doc.Components.Schemas[nombre].Value.VisitJSON(v); err != nil {
		t.Errorf("no cumple el esquema %s del contrato: %v\n%s", nombre, err, cuerpo)
	}
}

var patronFirma = regexp.MustCompile(`^t=\d+,v1=[0-9a-f]{64}$`)

// verificar recalcula la firma como lo tiene que hacer el receptor.
func verificar(secreto, header string, cuerpo []byte) bool {
	partes := strings.SplitN(header, ",", 2)
	if len(partes) != 2 {
		return false
	}
	t, v1 := strings.TrimPrefix(partes[0], "t="), strings.TrimPrefix(partes[1], "v1=")
	mac := hmac.New(sha256.New, []byte(secreto))
	mac.Write([]byte(t + "."))
	mac.Write(cuerpo)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(v1))
}

func TestContratoWebhookDelMock(t *testing.T) {
	doc := cargarContrato(t)
	type recibido struct {
		header http.Header
		cuerpo []byte
	}
	var mu sync.Mutex
	var avisos []recibido
	receptor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		avisos = append(avisos, recibido{r.Header.Clone(), b})
		mu.Unlock()
	}))
	defer receptor.Close()

	mock, _ := favmock.Nuevo("clave-mock")
	srv := httptest.NewServer(mock.Handler())
	defer srv.Close()
	router := routerPara(t, doc, srv.URL)

	creado := pedirYValidar(t, router, srv.URL, "POST", "/v1/pedidos",
		`{"referenciaExterna":"orden-2","notificacionUrl":"`+receptor.URL+`/webhooks/fav","items":[{"varianteId":"var-jean-mom-negro-40","cantidad":2}]}`,
		map[string]string{"X-API-Key": "clave-mock", "Idempotency-Key": "orden-2-1"})
	var p struct {
		PedidoID string `json:"pedidoId"`
	}
	json.Unmarshal(creado.cuerpo, &p)

	resp, err := http.Post(srv.URL+"/_mock/pedidos/"+p.PedidoID+"/estado", "application/json", strings.NewReader(`{"estado":"PAGADO","duplicar":true}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("no se pudo cambiar el estado: %v", err)
	}
	resp, _ = http.Post(srv.URL+"/_mock/pedidos/"+p.PedidoID+"/estado", "application/json", strings.NewReader(`{"estado":"VENCIDO"}`))
	if resp.StatusCode != 409 {
		t.Errorf("PAGADO → VENCIDO respondió %d; los estados solo avanzan, quiero 409", resp.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(avisos) != 2 {
		t.Fatalf("llegaron %d avisos; con duplicar quiero 2", len(avisos))
	}
	for _, a := range avisos {
		validarSchema(t, doc, "EventoPedido", a.cuerpo)
		firma := a.header.Get("X-FAV-Firma")
		if !patronFirma.MatchString(firma) {
			t.Errorf("X-FAV-Firma %q no cumple el formato del contrato", firma)
		}
		if !verificar(favmock.SecretoDeAvisosPorDefecto, firma, a.cuerpo) {
			t.Error("la firma del aviso no verifica con el secreto de avisos")
		}
		var ev struct {
			EventoID string `json:"eventoId"`
			Pedido   struct {
				Estado string `json:"estado"`
			} `json:"pedido"`
		}
		json.Unmarshal(a.cuerpo, &ev)
		if a.header.Get("X-FAV-Evento-Id") != ev.EventoID || ev.Pedido.Estado != "PAGADO" {
			t.Errorf("X-FAV-Evento-Id %q, cuerpo %s", a.header.Get("X-FAV-Evento-Id"), a.cuerpo)
		}
	}
	if avisos[0].header.Get("X-FAV-Evento-Id") != avisos[1].header.Get("X-FAV-Evento-Id") {
		t.Error("un aviso repetido tiene que traer el mismo eventoId")
	}

	r := pedirYValidar(t, router, srv.URL, "GET", "/v1/pedidos/"+p.PedidoID, "", map[string]string{"X-API-Key": "clave-mock"})
	if !strings.Contains(string(r.cuerpo), `"estado":"PAGADO"`) {
		t.Errorf("la consulta no muestra el estado nuevo: %s", r.cuerpo)
	}
}

func TestContratoVectorDeFirmaDelContrato(t *testing.T) {
	doc := cargarContrato(t)
	ej := doc.Components.Examples["EventoPagado"].Value.Value
	// Serializado en una línea, sin espacios y en el orden del contrato (como
	// se envía): el orden de las claves cambia la firma.
	m := ej.(map[string]any)
	ped := m["pedido"].(map[string]any)
	cuerpo := []byte(`{"eventoId":"` + m["eventoId"].(string) + `","tipo":"` + m["tipo"].(string) +
		`","ocurridoEn":"` + m["ocurridoEn"].(string) + `","pedido":{"pedidoId":"` + ped["pedidoId"].(string) +
		`","referenciaExterna":"` + ped["referenciaExterna"].(string) + `","estado":"` + ped["estado"].(string) + `"}}`)
	const esperado = "t=1792243201,v1=2fbec75293b36b2159e73e8c8c1a36062411d20762993a8755dd47a211182ab4"
	if got := favmock.FirmarAviso("secreto-de-prueba", 1792243201, cuerpo); got != esperado {
		t.Errorf("FirmarAviso = %s; el vector del contrato es %s", got, esperado)
	}
}
