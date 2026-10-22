package http

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxLogger
)

// statusClienteCerro se registra (no se envía) cuando el cliente cancela la
// request antes de recibir respuesta; sigue la convención 499 de nginx.
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

// conRequestLog toma X-Request-ID (o genera uno), lo devuelve en la respuesta,
// deja en el contexto un logger con request_id y registra una línea por request.
func conRequestLog(base *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		id := r.Header.Get(headerRequestID)
		if !requestIDValido(id) {
			id = nuevoRequestID()
		}
		w.Header().Set(headerRequestID, id)

		log := base.With("request_id", id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		ctx = context.WithValue(ctx, ctxLogger, log)

		rw := &respuestaConEstado{ResponseWriter: w}
		next.ServeHTTP(rw, r.WithContext(ctx))
		if rw.status == 0 {
			rw.status = http.StatusOK
			if r.Context().Err() != nil {
				rw.status = statusClienteCerro
			}
		}

		nivel := slog.LevelInfo
		if rw.status >= 500 {
			nivel = slog.LevelError
		} else if rw.status >= 400 {
			nivel = slog.LevelWarn
		}
		log.LogAttrs(ctx, nivel, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("query", r.URL.RawQuery),
			slog.Int("status", rw.status),
			slog.Int("bytes", rw.bytes),
			slog.Float64("duration_ms", float64(time.Since(inicio).Microseconds())/1000),
			slog.String("remote_addr", r.RemoteAddr),
		)
	})
}

// conAPIKey exige un X-API-Key incluido en la lista configurada.
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
