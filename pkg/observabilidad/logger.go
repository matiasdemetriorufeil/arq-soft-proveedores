// Package observabilidad da a todos los servicios del repo la misma forma de
// observarse: un logger JSON correlacionado con las trazas y la inicialización
// de OpenTelemetry. No importa nada de los servicios, para que cualquiera de
// ellos pueda adoptarlo.
package observabilidad

import (
	"context"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// NuevoLogger devuelve un logger JSON sobre stdout con el atributo `service`,
// que agrega `trace_id` y `span_id` a toda línea emitida con un contexto que
// tenga un span válido. Es la correlación entre logs y trazas: con el
// `request_id` que devuelve el gateway se encuentra la línea de log, y con su
// `trace_id` la traza en Jaeger.
//
// La correlación solo aparece en los métodos que reciben contexto
// (`InfoContext`, `ErrorContext`, `LogAttrs`); `log.Info(...)` emite la línea
// sin ids porque no hay de dónde leerlos.
func NuevoLogger(servicio string) *slog.Logger {
	return nuevoLoggerEn(os.Stdout, servicio)
}

// nuevoLoggerEn es NuevoLogger con la salida explícita, para los tests.
func nuevoLoggerEn(w io.Writer, servicio string) *slog.Logger {
	return slog.New(&handlerTrazas{siguiente: slog.NewJSONHandler(w, nil)}).
		With("service", servicio)
}

// handlerTrazas envuelve a otro slog.Handler y le agrega los ids de la traza.
// Va acá y no en cada llamada porque Handle recibe el context.Context: así todo
// el que loguea queda correlacionado sin tener que acordarse de los ids.
type handlerTrazas struct {
	siguiente slog.Handler
}

func (h *handlerTrazas) Enabled(ctx context.Context, nivel slog.Level) bool {
	return h.siguiente.Enabled(ctx, nivel)
}

func (h *handlerTrazas) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.siguiente.Handle(ctx, r)
}

func (h *handlerTrazas) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &handlerTrazas{siguiente: h.siguiente.WithAttrs(attrs)}
}

// WithGroup anida los ids de la traza dentro del grupo, igual que el resto de
// los atributos. Los servicios loguean plano, así que no se usa.
func (h *handlerTrazas) WithGroup(nombre string) slog.Handler {
	if nombre == "" {
		return h
	}
	return &handlerTrazas{siguiente: h.siguiente.WithGroup(nombre)}
}
