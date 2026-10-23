package observabilidad

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// contextoConSpan arma un contexto con un span context válido sin necesidad del
// SDK: alcanza para probar lo que el handler de logs lee.
func contextoConSpan(t *testing.T) (context.Context, string, string) {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("trace id de prueba: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("span id de prueba: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), sc), traceID.String(), spanID.String()
}

func unaLinea(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var linea map[string]any
	if err := json.Unmarshal(buf.Bytes(), &linea); err != nil {
		t.Fatalf("la línea de log no es JSON (%v): %s", err, buf.String())
	}
	return linea
}

func TestLoggerCorrelacionaConElSpanDelContexto(t *testing.T) {
	ctx, traceID, spanID := contextoConSpan(t)

	var buf bytes.Buffer
	nuevoLoggerEn(&buf, "api-gateway").InfoContext(ctx, "request", "status", 200)

	linea := unaLinea(t, &buf)
	if linea["trace_id"] != traceID {
		t.Errorf("trace_id = %v; se esperaba %s", linea["trace_id"], traceID)
	}
	if linea["span_id"] != spanID {
		t.Errorf("span_id = %v; se esperaba %s", linea["span_id"], spanID)
	}
	if linea["service"] != "api-gateway" {
		t.Errorf("service = %v; se esperaba api-gateway", linea["service"])
	}
	if linea["msg"] != "request" || linea["status"] != float64(200) {
		t.Errorf("se perdieron atributos de la línea: %v", linea)
	}
}

func TestLoggerSinSpanNoInventaIds(t *testing.T) {
	var buf bytes.Buffer
	nuevoLoggerEn(&buf, "api-gateway").InfoContext(context.Background(), "arrancando")

	linea := unaLinea(t, &buf)
	if _, hay := linea["trace_id"]; hay {
		t.Errorf("no debería haber trace_id sin span: %v", linea)
	}
	if _, hay := linea["span_id"]; hay {
		t.Errorf("no debería haber span_id sin span: %v", linea)
	}
	if linea["service"] != "api-gateway" {
		t.Errorf("service = %v; se esperaba api-gateway", linea["service"])
	}
}

// El logger de una request se arma con With("request_id", ...): eso no puede
// romper la correlación, porque es justamente la línea que se busca en Jaeger.
func TestLoggerDerivadoConWithSigueCorrelacionando(t *testing.T) {
	ctx, traceID, _ := contextoConSpan(t)

	var buf bytes.Buffer
	log := nuevoLoggerEn(&buf, "api-gateway").With("request_id", "abc123")
	log.ErrorContext(ctx, "falla al llamar al upstream")

	linea := unaLinea(t, &buf)
	if linea["trace_id"] != traceID {
		t.Errorf("trace_id = %v; se esperaba %s", linea["trace_id"], traceID)
	}
	if linea["request_id"] != "abc123" {
		t.Errorf("request_id = %v; se esperaba abc123", linea["request_id"])
	}
}

// Sin contexto no hay de dónde leer los ids: la línea sale igual, sin ids. Se
// deja asentado para que quede claro por qué los servicios usan InfoContext.
func TestLoggerSinContextoEmiteLaLineaIgual(t *testing.T) {
	var buf bytes.Buffer
	nuevoLoggerEn(&buf, "api-gateway").Info("sin contexto")

	linea := unaLinea(t, &buf)
	if linea["msg"] != "sin contexto" {
		t.Errorf("msg = %v", linea["msg"])
	}
	if _, hay := linea["trace_id"]; hay {
		t.Errorf("no puede haber trace_id sin contexto: %v", linea)
	}
}
