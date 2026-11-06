package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/handler"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

const claveAPI = "clave-de-prueba"

// servicioFalso devuelve lo que el test le pida: lo que se prueba acá es la
// traducción de errores de negocio a problem+json, no la regla.
type servicioFalso struct {
	reserva  repository.Reserva
	repetida bool
	stock    repository.Stock
	err      error
}

func (f *servicioFalso) Reservar(_ context.Context, _ string, _ int, _ string) (repository.Reserva, bool, error) {
	return f.reserva, f.repetida, f.err
}

func (f *servicioFalso) Stock(_ context.Context, _ string) (repository.Stock, error) {
	return f.stock, f.err
}

type saludFalsa struct{ err error }

func (s saludFalsa) Ping(context.Context) error { return s.err }

func nuevoServidor(svc handler.ServicioStock, salud handler.Salud) http.Handler {
	return handler.NuevoHandler(handler.Config{
		Servicio: svc,
		Salud:    salud,
		APIKeys:  []string{claveAPI},
		// io.Discard: los tests no tienen que ensuciar la salida.
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func postReserva(t *testing.T, srv http.Handler, cuerpo string, clave string, apiKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stock/reservas", strings.NewReader(cuerpo))
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	if clave != "" {
		req.Header.Set("Idempotency-Key", clave)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// exigirProblema comprueba el status, el content-type y el `code` del
// problem+json, que es el contrato que ve el cliente.
func exigirProblema(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d; se esperaba %d (cuerpo: %s)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q; se esperaba application/problem+json", ct)
	}
	var p handler.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("el cuerpo no es un problem+json: %v", err)
	}
	if p.Code != code {
		t.Errorf("code = %q; se esperaba %q", p.Code, code)
	}
	if p.Status != status {
		t.Errorf("el campo status del problema = %d; se esperaba %d", p.Status, status)
	}
	if p.Title == "" || p.Detail == "" {
		t.Errorf("el problema tiene que traer title y detail; trae %+v", p)
	}
}

func TestReservarDevuelve201(t *testing.T) {
	svc := &servicioFalso{reserva: repository.Reserva{
		ID: "11111111-1111-4111-8111-111111111111", SKU: "PAN-450W-MONO",
		Cantidad: 6, Estado: "CONFIRMADA", CreadoEn: time.Now(),
	}}
	srv := nuevoServidor(svc, saludFalsa{})

	rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", claveAPI)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; se esperaba 201 (cuerpo: %s)", rec.Code, rec.Body.String())
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatalf("respuesta no JSON: %v", err)
	}
	for _, campo := range []string{"id", "sku", "cantidad", "estado", "creado_en"} {
		if _, ok := cuerpo[campo]; !ok {
			t.Errorf("falta el campo %q en la respuesta: %v", campo, cuerpo)
		}
	}
	// La clave de idempotencia es del cliente y no vuelve en la respuesta.
	if _, ok := cuerpo["clave_idempotencia"]; ok {
		t.Error("la respuesta no tiene que devolver la clave de idempotencia")
	}
}

// El reintento de una clave ya aplicada responde 200: no se creó nada nuevo.
func TestReservarRepetidaDevuelve200(t *testing.T) {
	svc := &servicioFalso{
		reserva:  repository.Reserva{ID: "r-1", SKU: "PAN-450W-MONO", Cantidad: 6, Estado: "CONFIRMADA"},
		repetida: true,
	}
	srv := nuevoServidor(svc, saludFalsa{})

	rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", claveAPI)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; se esperaba 200 (cuerpo: %s)", rec.Code, rec.Body.String())
	}
}

func TestReservarTraduceLosErroresDelServicio(t *testing.T) {
	casos := []struct {
		nombre string
		err    error
		status int
		code   string
	}{
		{"stock insuficiente", &service.StockInsuficienteError{
			SKU: "PAN-450W-MONO", Disponible: 4, Solicitado: 6,
		}, http.StatusConflict, handler.CodigoStockInsuficiente},
		{"sku inexistente", service.ErrProductoNoEncontrado,
			http.StatusNotFound, handler.CodigoProductoNoEncontrado},
		{"parámetro inválido", service.ErrParametroInvalido,
			http.StatusBadRequest, handler.CodigoParametroInvalido},
		{"base caída", errors.New("conexión rechazada"),
			http.StatusServiceUnavailable, handler.CodigoNoDisponible},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			srv := nuevoServidor(&servicioFalso{err: c.err}, saludFalsa{})
			rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", claveAPI)
			exigirProblema(t, rec, c.status, c.code)
		})
	}
}

