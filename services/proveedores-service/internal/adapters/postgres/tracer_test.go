package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
)

// El SQL y el parámetro que usan los tests: el valor del parámetro no tiene que
// aparecer en ningún atributo del span.
const (
	sqlDePrueba  = "SELECT o.proveedor_id\nFROM productos p\nLEFT JOIN ofertas o ON o.producto_sku = p.sku\nWHERE p.sku = $1"
	skuDePrueba  = "PAN-450W-MONO"
	nombrePadre  = "GET /api/v1/proveedores/recomendados"
	nombreTabla  = "productos"
	nombreEspera = "SELECT productos"
)

// grabador arma un proveedor de trazas que guarda los spans en memoria, sin
// tocar el proveedor global: el tracer de consultas lo recibe por parámetro.
func grabador(t *testing.T) (*tracetest.SpanRecorder, trace.TracerProvider, *postgres.Tracer) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Errorf("apagar el proveedor de trazas: %v", err)
		}
	})
	return sr, tp, postgres.NuevoTracer(tp)
}

// conSpanPadre simula lo que hace otelhttp con la request que llega del gateway:
// deja un span de servidor en el contexto, del que tiene que colgar el de la
// consulta.
func conSpanPadre(t *testing.T, tp trace.TracerProvider) (context.Context, trace.Span) {
	t.Helper()
	ctx, span := tp.Tracer("test").Start(context.Background(), nombrePadre,
		trace.WithSpanKind(trace.SpanKindServer))
	return ctx, span
}

func atributo(t *testing.T, span sdktrace.ReadOnlySpan, clave string) (attribute.Value, bool) {
	t.Helper()
	for _, kv := range span.Attributes() {
		if string(kv.Key) == clave {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

func TestTracer_AbreYCierraElSpanDeLaConsulta(t *testing.T) {
	sr, tp, qt := grabador(t)
	padreCtx, padre := conSpanPadre(t, tp)
	defer padre.End()

	ctx := qt.TraceQueryStart(padreCtx, nil, pgx.TraceQueryStartData{
		SQL:  sqlDePrueba,
		Args: []any{skuDePrueba},
	})
	if len(sr.Ended()) != 0 {
		t.Fatalf("el span se cerró antes de TraceQueryEnd: %d spans terminados", len(sr.Ended()))
	}
	qt.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 3")})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans terminados = %d, se esperaba 1", len(spans))
	}
	span := spans[0]

	if span.Name() != nombreEspera {
		t.Errorf("nombre del span = %q, se esperaba %q", span.Name(), nombreEspera)
	}
	if span.SpanKind() != trace.SpanKindClient {
		t.Errorf("kind = %v, se esperaba client", span.SpanKind())
	}
	if span.Parent().SpanID() != padre.SpanContext().SpanID() {
		t.Error("el span de la consulta no cuelga del span de la request")
	}
	if span.SpanContext().TraceID() != padre.SpanContext().TraceID() {
		t.Error("el span de la consulta quedó en otra traza que la request")
	}
	if span.Status().Code != codes.Unset {
		t.Errorf("status = %v, se esperaba sin error", span.Status().Code)
	}

	if v, ok := atributo(t, span, "db.system"); !ok || v.AsString() != "postgresql" {
		t.Errorf("db.system = %q (presente: %v), se esperaba postgresql", v.AsString(), ok)
	}
	if v, ok := atributo(t, span, "db.statement"); !ok || v.AsString() != strings.TrimSpace(sqlDePrueba) {
		t.Errorf("db.statement = %q (presente: %v), se esperaba el SQL completo", v.AsString(), ok)
	}
	if v, ok := atributo(t, span, "db.sql.table"); !ok || v.AsString() != nombreTabla {
		t.Errorf("db.sql.table = %q (presente: %v), se esperaba %q", v.AsString(), ok, nombreTabla)
	}
	if v, ok := atributo(t, span, "db.rows_affected"); !ok || v.AsInt64() != 3 {
		t.Errorf("db.rows_affected = %d (presente: %v), se esperaban 3", v.AsInt64(), ok)
	}
}

// El SKU de la consulta es inocuo, pero la regla es que ningún valor de los
// parámetros entre en la traza: así no se filtra uno sensible el día que haya
// otra consulta.
func TestTracer_NoRegistraLosParametros(t *testing.T) {
	sr, tp, qt := grabador(t)
	padreCtx, padre := conSpanPadre(t, tp)
	defer padre.End()

	const secreto = "no-tiene-que-aparecer-en-la-traza"
	ctx := qt.TraceQueryStart(padreCtx, nil, pgx.TraceQueryStartData{
		SQL:  sqlDePrueba,
		Args: []any{skuDePrueba, secreto},
	})
	qt.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{CommandTag: pgconn.NewCommandTag("SELECT 1")})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans terminados = %d, se esperaba 1", len(spans))
	}
	for _, kv := range spans[0].Attributes() {
		valor := kv.Value.Emit()
		if strings.Contains(valor, secreto) || strings.Contains(valor, skuDePrueba) {
			t.Errorf("el atributo %q filtró un parámetro de la consulta: %q", kv.Key, valor)
		}
	}
	if nombre := spans[0].Name(); strings.Contains(nombre, skuDePrueba) {
		t.Errorf("el nombre del span filtró un parámetro: %q", nombre)
	}
}

