package fav_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
)

const pedidoOK = `{"pedidoId":"7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f","referenciaExterna":"orden-1","estado":"PENDIENTE_DE_PAGO",
"items":[{"varianteId":"var-remera-basica-blanca-m","cantidad":3,"precioUnitario":15000,"subtotal":45000}],
"total":45000,"moneda":"ARS","pago":{"medio":"TRANSFERENCIA","venceEn":"2026-11-08T15:00:00Z","datosTransferencia":{"alias":"A","titular":"T"}},
"creadoEn":"2026-11-06T15:00:00Z","campoNuevo":"se ignora"}`

type recibida struct {
	clave, cuerpo, apiKey, correlacion string
}

// servidor responde en orden las respuestas dadas y registra cada request.
func servidor(t *testing.T, respuestas ...func(http.ResponseWriter)) (*httptest.Server, func() []recibida) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recibida
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		i := len(reqs)
		reqs = append(reqs, recibida{r.Header.Get("Idempotency-Key"), string(b), r.Header.Get("X-API-Key"), r.Header.Get("X-Correlation-Id")})
		mu.Unlock()
		respuestas[min(i, len(respuestas)-1)](w)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recibida {
		mu.Lock()
		defer mu.Unlock()
		return append([]recibida(nil), reqs...)
	}
}

func con(status int, cuerpo string, headers ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, cuerpo)
	}
}

var pedido = fav.PedidoNuevo{ReferenciaExterna: "orden-1",
	Items: []fav.ItemPedido{{VarianteID: "var-remera-basica-blanca-m", Cantidad: 3}}}

func cliente(base string, esperas *[]time.Duration) *fav.Cliente {
	return fav.NuevoCliente(base, "clave-grupo", nil).ConEsperas(200*time.Millisecond, func(d time.Duration) {
		*esperas = append(*esperas, d)
	})
}

func TestCrearPedidoReintentaConLaMismaClaveYElMismoCuerpo(t *testing.T) {
	srv, reqs := servidor(t,
		con(503, `{"codigo":"NO_DISPONIBLE"}`, "Retry-After", "7"),
		con(500, `{"codigo":"ERROR_INTERNO"}`),
		con(201, pedidoOK))
	var esperas []time.Duration
	p, err := cliente(srv.URL, &esperas).CrearPedido(context.Background(), "orden-1-1", pedido)
	if err != nil {
		t.Fatal(err)
	}
	if p.PedidoID != "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f" || p.Estado != "PENDIENTE_DE_PAGO" || p.Total != 45000 ||
		!p.Pago.VenceEn.Equal(time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("pedido = %+v", p)
	}
	r := reqs()
	if len(r) != 3 {
		t.Fatalf("hubo %d requests; quiero 3", len(r))
	}
	for _, x := range r {
		if x.clave != "orden-1-1" || x.cuerpo != r[0].cuerpo || x.apiKey != "clave-grupo" || x.correlacion != "orden-1-1" {
			t.Errorf("reintento distinto del primero: %+v", x)
		}
	}
	if len(esperas) != 2 || esperas[0] != 7*time.Second || esperas[1] != time.Second {
		t.Errorf("esperas = %v; quiero [7s (Retry-After) 1s]", esperas)
	}
}

func TestCrearPedidoSeRindeConFAVCaido(t *testing.T) {
	srv, reqs := servidor(t, con(503, `{"codigo":"NO_DISPONIBLE"}`))
	var esperas []time.Duration
	_, err := cliente(srv.URL, &esperas).CrearPedido(context.Background(), "orden-1-1", pedido)
	if !errors.Is(err, fav.ErrNoDisponible) {
		t.Fatalf("err = %v; quiero ErrNoDisponible", err)
	}
	if n := len(reqs()); n != 4 {
		t.Errorf("hubo %d intentos; quiero 4 (1 + 3 reintentos)", n)
	}
}

func TestCrearPedidoCortaUnIntentoColgado(t *testing.T) {
	var veces int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		veces++
		n := veces
		mu.Unlock()
		if n == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		w.WriteHeader(201)
		io.WriteString(w, pedidoOK)
	}))
	defer srv.Close()
	var esperas []time.Duration
	inicio := time.Now()
	if _, err := cliente(srv.URL, &esperas).CrearPedido(context.Background(), "orden-1-1", pedido); err != nil {
		t.Fatalf("después del timeout el reintento tenía que andar: %v", err)
	}
	if d := time.Since(inicio); d > time.Second {
		t.Errorf("tardó %s; el intento colgado se tenía que cortar a los 200 ms", d)
	}
}

func TestCrearPedidoErroresDefinitivosNoSeReintentan(t *testing.T) {
	casos := []struct {
		nombre string
		status int
		cuerpo string
		quiero error
	}{
		{"stock insuficiente", 409, `{"codigo":"STOCK_INSUFICIENTE","detail":"no alcanza","faltantes":[{"varianteId":"v","solicitada":3}]}`, fav.ErrStockInsuficiente},
		{"clave reutilizada", 409, `{"codigo":"CLAVE_IDEMPOTENCIA_REUTILIZADA"}`, fav.ErrPedidoRechazado},
		{"variante inexistente", 400, `{"codigo":"VARIANTE_INEXISTENTE","variantes":["v"]}`, fav.ErrPedidoRechazado},
		{"clave del grupo", 401, `{"codigo":"NO_AUTENTICADO"}`, fav.ErrNoAutenticado},
		{"201 fuera de contrato", 201, `{"pedido":"no"}`, fav.ErrRespuestaInvalida},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			srv, reqs := servidor(t, con(c.status, c.cuerpo))
			var esperas []time.Duration
			_, err := cliente(srv.URL, &esperas).CrearPedido(context.Background(), "orden-1-1", pedido)
			if !errors.Is(err, c.quiero) {
				t.Fatalf("err = %v; quiero %v", err, c.quiero)
			}
			if n := len(reqs()); n != 1 {
				t.Errorf("hubo %d intentos; un error definitivo se intenta 1 vez", n)
			}
			var e *fav.ErrorFAV
			if c.status != 201 && (!errors.As(err, &e) || e.Status != c.status) {
				t.Errorf("el error no lleva el status de FAV: %v", err)
			}
		})
	}
}

func TestConsultarPedido(t *testing.T) {
	srv, _ := servidor(t, con(200, pedidoOK))
	var esperas []time.Duration
	p, err := cliente(srv.URL, &esperas).ConsultarPedido(context.Background(), "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f")
	if err != nil || p.Estado != "PENDIENTE_DE_PAGO" {
		t.Fatalf("p = %+v, err = %v", p, err)
	}
	srv, _ = servidor(t, con(404, `{"codigo":"PEDIDO_INEXISTENTE"}`))
	if _, err := cliente(srv.URL, &esperas).ConsultarPedido(context.Background(), "x"); !errors.Is(err, fav.ErrPedidoInexistente) {
		t.Errorf("err = %v; quiero ErrPedidoInexistente", err)
	}
}
