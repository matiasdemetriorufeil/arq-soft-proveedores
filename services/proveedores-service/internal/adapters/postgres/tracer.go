package postgres

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// nombreTracer identifica a la librería que instrumenta, no al servicio: el
// nombre del servicio va en el `resource` de las trazas.
const nombreTracer = "github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"

// Tracer instrumenta el acceso a la base: abre un span por cada Query, QueryRow
// o Exec que pgx ejecute, y otro por cada intento de conexión. Como el
// repositorio le pasa a pgx el contexto de la request, esos spans cuelgan del
// span del servidor y en Jaeger la traza llega hasta la base: gateway →
// servicio → consulta.
//
// Vive acá y no en pkg/observabilidad porque implementarlo exige importar pgx, y
// el paquete compartido también lo usa el gateway, que no tiene base de datos.
type Tracer struct {
	tracer trace.Tracer
}

var (
	_ pgx.QueryTracer   = (*Tracer)(nil)
	_ pgx.ConnectTracer = (*Tracer)(nil)
)

// NuevoTracer crea el tracer del adaptador. Con tp en nil toma el proveedor
// global de trazas —el que deja puesto observabilidad.IniciarTrazas—, que es lo
// que hace el servicio; los tests le pasan uno propio para poder inspeccionar
// los spans que se emiten.
func NuevoTracer(tp trace.TracerProvider) *Tracer {
	if tp == nil {
		// El proveedor global delega: si IniciarTrazas corre después que esto, el
		// tracer empieza a emitir igual, sin tener que recrearse.
		tp = otel.GetTracerProvider()
	}
	return &Tracer{tracer: tp.Tracer(nombreTracer)}
}

// TraceQueryStart abre el span de la consulta y lo devuelve en el contexto, que
// es el que pgx le pasa después a TraceQueryEnd.
func (t *Tracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	// Solo se instrumentan las consultas que ya son parte de una traza. Las
	// migraciones del arranque y el Ping de /readyz —que el healthcheck del
	// contenedor repite cada pocos segundos— no tienen span padre: trazarlas
	// llenaría Jaeger de trazas de un solo span que no cuentan ningún flujo y
	// taparían la que sí importa.
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	sql := strings.TrimSpace(data.SQL)
	atributos := []attribute.KeyValue{
		attribute.String("db.system", "postgresql"),
		// El SQL va completo; data.Args, nunca. Hoy el único parámetro es un SKU,
		// que es inocuo, pero la convención evita que mañana un dato sensible
		// termine guardado en una traza.
		attribute.String("db.statement", sql),
	}
	if tabla := tablaDe(sql); tabla != "" {
		atributos = append(atributos, attribute.String("db.sql.table", tabla))
	}

	ctx, _ = t.tracer.Start(ctx, nombreSpan(sql),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(atributos...))
	return ctx
}

// TraceQueryEnd cierra el span y, si la consulta falló, lo marca con el error.
// Una base caída se ve así en Jaeger: el span rojo de la consulta dentro del
// span de la request que devolvió 503.
func (t *Tracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return // TraceQueryStart no abrió span para esta consulta
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	} else {
		// También cuando son 0 filas: un SELECT que no encontró nada es un dato,
		// y omitir el atributo lo haría indistinguible de un tracer que no mide
		// filas.
		span.SetAttributes(attribute.Int64("db.rows_affected", data.CommandTag.RowsAffected()))
	}
	span.End()
}

// TraceConnectStart abre el span del intento de conexión. Hace falta además del
// de la consulta porque con la base caída el pool falla al tomar una conexión,
// antes de llegar a ejecutar nada: sin esto, la traza de un 503 no diría en
// ninguna parte que el problema estuvo en Postgres.
//
// En el camino feliz no aparece: el pool ya tiene su conexión abierta desde las
// migraciones, así que la request no abre ninguna.
func (t *Tracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx // igual que las consultas: sin traza en curso no se instrumenta
	}

	atributos := []attribute.KeyValue{attribute.String("db.system", "postgresql")}
	if cfg := data.ConnConfig; cfg != nil {
		// Host y puerto nada más: ni el usuario ni la contraseña de la URL.
		atributos = append(atributos,
			attribute.String("server.address", cfg.Host),
			attribute.Int("server.port", int(cfg.Port)))
	}

	ctx, _ = t.tracer.Start(ctx, "postgres connect",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(atributos...))
	return ctx
}

// TraceConnectEnd cierra el span de la conexión y lo marca si no se pudo abrir.
func (t *Tracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return // TraceConnectStart no abrió span para este intento
	}
	if data.Err != nil {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

// nombreSpan sigue la convención de OpenTelemetry para bases de datos:
// `<operación> <tabla>`, por ejemplo "SELECT productos". Es de baja cardinalidad
// —no depende de los parámetros— y se lee de un vistazo en Jaeger.
func nombreSpan(sql string) string {
	operacion := strings.ToUpper(operacionDe(sql))
	tabla := tablaDe(sql)
	switch {
	case operacion != "" && tabla != "":
		return operacion + " " + tabla
	case operacion != "":
		return operacion
	default:
		return "postgres"
	}
}

// operacionDe devuelve la primera palabra del statement (SELECT, INSERT, …) o
// "" si no empieza con una.
func operacionDe(sql string) string {
	for i, c := range sql {
		if !esLetra(c) {
			return sql[:i]
		}
	}
	return sql
}

func esLetra(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// tablaDe devuelve la tabla principal del statement: el identificador que sigue
// al primer FROM. Es a propósito un reconocedor mínimo y no un parser de SQL; si
// no encuentra un FROM —un INSERT, un CREATE TABLE— el atributo no se agrega y
// el span queda igual de válido.
func tablaDe(sql string) string {
	campos := strings.Fields(sql)
	for i, campo := range campos {
		if strings.EqualFold(campo, "FROM") && i+1 < len(campos) {
			return strings.Trim(campos[i+1], `";`)
		}
	}
	return ""
}
