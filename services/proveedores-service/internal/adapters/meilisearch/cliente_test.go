package meilisearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

func TestEntreComillasNoDejaSalirDelLiteral(t *testing.T) {
	casos := map[string]string{
		`ropa`:               `"ropa"`,
		`x" OR categoria="y`: `"x\" OR categoria=\"y"`,
		`termina en barra\`:  `"termina en barra\\"`,
		`\" OR 1=1`:          `"\\\" OR 1=1"`,
		`Juarez Materiales`:  `"Juarez Materiales"`,
	}
	for entrada, quiero := range casos {
		if got := entreComillas(entrada); got != quiero {
			t.Errorf("entreComillas(%q) = %s, quiero %s", entrada, got, quiero)
		}
	}
}

func TestArmarFiltro(t *testing.T) {
	cinco, cien := 5.5, 100.0
	got := armarFiltro(ports.ConsultaProductos{
		Categoria: "paneles", Proveedor: "Andes", PrecioMin: &cinco, PrecioMax: &cien, ConStock: true,
	})
	quiero := []string{
		`categoria = "paneles"`, `proveedores = "Andes"`,
		"precio_min >= 5.5", "precio_min <= 100", "proveedores_con_stock > 0",
	}
	if strings.Join(got, "|") != strings.Join(quiero, "|") {
		t.Errorf("filtro = %v", got)
	}
	if len(armarFiltro(ports.ConsultaProductos{})) != 0 {
		t.Error("sin filtros tiene que quedar vacío")
	}
}

func TestIdDeDocumentoAceptaCualquierSKU(t *testing.T) {
	for _, sku := range []string{"PAN-550W-MONO", "FAV-var ñandú/talle.38", ""} {
		id := idDeDocumento(sku)
		if id != idDeDocumento(sku) {
			t.Errorf("no es determinístico para %q", sku)
		}
		for _, c := range id {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Errorf("id %q no es válido para el motor", id)
			}
		}
	}
	if idDeDocumento("a") == idDeDocumento("b") {
		t.Error("dos SKU con el mismo id")
	}
}

// motorFalso responde las rutas del motor y anota lo que le piden, en orden.
type motorFalso struct {
	mu       sync.Mutex
	pedidos  []string
	cuerpos  map[string]string
	tareaMal bool
	stats    string
	statsSt  int
	busqueda string
}

func (m *motorFalso) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	crudo, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	defer m.mu.Unlock()
	pedido := r.Method + " " + r.URL.Path
	if r.Method != http.MethodGet {
		m.pedidos = append(m.pedidos, pedido)
	}
	if m.cuerpos == nil {
		m.cuerpos = map[string]string{}
	}
	m.cuerpos[pedido] = string(crudo)

	if r.Header.Get("Authorization") != "Bearer secreto" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"invalid_api_key","message":"no"}`)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/tasks/"):
		if m.tareaMal {
			_, _ = io.WriteString(w, `{"status":"failed","error":{"code":"invalid_document_id","message":"mal"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"succeeded"}`)
	case strings.HasSuffix(r.URL.Path, "/stats"):
		if m.statsSt != 0 {
			w.WriteHeader(m.statsSt)
		}
		_, _ = io.WriteString(w, m.stats)
	case strings.HasSuffix(r.URL.Path, "/search"):
		_, _ = io.WriteString(w, m.busqueda)
	default:
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"taskUid":1}`)
	}
}

func armar(t *testing.T) (*Cliente, *motorFalso) {
	t.Helper()
	m := &motorFalso{}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	return NuevoCliente(srv.URL+"/", "secreto", ""), m
}

func TestIndiceCargado(t *testing.T) {
	c, m := armar(t)
	casos := []struct {
		nombre string
		status int
		cuerpo string
		quiero bool
	}{
		{"con documentos", 0, `{"numberOfDocuments":30}`, true},
		{"vacío: una reconstrucción cortada", 0, `{"numberOfDocuments":0}`, false},
		{"no existe", 404, `{"code":"index_not_found","message":"no"}`, false},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			m.statsSt, m.stats = caso.status, caso.cuerpo
			got, err := c.IndiceCargado(context.Background())
			if err != nil || got != caso.quiero {
				t.Errorf("cargado = %v, err = %v", got, err)
			}
		})
	}
	m.statsSt, m.stats = 500, `{"code":"internal","message":"roto"}`
	if _, err := c.IndiceCargado(context.Background()); err == nil {
		t.Error("un error del motor no es un índice vacío")
	}
}

func TestReconstruirUsaIndiceTemporalEIntercambia(t *testing.T) {
	c, m := armar(t)
	precio := 10.0
	docs := []ports.DocumentoProducto{{SKU: "A", PrecioMin: &precio}}
	if err := c.Reconstruir(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	quiero := []string{
		"DELETE /indexes/productos_tmp",
		"POST /indexes",
		"PATCH /indexes/productos_tmp/settings",
		"POST /indexes/productos_tmp/documents",
		"POST /indexes",
		"PATCH /indexes/productos/settings",
		"POST /swap-indexes",
		"DELETE /indexes/productos_tmp",
	}
	if strings.Join(m.pedidos, "\n") != strings.Join(quiero, "\n") {
		t.Errorf("pedidos:\n%s", strings.Join(m.pedidos, "\n"))
	}
	if !strings.Contains(m.cuerpos["POST /swap-indexes"], `["productos","productos_tmp"]`) {
		t.Errorf("intercambio = %s", m.cuerpos["POST /swap-indexes"])
	}
}

func TestUnaTareaFallidaEsUnError(t *testing.T) {
	c, m := armar(t)
	m.tareaMal = true
	err := c.Indexar(context.Background(), []ports.DocumentoProducto{{SKU: "A"}})
	if err == nil || !strings.Contains(err.Error(), "invalid_document_id") {
		t.Errorf("err = %v: si el motor rechaza el lote, el SKU tiene que quedar pendiente", err)
	}
}

func TestIndexarArmaElDocumentoConIdYListaDeProveedores(t *testing.T) {
	c, m := armar(t)
	if err := c.Indexar(context.Background(), []ports.DocumentoProducto{{SKU: "A"}}); err != nil {
		t.Fatal(err)
	}
	var cuerpo []map[string]any
	if err := json.Unmarshal([]byte(m.cuerpos["POST /indexes/productos/documents"]), &cuerpo); err != nil {
		t.Fatal(err)
	}
	d := cuerpo[0]
	if d["id"] != idDeDocumento("A") || d["sku"] != "A" {
		t.Errorf("documento = %v", d)
	}
	if _, ok := d["proveedores"].([]any); !ok {
		t.Errorf("proveedores tiene que ser una lista: %v", d["proveedores"])
	}
	if v, ok := d["precio_min"]; !ok || v != nil {
		t.Errorf("sin stock, precio_min = %v (%v), quiero null", v, ok)
	}
}

func TestBuscarArmaLaConsultaYLeeLaRespuesta(t *testing.T) {
	c, m := armar(t)
	m.busqueda = `{"hits":[{"id":"x","sku":"A","nombre":"Panel","precio_min":10.5,"proveedores":["Andes"]}],"totalHits":21,"page":2,"hitsPerPage":5}`
	res, err := c.Buscar(context.Background(), ports.ConsultaProductos{
		Q: "panel", Categoria: "paneles", Orden: ports.OrdenPrecioDesc, Pagina: 2, Tamanio: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 21 || res.Pagina != 2 || res.Tamanio != 5 || res.Resultados[0].SKU != "A" || *res.Resultados[0].PrecioMin != 10.5 {
		t.Errorf("resultado = %+v", res)
	}
	var pedido map[string]any
	if err := json.Unmarshal([]byte(m.cuerpos["POST /indexes/productos/search"]), &pedido); err != nil {
		t.Fatal(err)
	}
	if pedido["q"] != "panel" || pedido["page"] != 2.0 || pedido["hitsPerPage"] != 5.0 {
		t.Errorf("pedido = %v", pedido)
	}
	if orden := pedido["sort"].([]any); orden[0] != "precio_min:desc" {
		t.Errorf("orden = %v", orden)
	}
}

func TestBuscarSinOrdenNoMandaSortYSinResultadosDevuelveLista(t *testing.T) {
	c, m := armar(t)
	m.busqueda = `{"hits":[],"totalHits":0}`
	res, err := c.Buscar(context.Background(), ports.ConsultaProductos{Pagina: 1, Tamanio: 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.Resultados == nil {
		t.Error("resultados tiene que ser una lista vacía, no null")
	}
	if strings.Contains(m.cuerpos["POST /indexes/productos/search"], "sort") {
		t.Errorf("pedido = %s", m.cuerpos["POST /indexes/productos/search"])
	}
}

func TestMotorCaidoEsUnError(t *testing.T) {
	c := NuevoCliente("http://127.0.0.1:1", "x", "")
	if _, err := c.Buscar(context.Background(), ports.ConsultaProductos{Pagina: 1, Tamanio: 1}); err == nil {
		t.Error("tenía que fallar")
	}
	if _, err := c.IndiceCargado(context.Background()); err == nil {
		t.Error("tenía que fallar")
	}
}