func TestTracer_MarcaElErrorDeLaConsulta(t *testing.T) {
	sr, tp, qt := grabador(t)
	padreCtx, padre := conSpanPadre(t, tp)
	defer padre.End()

	fallaDeLaBase := errors.New("conexión rechazada")
	ctx := qt.TraceQueryStart(padreCtx, nil, pgx.TraceQueryStartData{SQL: sqlDePrueba})
	qt.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: fallaDeLaBase})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans terminados = %d, se esperaba 1", len(spans))
	}
	span := spans[0]
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, se esperaba error", span.Status().Code)
	}
	if !strings.Contains(span.Status().Description, fallaDeLaBase.Error()) {
		t.Errorf("descripción del status = %q, se esperaba que mencione %q",
			span.Status().Description, fallaDeLaBase.Error())
	}
	if len(span.Events()) == 0 {
		t.Error("no se registró el evento de la excepción en el span")
	}
}

// Con la base caída el pool falla al tomar la conexión, antes de ejecutar
// ninguna consulta: el span del intento de conexión es el que cuenta en la traza
// que el 503 vino de Postgres.
func TestTracer_MarcaElErrorDeConexion(t *testing.T) {
	sr, tp, qt := grabador(t)
	padreCtx, padre := conSpanPadre(t, tp)
	defer padre.End()

	cfg, err := pgx.ParseConfig("postgres://usuario:secreto@postgres:5432/proveedores")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	baseCaida := errors.New("dial tcp 172.28.0.2:5432: connect: connection refused")

	ctx := qt.TraceConnectStart(padreCtx, pgx.TraceConnectStartData{ConnConfig: cfg})
	qt.TraceConnectEnd(ctx, pgx.TraceConnectEndData{Err: baseCaida})

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("spans terminados = %d, se esperaba 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "postgres connect" {
		t.Errorf("nombre del span = %q, se esperaba %q", span.Name(), "postgres connect")
	}
	if span.Parent().SpanID() != padre.SpanContext().SpanID() {
		t.Error("el span de la conexión no cuelga del span de la request")
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, se esperaba error", span.Status().Code)
	}
	if v, ok := atributo(t, span, "server.address"); !ok || v.AsString() != "postgres" {
		t.Errorf("server.address = %q (presente: %v), se esperaba postgres", v.AsString(), ok)
	}
	// La URL de conexión trae credenciales: no tienen que terminar en la traza.
	for _, kv := range span.Attributes() {
		if strings.Contains(kv.Value.Emit(), "secreto") {
			t.Errorf("el atributo %q filtró la contraseña de la base", kv.Key)
		}
	}
}

func TestTracer_SinSpanPadreNoAbreSpanDeConexion(t *testing.T) {
	sr, _, qt := grabador(t)

	ctx := qt.TraceConnectStart(context.Background(), pgx.TraceConnectStartData{})
	qt.TraceConnectEnd(ctx, pgx.TraceConnectEndData{Err: errors.New("base caída")})

	if n := len(sr.Ended()); n != 0 {
		t.Errorf("spans terminados = %d, se esperaba ninguno", n)
	}
}

// Sin span padre no hay flujo que contar: el Ping de /readyz y las migraciones
// del arranque no tienen que generar trazas de un solo span en Jaeger.
func TestTracer_SinSpanPadreNoAbreSpan(t *testing.T) {
	sr, _, qt := grabador(t)

	ctx := qt.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: ";"})
	qt.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if n := len(sr.Ended()); n != 0 {
		t.Errorf("spans terminados = %d, se esperaba ninguno", n)
	}
}

// El tracer tiene que poder vivir sin proveedor de trazas configurado: es el
// caso del servicio corriendo sin colector OTLP, y el de `go test` sin Docker.
func TestTracer_SinProveedorNoRompe(t *testing.T) {
	qt := postgres.NuevoTracer(nil)

	ctx := qt.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{SQL: sqlDePrueba})
	qt.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("algo falló")})
}
