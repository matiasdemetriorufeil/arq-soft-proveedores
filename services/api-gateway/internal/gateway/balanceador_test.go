package gateway

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func loggerSilencioso() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// balanceadorDePrueba arma un balanceador con un intervalo largo: los tests
// sondean a mano, así nada corre en segundo plano mientras se afirma.
func balanceadorDePrueba(t *testing.T, upstreams ...string) *Balanceador {
	t.Helper()
	b, err := NuevoBalanceador(ConfigBalanceador{
		Upstreams: upstreams,
		Intervalo: time.Hour,
		Timeout:   500 * time.Millisecond,
		Logger:    loggerSilencioso(),
	})
	if err != nil {
		t.Fatalf("NuevoBalanceador: %v", err)
	}
	return b
}

func TestNuevoBalanceadorRechazaConfiguracionInvalida(t *testing.T) {
	casos := map[string]ConfigBalanceador{
		"sin upstreams":  {Upstreams: nil, Intervalo: time.Second, Timeout: time.Second, Logger: loggerSilencioso()},
		"sin esquema":    {Upstreams: []string{"proveedores:8080"}, Intervalo: time.Second, Timeout: time.Second, Logger: loggerSilencioso()},
		"esquema raro":   {Upstreams: []string{"tcp://proveedores:8080"}, Intervalo: time.Second, Timeout: time.Second, Logger: loggerSilencioso()},
		"sin host":       {Upstreams: []string{"http://"}, Intervalo: time.Second, Timeout: time.Second, Logger: loggerSilencioso()},
		"intervalo cero": {Upstreams: []string{"http://a:1"}, Intervalo: 0, Timeout: time.Second, Logger: loggerSilencioso()},
		"timeout cero":   {Upstreams: []string{"http://a:1"}, Intervalo: time.Second, Timeout: 0, Logger: loggerSilencioso()},
		"sin logger":     {Upstreams: []string{"http://a:1"}, Intervalo: time.Second, Timeout: time.Second},
	}
	for nombre, cfg := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := NuevoBalanceador(cfg); err == nil {
				t.Error("se esperaba un error de configuración")
			}
		})
	}
}

// Las réplicas arrancan caídas: /readyz no puede decir que el gateway está
// listo antes de que alguna haya contestado de verdad.
func TestBalanceadorArrancaSinReplicasSanas(t *testing.T) {
	b := balanceadorDePrueba(t, "http://a:8080", "http://b:8080")
	if b.AlgunaSana() {
		t.Error("ninguna réplica debería estar sana antes del primer sondeo")
	}
	if _, ok := b.Siguiente(); ok {
		t.Error("Siguiente no debería elegir nada antes del primer sondeo")
	}
}

func TestSiguienteRepartePorIgual(t *testing.T) {
	b := balanceadorDePrueba(t, "http://a:8080", "http://b:8080")
	for _, inst := range b.Instancias() {
		inst.sana.Store(true)
	}

	reparto := map[string]int{}
	for range 4 {
		inst, ok := b.Siguiente()
		if !ok {
			t.Fatal("con las dos réplicas sanas tiene que elegir alguna")
		}
		reparto[inst.Destino()]++
	}
	if reparto["a:8080"] != 2 || reparto["b:8080"] != 2 {
		t.Errorf("reparto = %v; se esperaba 2 y 2", reparto)
	}
}

func TestSiguienteSalteaLasCaidas(t *testing.T) {
	b := balanceadorDePrueba(t, "http://a:8080", "http://b:8080")
	b.Instancias()[1].sana.Store(true) // solo la segunda responde

	for range 4 {
		inst, ok := b.Siguiente()
		if !ok {
			t.Fatal("queda una réplica sana: tiene que elegirla")
		}
		if inst.Destino() != "b:8080" {
			t.Fatalf("eligió %s; la única sana es b:8080", inst.Destino())
		}
	}
}

