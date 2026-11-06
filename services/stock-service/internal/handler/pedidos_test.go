package handler_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/handler"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

const secretoAvisos = "secreto-de-prueba"

type ordenesFalsas struct{}

func (ordenesFalsas) Ordenes(context.Context, string) ([]repository.Orden, error) { return nil, nil }
func (ordenesFalsas) TransicionarOrden(context.Context, string, string) (repository.Orden, error) {
	return repository.Orden{}, nil
}

type pedidosFalsos struct {
	orden     repository.Orden
	errEnviar error
	repetido  bool
	errAviso  error
	avisos    []fav.Aviso
}

func (p *pedidosFalsos) EnviarOrden(context.Context, string) (repository.Orden, error) {
	return p.orden, p.errEnviar
}

func (p *pedidosFalsos) RecibirAviso(_ context.Context, a fav.Aviso, _ func(error)) (bool, error) {
	if p.errAviso != nil {
		return false, p.errAviso
	}
	p.avisos = append(p.avisos, a)
	return !p.repetido, nil
}

func servidorConPedidos(p *pedidosFalsos, secreto string) http.Handler {
	return handler.NuevoHandler(handler.Config{
		Servicio: &servicioFalso{}, Ordenes: ordenesFalsas{}, Pedidos: p, SecretoAvisos: secreto,
		Salud: saludFalsa{}, APIKeys: []string{claveAPI},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

const avisoPagado = `{"eventoId":"3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b","tipo":"pedido.actualizado","ocurridoEn":"2026-11-06T13:20:00Z","pedido":{"pedidoId":"7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f","referenciaExterna":"o1","estado":"PAGADO"}}`

func firmaDe(secreto string, t time.Time, cuerpo string) string {
	mac := hmac.New(sha256.New, []byte(secreto))
	fmt.Fprintf(mac, "%d.%s", t.Unix(), cuerpo)
	return fmt.Sprintf("t=%d,v1=%s", t.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func postAviso(srv http.Handler, cuerpo, firma, eventoID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/fav", strings.NewReader(cuerpo))
	req.Header.Set("Content-Type", "application/json")
	if firma != "" {
		req.Header.Set("X-FAV-Firma", firma)
	}
	if eventoID != "" {
		req.Header.Set("X-FAV-Evento-Id", eventoID)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestWebhookAvisoValido(t *testing.T) {
	p := &pedidosFalsos{}
	srv := servidorConPedidos(p, secretoAvisos)
	// Sin API key: lo llama FAV y se autentica con la firma.
	rec := postAviso(srv, avisoPagado, firmaDe(secretoAvisos, time.Now(), avisoPagado), "3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(p.avisos) != 1 || p.avisos[0].Pedido.Estado != "PAGADO" {
		t.Errorf("avisos que llegaron al servicio = %+v", p.avisos)
	}
	var r struct{ Recibido, Repetido bool }
	json.Unmarshal(rec.Body.Bytes(), &r)
	if !r.Recibido || r.Repetido {
		t.Errorf("respuesta = %s", rec.Body.String())
	}
}

func TestWebhookAvisoRepetidoResponde200(t *testing.T) {
	p := &pedidosFalsos{repetido: true}
	rec := postAviso(servidorConPedidos(p, secretoAvisos), avisoPagado, firmaDe(secretoAvisos, time.Now(), avisoPagado), "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"repetido":true`) {
		t.Errorf("status = %d %s; un aviso repetido se contesta 200 sin repetir el efecto", rec.Code, rec.Body.String())
	}
}

func TestWebhookRechazos(t *testing.T) {
	ahora := time.Now()
	casos := []struct {
		nombre, cuerpo, firma, eventoID string
		status                          int
		code                            string
	}{
		{"sin firma", avisoPagado, "", "", 401, handler.CodigoFirmaInvalida},
		{"firmado con otro secreto", avisoPagado, firmaDe("otro", ahora, avisoPagado), "", 401, handler.CodigoFirmaInvalida},
		{"firma de hace 10 minutos", avisoPagado, firmaDe(secretoAvisos, ahora.Add(-10*time.Minute), avisoPagado), "", 401, handler.CodigoFirmaInvalida},
		{"cuerpo alterado", strings.Replace(avisoPagado, "PAGADO", "VENCIDO", 1), firmaDe(secretoAvisos, ahora, avisoPagado), "", 401, handler.CodigoFirmaInvalida},
		{"firmado pero sin eventoId", `{"pedido":{"pedidoId":"p","estado":"PAGADO"}}`, firmaDe(secretoAvisos, ahora, `{"pedido":{"pedidoId":"p","estado":"PAGADO"}}`), "", 400, handler.CodigoAvisoInvalido},
		{"X-FAV-Evento-Id distinto", avisoPagado, firmaDe(secretoAvisos, ahora, avisoPagado), "otro-id", 400, handler.CodigoAvisoInvalido},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			p := &pedidosFalsos{}
			exigirProblema(t, postAviso(servidorConPedidos(p, secretoAvisos), c.cuerpo, c.firma, c.eventoID), c.status, c.code)
			if len(p.avisos) != 0 {
				t.Error("un aviso rechazado no tiene que llegar al servicio")
			}
		})
	}
}

func TestWebhookConLaBaseCaidaResponde503(t *testing.T) {
	p := &pedidosFalsos{errAviso: errors.New("la base no responde")}
	rec := postAviso(servidorConPedidos(p, secretoAvisos), avisoPagado, firmaDe(secretoAvisos, time.Now(), avisoPagado), "")
	// Con 5xx FAV reintenta el aviso.
	exigirProblema(t, rec, http.StatusServiceUnavailable, handler.CodigoNoDisponible)
}

func TestWebhookSinSecretoNoSeRegistra(t *testing.T) {
	rec := postAviso(servidorConPedidos(&pedidosFalsos{}, ""), avisoPagado, firmaDe("", time.Now(), avisoPagado), "")
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; sin secreto el receptor no tiene que existir", rec.Code)
	}
}

func postPedido(srv http.Handler, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stock/ordenes/o1/pedido", nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestEnviarPedido(t *testing.T) {
	pid := "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f"
	ok := &pedidosFalsos{orden: repository.Orden{ID: "o1", Estado: service.EstadoSolicitada, PedidoID: &pid}}
	rec := postPedido(servidorConPedidos(ok, secretoAvisos), claveAPI)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"pedido_id":"`+pid+`"`) {
		t.Errorf("status = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "clave_pedido") {
		t.Error("la Idempotency-Key del pedido no se publica en la API")
	}

	exigirProblema(t, postPedido(servidorConPedidos(ok, secretoAvisos), ""), http.StatusUnauthorized, handler.CodigoNoAutorizado)

	casos := []struct {
		nombre string
		err    error
		status int
		code   string
	}{
		{"FAV caído", &service.EnvioFallidoError{Motivo: "FAV no respondió", Reintentable: true}, 503, handler.CodigoProveedorNoDisponible},
		{"FAV sin stock", &service.EnvioFallidoError{Motivo: "sin stock"}, 409, handler.CodigoPedidoRechazado},
		{"no es de FAV", fmt.Errorf("%w: x", service.ErrOrdenNoEsDeFAV), 409, handler.CodigoPedidoRechazado},
		{"ya solicitada", &service.TransicionInvalidaError{ID: "o1", Desde: "SOLICITADA", Hacia: "SOLICITADA"}, 409, handler.CodigoTransicionInvalida},
		{"no existe", service.ErrOrdenNoEncontrada, 404, handler.CodigoOrdenNoEncontrada},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			exigirProblema(t, postPedido(servidorConPedidos(&pedidosFalsos{errEnviar: c.err}, secretoAvisos), claveAPI), c.status, c.code)
		})
	}
}
