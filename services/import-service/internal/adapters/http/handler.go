// Package http expone la API interna de import-service: salud, estado de la
// sincronización y un disparo manual. No pasa por el gateway ni se publica al
// host: es para operar el servicio (demo y Game Day), no para usuarios.
package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/sincronizacion"
)

// Sincronizador es lo que el handler necesita de la sincronización.
type Sincronizador interface {
	Sincronizar(ctx context.Context) (sincronizacion.Resumen, error)
	Estado(ctx context.Context) (sincronizacion.Estado, error)
}

// Salud dice si la base responde.
type Salud interface {
	Ping(ctx context.Context) error
}

// TimeoutSincronizacionManual acota POST /sincronizar: alcanza para los
// reintentos a FAV (4 intentos de 3 s y sus esperas) y la escritura.
const TimeoutSincronizacionManual = 30 * time.Second

// NuevoHandler arma las rutas.
func NuevoHandler(s Sincronizador, salud Salud, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		escribirJSON(w, http.StatusOK, map[string]string{"estado": "vivo"})
	})
	// /readyz depende solo de MongoDB. Sin RabbitMQ el servicio sigue siendo
	// útil: los eventos esperan en el outbox. Sin FAV, también: siguen las
	// últimas ofertas válidas.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := salud.Ping(r.Context()); err != nil {
			escribirJSON(w, http.StatusServiceUnavailable, map[string]string{"estado": "no_listo", "mongodb": err.Error()})
			return
		}
		escribirJSON(w, http.StatusOK, map[string]string{"estado": "listo"})
	})
	mux.HandleFunc("GET /estado", func(w http.ResponseWriter, r *http.Request) {
		e, err := s.Estado(r.Context())
		if err != nil {
			log.ErrorContext(r.Context(), "no se pudo armar el estado", "error", err.Error())
			escribirJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "la base no responde"})
			return
		}
		escribirJSON(w, http.StatusOK, e)
	})
	mux.HandleFunc("POST /sincronizar", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), TimeoutSincronizacionManual)
		defer cancel()
		res, err := s.Sincronizar(ctx)
		if err != nil {
			log.ErrorContext(ctx, "sincronización manual fallida", "error", err.Error())
			escribirJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		escribirJSON(w, http.StatusOK, res)
	})
	return mux
}

func escribirJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