func TestMarcarCaidaSacaLaReplicaDelReparto(t *testing.T) {
	b := balanceadorDePrueba(t, "http://a:8080", "http://b:8080")
	for _, inst := range b.Instancias() {
		inst.sana.Store(true)
	}

	b.MarcarCaida(context.Background(), b.Instancias()[0])
	if b.Instancias()[0].Sana() {
		t.Error("la réplica marcada tendría que quedar caída")
	}
	for range 3 {
		inst, _ := b.Siguiente()
		if inst.Destino() != "b:8080" {
			t.Fatalf("eligió %s después de marcar a a:8080 como caída", inst.Destino())
		}
	}
	if !b.AlgunaSana() {
		t.Error("todavía queda una réplica sana")
	}

	b.MarcarCaida(context.Background(), b.Instancias()[1])
	if b.AlgunaSana() {
		t.Error("con las dos caídas no puede quedar ninguna sana")
	}
	if _, ok := b.Siguiente(); ok {
		t.Error("con las dos caídas no tiene que elegir ninguna")
	}
}

func TestOtraDevuelveUnaReplicaDistinta(t *testing.T) {
	b := balanceadorDePrueba(t, "http://a:8080", "http://b:8080")
	for _, inst := range b.Instancias() {
		inst.sana.Store(true)
	}

	fallida, _ := b.Siguiente()
	otra, ok := b.Otra(fallida)
	if !ok {
		t.Fatal("la otra réplica está sana: tendría que servir para el reintento")
	}
	if otra == fallida {
		t.Errorf("Otra devolvió la misma réplica (%s)", otra.Destino())
	}

	// Sin otra réplica sana no hay dónde reintentar.
	otra.sana.Store(false)
	if _, ok := b.Otra(fallida); ok {
		t.Error("no queda ninguna otra réplica sana")
	}
}

func TestSondearSigueElEstadoRealDeLasReplicas(t *testing.T) {
	var estado atomic.Int32
	estado.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != rutaSalud {
			t.Errorf("el sondeo pegó a %q; se esperaba %s", r.URL.Path, rutaSalud)
		}
		w.WriteHeader(int(estado.Load()))
	}))
	defer srv.Close()

	b := balanceadorDePrueba(t, srv.URL)
	inst := b.Instancias()[0]

	b.Sondear(context.Background())
	if !inst.Sana() {
		t.Fatal("la réplica responde 200 en /healthz: tendría que quedar sana")
	}

	estado.Store(http.StatusServiceUnavailable)
	b.Sondear(context.Background())
	if inst.Sana() {
		t.Error("/healthz devolvió 503: la réplica tendría que quedar caída")
	}

	estado.Store(http.StatusOK)
	b.Sondear(context.Background())
	if !inst.Sana() {
		t.Error("la réplica volvió a responder 200: el sondeo tendría que recuperarla")
	}
}

// Una réplica apagada no responde la conexión: el sondeo la tiene que dar por
// caída sin colgarse.
func TestSondearDaPorCaidaALaReplicaApagada(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	b := balanceadorDePrueba(t, srv.URL)
	inst := b.Instancias()[0]

	b.Sondear(context.Background())
	if !inst.Sana() {
		t.Fatal("la réplica está arriba")
	}

	srv.Close()
	b.Sondear(context.Background())
	if inst.Sana() {
		t.Error("la réplica está apagada: el sondeo tendría que marcarla caída")
	}
}

func TestIniciarSondeoRespondeAntesDeDevolverYParaConElContexto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	b, err := NuevoBalanceador(ConfigBalanceador{
		Upstreams: []string{srv.URL},
		Intervalo: 10 * time.Millisecond,
		Timeout:   500 * time.Millisecond,
		Logger:    loggerSilencioso(),
	})
	if err != nil {
		t.Fatalf("NuevoBalanceador: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.IniciarSondeo(ctx)
	// La primera vuelta es sincrónica: al volver de IniciarSondeo el estado ya
	// está resuelto, sin esperas ni sleeps en el test.
	if !b.AlgunaSana() {
		t.Error("IniciarSondeo tendría que haber sondeado antes de devolver")
	}
	cancel()
}
