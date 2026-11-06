package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/sincronizacion"
)

type sincFalso struct {
	err      error
	llamadas int
}

func (s *sincFalso) Sincronizar(context.Context) (sincronizacion.Resumen, error) {
	s.llamadas++
	return sincronizacion.Resumen{Estado: sincronizacion.EstadoSinCambios}, s.err
}

func (s *sincFalso) Estado(context.Context) (sincronizacion.Estado, error) {
	return sincronizacion.Estado{Proveedor: "fav", Breaker: "cerrado"}, s.err
}

type saludFalsa struct{ err error }

func (s saludFalsa) Ping(context.Context) error { return s.err }

func pedir(t *testing.T, h http.Handler, metodo, ruta string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(metodo, ruta, nil))
	var cuerpo map[string]any
	json.Unmarshal(rec.Body.Bytes(), &cuerpo)
	return rec, cuerpo
}

var silencio = slog.New(slog.NewJSONHandler(io.Discard, nil))

func TestReadyzDependeSoloDeMongo(t *testing.T) {
	h := NuevoHandler(&sincFalso{}, saludFalsa{}, silencio)
	if rec, _ := pedir(t, h, "GET", "/readyz"); rec.Code != 200 {
		t.Errorf("readyz = %d", rec.Code)
	}
	h = NuevoHandler(&sincFalso{}, saludFalsa{err: errors.New("sin mongo")}, silencio)
	if rec, _ := pedir(t, h, "GET", "/readyz"); rec.Code != 503 {
		t.Errorf("readyz con mongo caído = %d", rec.Code)
	}
	if rec, _ := pedir(t, h, "GET", "/healthz"); rec.Code != 200 {
		t.Errorf("healthz = %d: el proceso sigue vivo aunque mongo no esté", rec.Code)
	}
}

func TestEstadoYSincronizarManual(t *testing.T) {
	s := &sincFalso{}
	h := NuevoHandler(s, saludFalsa{}, silencio)

	rec, cuerpo := pedir(t, h, "GET", "/estado")
	if rec.Code != 200 || cuerpo["proveedor"] != "fav" || cuerpo["circuit_breaker"] != "cerrado" {
		t.Errorf("estado = %d %v", rec.Code, cuerpo)
	}
	rec, cuerpo = pedir(t, h, "POST", "/sincronizar")
	if rec.Code != 200 || cuerpo["estado"] != sincronizacion.EstadoSinCambios || s.llamadas != 1 {
		t.Errorf("sincronizar = %d %v", rec.Code, cuerpo)
	}
	if rec, _ := pedir(t, h, "GET", "/sincronizar"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /sincronizar = %d, quiero 405", rec.Code)
	}

	s.err = errors.New("mongo caído")
	if rec, _ := pedir(t, h, "POST", "/sincronizar"); rec.Code != 503 {
		t.Errorf("sincronizar con la base caída = %d", rec.Code)
	}
	if rec, _ := pedir(t, h, "GET", "/estado"); rec.Code != 503 {
		t.Errorf("estado con la base caída = %d", rec.Code)
	}
}