// El 409 tiene que decir cuántas unidades había: es lo que el cliente necesita
// para decidir si pide menos.
func TestStockInsuficienteDetallaElDisponible(t *testing.T) {
	srv := nuevoServidor(&servicioFalso{err: &service.StockInsuficienteError{
		SKU: "PAN-450W-MONO", Disponible: 4, Solicitado: 6,
	}}, saludFalsa{})

	rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", claveAPI)
	exigirProblema(t, rec, http.StatusConflict, handler.CodigoStockInsuficiente)
	if !strings.Contains(rec.Body.String(), "4 disponibles") {
		t.Errorf("el detail tiene que decir el disponible; es %s", rec.Body.String())
	}
}

func TestReservarRechazaEntradasInvalidas(t *testing.T) {
	casos := []struct {
		nombre string
		cuerpo string
		clave  string
	}{
		{"json roto", `{"sku":`, "clave-1"},
		{"cuerpo vacío", ``, "clave-1"},
		{"sin sku", `{"cantidad":6}`, "clave-1"},
		{"sin cantidad", `{"sku":"PAN-450W-MONO"}`, "clave-1"},
		{"cantidad cero", `{"sku":"PAN-450W-MONO","cantidad":0}`, "clave-1"},
		{"cantidad negativa", `{"sku":"PAN-450W-MONO","cantidad":-2}`, "clave-1"},
		{"cantidad no entera", `{"sku":"PAN-450W-MONO","cantidad":1.5}`, "clave-1"},
		{"cantidad como texto", `{"sku":"PAN-450W-MONO","cantidad":"6"}`, "clave-1"},
		{"sin clave de idempotencia", `{"sku":"PAN-450W-MONO","cantidad":6}`, ""},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			// El servicio no tiene que llegar a ejecutarse: si se ejecutara,
			// este doble devolvería 201 y el test fallaría.
			srv := nuevoServidor(&servicioFalso{}, saludFalsa{})
			rec := postReserva(t, srv, c.cuerpo, c.clave, claveAPI)
			exigirProblema(t, rec, http.StatusBadRequest, handler.CodigoParametroInvalido)
		})
	}
}

func TestSinAPIKeyDevuelve401(t *testing.T) {
	srv := nuevoServidor(&servicioFalso{}, saludFalsa{})

	t.Run("reservas sin header", func(t *testing.T) {
		rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", "")
		exigirProblema(t, rec, http.StatusUnauthorized, handler.CodigoNoAutorizado)
	})
	t.Run("reservas con clave equivocada", func(t *testing.T) {
		rec := postReserva(t, srv, `{"sku":"PAN-450W-MONO","cantidad":6}`, "clave-1", "otra-clave")
		exigirProblema(t, rec, http.StatusUnauthorized, handler.CodigoNoAutorizado)
	})
	t.Run("consulta de stock sin header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stock/PAN-450W-MONO", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		exigirProblema(t, rec, http.StatusUnauthorized, handler.CodigoNoAutorizado)
	})
}

func TestConsultaDeStock(t *testing.T) {
	svc := &servicioFalso{stock: repository.Stock{
		SKU: "PAN-450W-MONO", Disponible: 4, Reservado: 6, Minimo: 30,
	}}
	srv := nuevoServidor(svc, saludFalsa{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stock/PAN-450W-MONO", nil)
	req.Header.Set("X-API-Key", claveAPI)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; se esperaba 200 (cuerpo: %s)", rec.Code, rec.Body.String())
	}
	var st repository.Stock
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("respuesta no JSON: %v", err)
	}
	if st.Disponible != 4 || st.Reservado != 6 || st.Minimo != 30 {
		t.Errorf("stock = %+v; se esperaba disponible 4 / reservado 6 / minimo 30", st)
	}
}

func TestConsultaDeStockInexistente(t *testing.T) {
	srv := nuevoServidor(&servicioFalso{err: service.ErrProductoNoEncontrado}, saludFalsa{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stock/NO-EXISTE", nil)
	req.Header.Set("X-API-Key", claveAPI)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	exigirProblema(t, rec, http.StatusNotFound, handler.CodigoProductoNoEncontrado)
}

// /healthz responde 200 siempre (está vivo) y /readyz mira la base.
func TestSalud(t *testing.T) {
	srv := nuevoServidor(&servicioFalso{}, saludFalsa{})
	for _, ruta := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ruta, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s devolvió %d; se esperaba 200", ruta, rec.Code)
		}
	}

	caido := nuevoServidor(&servicioFalso{}, saludFalsa{err: errors.New("sin conexión")})

	rec := httptest.NewRecorder()
	caido.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz con la base caída devolvió %d; tiene que seguir en 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	caido.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	exigirProblema(t, rec, http.StatusServiceUnavailable, handler.CodigoNoDisponible)
}
