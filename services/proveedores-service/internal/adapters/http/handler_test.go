package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
)

type repoFalso struct {
	ofertas map[string][]domain.Oferta
	err     error
	ultimo  string
}

func (r *repoFalso) OfertasPorProducto(_ context.Context, sku string) ([]domain.Oferta, error) {
	r.ultimo = sku
	if r.err != nil {
		return nil, r.err
	}
	o, ok := r.ofertas[sku]
	if !ok {
		return nil, domain.ErrProductoNoEncontrado
	}
	return o, nil
}

func (r *repoFalso) Ping(context.Context) error { return r.err }

const keyValida = "clave-test"

func nuevoServidor(t *testing.T, repo *repoFalso) (http.Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	h := NuevoHandler(Config{
		Repo:    repo,
		Salud:   repo,
		APIKeys: []string{"otra", keyValida},
		Logger:  slog.New(slog.NewJSONHandler(logs, nil)),
	})
	return h, logs
}

func repoDemo() *repoFalso {
	of := func(id int64, nombre string, precio float64, stock int) domain.Oferta {
		return domain.Oferta{ProveedorID: id, ProveedorNombre: nombre, ProductoSKU: "PAN-1",
			PrecioUnitario: precio, StockDisponible: stock}
	}
	return &repoFalso{ofertas: map[string][]domain.Oferta{
		"PAN-1":    {of(1, "Caro", 300, 100), of(2, "Barato", 100, 100), of(3, "Medio", 200, 100), of(4, "Sin stock", 50, 1)},
		"SIN-OFER": {},
	}}
}

func hacer(h http.Handler, target, key string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if key != "" {
		req.Header.Set(headerAPIKey, key)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func leerProblema(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != contentTypeProblem {
		t.Fatalf("Content-Type = %q, want %q", ct, contentTypeProblem)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body no es problem+json: %v (%s)", err, rec.Body.String())
	}
	if p.Type == "" || p.Title == "" || p.Detail == "" || p.Status != rec.Code {
		t.Fatalf("problem incompleto: %+v", p)
	}
	return p
}

const ruta = "/api/v1/proveedores/recomendados"

func TestRecomendadosOK(t *testing.T) {
	repo := repoDemo()
	h, _ := nuevoServidor(t, repo)

	rec := hacer(h, ruta+"?producto=PAN-1&cantidad=10&limite=2", keyValida)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != contentTypeJSON {
		t.Fatalf("Content-Type = %q", ct)
	}
	var recs []domain.Recomendacion
	if err := json.Unmarshal(rec.Body.Bytes(), &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].ProveedorID != 2 || recs[1].ProveedorID != 3 {
		t.Fatalf("ranking inesperado: %+v", recs)
	}
	if recs[0].CostoTotal != 1000 {
		t.Fatalf("costo_total = %v, want 1000", recs[0].CostoTotal)
	}
}

func TestRecomendadosDefaults(t *testing.T) {
	h, _ := nuevoServidor(t, repoDemo())
	rec := hacer(h, ruta+"?producto=PAN-1", keyValida)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var recs []domain.Recomendacion
	_ = json.Unmarshal(rec.Body.Bytes(), &recs)
	// cantidad=1 por defecto: la oferta con stock 1 entra y es la más barata.
	if len(recs) != 4 || recs[0].ProveedorID != 4 {
		t.Fatalf("ranking inesperado con defaults: %+v", recs)
	}
}

func TestRecomendadosProductoSinOfertasDevuelveListaVacia(t *testing.T) {
	h, _ := nuevoServidor(t, repoDemo())
	rec := hacer(h, ruta+"?producto=SIN-OFER&cantidad=10", keyValida)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("status = %d, body = %q; want 200 []", rec.Code, rec.Body.String())
	}
}

func TestRecomendadosSinStockDevuelveListaVacia(t *testing.T) {
	h, _ := nuevoServidor(t, repoDemo())
	rec := hacer(h, ruta+"?producto=PAN-1&cantidad=1000", keyValida)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("status = %d, body = %q; want 200 []", rec.Code, rec.Body.String())
	}
}

func TestRecomendadosParametrosInvalidos(t *testing.T) {
	casos := map[string]string{
		"sin producto":         "?cantidad=1",
		"producto vacío":       "?producto=%20",
		"cantidad cero":        "?producto=PAN-1&cantidad=0",
		"cantidad negativa":    "?producto=PAN-1&cantidad=-3",
		"cantidad no numérica": "?producto=PAN-1&cantidad=diez",
		"limite no numérico":   "?producto=PAN-1&limite=abc",
		"limite cero":          "?producto=PAN-1&limite=0",
		"limite sobre máximo":  "?producto=PAN-1&limite=51",
		"limite decimal":       "?producto=PAN-1&limite=2.5",
	}
	for nombre, qs := range casos {
		t.Run(nombre, func(t *testing.T) {
			repo := repoDemo()
			h, _ := nuevoServidor(t, repo)
			rec := hacer(h, ruta+qs, keyValida)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if p := leerProblema(t, rec); p.Code != CodigoParametroInvalido {
				t.Fatalf("code = %s", p.Code)
			}
			if repo.ultimo != "" {
				t.Fatal("no debería consultar el repo con parámetros inválidos")
			}
		})
	}
}

