package gateway

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxLogger
	ctxIntento
)

// nombreTracer identifica a la librería que instrumenta, no al servicio: el
// nombre del servicio va en el `resource` de las trazas.
const nombreTracer = "github.com/matiasdemetriorufeil/arq-soft-proveedores/services/api-gateway"

// statusClienteCerro se registra (no se envía) cuando el cliente cancela antes
// de recibir respuesta; sigue la convención 499 de nginx, igual que el servicio.
const statusClienteCerro = 499

// maxLongitudRequestID evita que un cliente inyecte valores enormes en los logs.
const maxLongitudRequestID = 128

func requestIDDe(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// LoggerDe devuelve el logger de la request (con request_id) o el default.
func LoggerDe(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxLogger).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// nuevoRequestID usa el mismo criterio que proveedores-service: 16 bytes al
// azar en hexadecimal, 32 caracteres.
func nuevoRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func requestIDValido(id string) bool {
	if id == "" || len(id) > maxLongitudRequestID {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e { // solo ASCII imprimible sin espacios
			return false
		}
	}
	return true
}

type respuestaConEstado struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *respuestaConEstado) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *respuestaConEstado) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap deja que http.ResponseController y httputil.ReverseProxy lleguen al
// ResponseWriter real (Flush, deadlines) a través del envoltorio.
func (r *respuestaConEstado) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// statusObservado es el status que efectivamente vio el cliente: si el handler
// no escribió nada, net/http manda 200, salvo que el cliente se haya ido.
func (r *respuestaConEstado) statusObservado(req *http.Request) int {
	if r.status != 0 {
		return r.status
	}
	if req.Context().Err() != nil {
		return statusClienteCerro
	}
	return http.StatusOK
}

// conObservabilidad es lo primero que toca la request: continúa la traza que
// venga en el `traceparent` (o abre una nueva) con el span padre de todo el
// flujo, resuelve el X-Request-ID, deja en el contexto un logger que ya trae
// `request_id` —y `trace_id`, porque el span está en ese contexto— y al cerrar
// registra una línea por request.
func conObservabilidad(base *slog.Logger, next http.Handler) http.Handler {
	tracer := otel.Tracer(nombreTracer)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()

		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		id := r.Header.Get(headerRequestID)
		if !requestIDValido(id) {
			id = nuevoRequestID()
		}
		// Se devuelve siempre, también en los errores: es la forma que tiene el
		// cliente de encontrar su request en los logs y en Jaeger.
		w.Header().Set(headerRequestID, id)
		span.SetAttributes(
			attribute.String("request_id", id),
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", r.URL.Path),
		)

		log := base.With("request_id", id)
		ctx = context.WithValue(ctx, ctxRequestID, id)
		ctx = context.WithValue(ctx, ctxLogger, log)

		rw := &respuestaConEstado{ResponseWriter: w}
		next.ServeHTTP(rw, r.WithContext(ctx))

		status := rw.statusObservado(r)
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}

		nivel := slog.LevelInfo
		switch {
		case status >= http.StatusInternalServerError:
			nivel = slog.LevelError
		case status >= http.StatusBadRequest:
			nivel = slog.LevelWarn
		}
		log.LogAttrs(ctx, nivel, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.Int("status", status),
			slog.Int("bytes", rw.bytes),
			slog.Float64("duration_ms", float64(time.Since(inicio).Microseconds())/1000),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}

// conAPIKey exige un X-API-Key de la lista configurada antes de llegar al
// upstream. La comparación es en tiempo constante para no filtrar por latencia
// cuánto prefijo de la clave acertó quien la manda.
func conAPIKey(keys []string, next http.Handler) http.Handler {
	validas := make([][]byte, 0, len(keys))
	for _, k := range keys {
		validas = append(validas, []byte(k))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recibida := []byte(r.Header.Get(headerAPIKey))
		ok := false
		if len(recibida) > 0 {
			for _, k := range validas {
				if subtle.ConstantTimeCompare(recibida, k) == 1 {
					ok = true
				}
			}
		}
		if !ok {
			escribirProblema(w, r, http.StatusUnauthorized, CodigoNoAutorizado,
				"falta el header X-API-Key o no es válido")
			return
		}
		next.ServeHTTP(w, r)
	})
}
