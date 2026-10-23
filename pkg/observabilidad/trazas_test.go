package observabilidad

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// limpiarEndpoints borra las dos variables que mira endpointOTLP, para que el
// entorno de quien corre los tests no decida el resultado.
func limpiarEndpoints(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
}

func TestIniciarTrazasSinEndpointEsNoOp(t *testing.T) {
	limpiarEndpoints(t)

	apagar, err := IniciarTrazas(context.Background(), "api-gateway")
	if err != nil {
		t.Fatalf("sin endpoint no debería fallar: %v", err)
	}
	if apagar == nil {
		t.Fatal("el apagado no puede ser nil")
	}
	if err := apagar(context.Background()); err != nil {
		t.Errorf("el apagado no-op no debería fallar: %v", err)
	}
}

// Aunque no haya colector, el propagador tiene que quedar puesto: es lo que
// hace que el traceparent entrante se siga respetando y reenviando.
func TestIniciarTrazasSinEndpointDejaElPropagadorW3C(t *testing.T) {
	limpiarEndpoints(t)

	if _, err := IniciarTrazas(context.Background(), "api-gateway"); err != nil {
		t.Fatalf("IniciarTrazas: %v", err)
	}

	entrada := http.Header{}
	entrada.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), propagation.HeaderCarrier(entrada))

	salida := http.Header{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(salida))
	if got := salida.Get("traceparent"); got != entrada.Get("traceparent") {
		t.Errorf("traceparent reinyectado = %q; se esperaba %q", got, entrada.Get("traceparent"))
	}
}

func TestIniciarTrazasExportaAlColector(t *testing.T) {
	var recibidas atomic.Int32
	colector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/traces" {
			t.Errorf("el exportador pegó a %q; se esperaba /v1/traces", r.URL.Path)
		}
		recibidas.Add(1)
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer colector.Close()

	limpiarEndpoints(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", colector.URL)

	apagar, err := IniciarTrazas(context.Background(), "api-gateway")
	if err != nil {
		t.Fatalf("IniciarTrazas: %v", err)
	}

	ctx, span := otel.Tracer("test").Start(context.Background(), "GET /api/v1/proveedores/recomendados")
	if !span.SpanContext().IsValid() {
		t.Fatal("con colector configurado el span tiene que ser válido")
	}
	span.End()

	// El contexto del span es el que después leen los logs: si no quedara el
	// span ahí, la línea de log saldría sin trace_id y no habría correlación.
	var buf strings.Builder
	nuevoLoggerEn(&buf, "api-gateway").InfoContext(ctx, "request")
	if !strings.Contains(buf.String(), span.SpanContext().TraceID().String()) {
		t.Errorf("el log no quedó correlacionado con el span: %s", buf.String())
	}

	apagarCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := apagar(apagarCtx); err != nil {
		t.Fatalf("apagado: %v", err)
	}
	if recibidas.Load() == 0 {
		t.Error("el colector no recibió ninguna exportación")
	}
}

// Jaeger caído: el exportador no puede hacer fallar el arranque ni bloquear.
func TestIniciarTrazasConColectorCaidoNoFalla(t *testing.T) {
	limpiarEndpoints(t)
	// Puerto cerrado: nadie escucha.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")

	listo := make(chan error, 1)
	go func() {
		_, err := IniciarTrazas(context.Background(), "api-gateway")
		listo <- err
	}()

	select {
	case err := <-listo:
		if err != nil {
			t.Fatalf("IniciarTrazas no debería fallar sin colector: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("IniciarTrazas se bloqueó esperando al colector")
	}
}