func TestRecomendadosLimiteMaximoAceptado(t *testing.T) {
	h, _ := nuevoServidor(t, repoDemo())
	if rec := hacer(h, ruta+"?producto=PAN-1&limite=50", keyValida); rec.Code != http.StatusOK {
		t.Fatalf("limite=50 debería aceptarse, status = %d", rec.Code)
	}
}

func TestRecomendadosNoAutorizado(t *testing.T) {
	for nombre, key := range map[string]string{"sin key": "", "key incorrecta": "mala"} {
		t.Run(nombre, func(t *testing.T) {
			repo := repoDemo()
			h, _ := nuevoServidor(t, repo)
			rec := hacer(h, ruta+"?producto=PAN-1", key)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if p := leerProblema(t, rec); p.Code != CodigoNoAutorizado {
				t.Fatalf("code = %s", p.Code)
			}
			if repo.ultimo != "" {
				t.Fatal("no debería consultar el repo sin autenticación")
			}
		})
	}
}

func TestRecomendadosProductoNoEncontrado(t *testing.T) {
	h, _ := nuevoServidor(t, repoDemo())
	rec := hacer(h, ruta+"?producto=NOPE", keyValida)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if p := leerProblema(t, rec); p.Code != CodigoProductoNoEncontrado {
		t.Fatalf("code = %s", p.Code)
	}
}

func TestRecomendadosBaseNoDisponible(t *testing.T) {
	repo := repoDemo()
	repo.err = context.DeadlineExceeded
	h, logs := nuevoServidor(t, repo)
	rec := hacer(h, ruta+"?producto=PAN-1", keyValida, headerRequestID, "req-503")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	p := leerProblema(t, rec)
	if p.Code != CodigoNoDisponible || p.RequestID != "req-503" {
		t.Fatalf("problem = %+v", p)
	}
	// El log de error tiene que llevar el request_id.
	encontrado := false
	for _, linea := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(linea), &m); err != nil {
			t.Fatalf("línea de log no es JSON: %q", linea)
		}
		if m["level"] == "ERROR" && m["msg"] == "falla al consultar ofertas" && m["request_id"] == "req-503" {
			encontrado = true
		}
	}
	if !encontrado {
		t.Fatalf("no se encontró el log de error con request_id:\n%s", logs.String())
	}
}

func TestRecomendadosClienteCanceloNoEsError(t *testing.T) {
	repo := repoDemo()
	repo.err = context.Canceled
	h, logs := nuevoServidor(t, repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, ruta+"?producto=PAN-1", nil).WithContext(ctx)
	req.Header.Set(headerAPIKey, keyValida)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusServiceUnavailable || rec.Body.Len() != 0 {
		t.Fatalf("no debería responder 503 a un cliente que canceló: %d %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Fatalf("no debería loguear ERROR por una cancelación del cliente:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), `"status":499`) {
		t.Fatalf("el log de request debería registrar 499:\n%s", logs.String())
	}
}

func TestRequestIDSePropagaYSeGenera(t *testing.T) {
	h, logs := nuevoServidor(t, repoDemo())

	rec := hacer(h, "/healthz", "", headerRequestID, "abc-123")
	if got := rec.Header().Get(headerRequestID); got != "abc-123" {
		t.Fatalf("X-Request-ID = %q, want abc-123", got)
	}

	rec = hacer(h, "/healthz", "")
	generado := rec.Header().Get(headerRequestID)
	if len(generado) != 32 {
		t.Fatalf("X-Request-ID generado = %q", generado)
	}

	rec = hacer(h, "/healthz", "", headerRequestID, "con espacios no vale")
	if got := rec.Header().Get(headerRequestID); got == "con espacios no vale" || got == "" {
		t.Fatalf("X-Request-ID inválido no debería propagarse: %q", got)
	}

	lineas := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lineas) != 3 {
		t.Fatalf("se esperaban 3 líneas de log, hay %d", len(lineas))
	}
	var primera, segunda map[string]any
	_ = json.Unmarshal([]byte(lineas[0]), &primera)
	_ = json.Unmarshal([]byte(lineas[1]), &segunda)
	if primera["request_id"] != "abc-123" || segunda["request_id"] != generado {
		t.Fatalf("request_id en logs no coincide: %v / %v", primera["request_id"], segunda["request_id"])
	}
	if primera["status"] != float64(200) || primera["path"] != "/healthz" {
		t.Fatalf("log de request incompleto: %v", primera)
	}
}

func TestReadyz(t *testing.T) {
	repo := repoDemo()
	h, _ := nuevoServidor(t, repo)
	if rec := hacer(h, "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", rec.Code)
	}
	repo.err = errors.New("conexión rechazada")
	rec := hacer(h, "/readyz", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz con base caída status = %d", rec.Code)
	}
	if p := leerProblema(t, rec); p.Code != CodigoNoDisponible {
		t.Fatalf("code = %s", p.Code)
	}
}

func TestHealthzNoRequiereKey(t *testing.T) {
	repo := repoDemo()
	repo.err = errors.New("base caída")
	h, _ := nuevoServidor(t, repo)
	if rec := hacer(h, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
}
