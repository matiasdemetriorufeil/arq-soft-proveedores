// Package http es el adaptador de entrada HTTP: expone la capacidad de
// proveedores recomendados y los chequeos de salud.
package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// Config agrupa las dependencias del adaptador HTTP.
type Config struct {
	Repo    ports.OfertasRepo
	Salud   ports.Salud
	APIKeys []string
	Logger  *slog.Logger
}

// NuevoHandler arma el router con sus middlewares.
func NuevoHandler(cfg Config) http.Handler {
	h := &handler{repo: cfg.Repo, salud: cfg.Salud}

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/proveedores/recomendados",
		conAPIKey(cfg.APIKeys, http.HandlerFunc(h.recomendados)))
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	return conRequestLog(cfg.Logger, mux)
}

type handler struct {
	repo  ports.OfertasRepo
	salud ports.Salud
}

func (h *handler) recomendados(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	sku := strings.TrimSpace(q.Get("producto"))
	if sku == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"el parámetro 'producto' (SKU) es obligatorio")
		return
	}
	cantidad, err := enteroPositivo(q.Get("cantidad"), domain.CantidadPorDefecto)
	if err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"'cantidad' "+err.Error())
		return
	}
	limite, err := enteroPositivo(q.Get("limite"), domain.LimitePorDefecto)
	if err == nil && limite > domain.LimiteMaximo {
		err = fmt.Errorf("no puede ser mayor a %d", domain.LimiteMaximo)
	}
	if err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"'limite' "+err.Error())
		return
	}

	ofertas, err := h.repo.OfertasPorProducto(r.Context(), sku)
	switch {
	case errors.Is(err, domain.ErrProductoNoEncontrado):
		escribirProblema(w, r, http.StatusNotFound, CodigoProductoNoEncontrado,
			fmt.Sprintf("no existe un producto con SKU '%s'", sku))
		return
	case err != nil && r.Context().Err() != nil:
		// El cliente canceló la request: no es una falla de la base.
		return
	case err != nil:
		LoggerDe(r.Context()).Error("falla al consultar ofertas", "producto", sku, "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"el almacenamiento de ofertas no está disponible; reintentar más tarde")
		return
	}

	recs, err := domain.Rankear(ofertas, cantidad, limite)
	if err != nil {
		// No debería ocurrir: los parámetros ya se validaron arriba.
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido, err.Error())
		return
	}
	escribirJSON(w, http.StatusOK, recs)
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	escribirJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.salud.Ping(r.Context()); err != nil {
		if r.Context().Err() != nil {
			return // el cliente canceló la request
		}
		LoggerDe(r.Context()).Error("readiness: la base no responde", "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"la base de datos no responde")
		return
	}
	escribirJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// enteroPositivo parsea un entero > 0; si el valor está vacío devuelve el default.
func enteroPositivo(v string, porDefecto int) (int, error) {
	if v == "" {
		return porDefecto, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, errors.New("debe ser un entero mayor a 0")
	}
	return n, nil
}
