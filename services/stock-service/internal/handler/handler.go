// Package handler es la capa de entrada HTTP de stock-service: valida la
// request, llama a la capa de servicio y traduce sus errores de negocio a
// problem+json. No tiene reglas propias ni conoce la base de datos.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// maxCuerpo corta los cuerpos desmedidos antes de decodificarlos: el de una
// reserva son dos campos.
const maxCuerpo = 4 << 10 // 4 KiB

// ServicioStock es lo que el adaptador necesita de la capa de negocio. Está
// declarada acá para poder probar la traducción de errores con un doble, sin
// base de datos.
type ServicioStock interface {
	Reservar(ctx context.Context, sku string, cantidad int, clave string) (repository.Reserva, bool, error)
	Stock(ctx context.Context, sku string) (repository.Stock, error)
}

// ServicioOrdenes es lo que el adaptador necesita de la capa de negocio para
// las órdenes de reposición (HU-14). Está declarada acá, igual que
// ServicioStock, para probar la traducción de errores con un doble.
type ServicioOrdenes interface {
	Ordenes(ctx context.Context, estado string) ([]repository.Orden, error)
	TransicionarOrden(ctx context.Context, id, hacia string) (repository.Orden, error)
}

// Salud es el chequeo de /readyz.
type Salud interface {
	Ping(ctx context.Context) error
}

// Config agrupa las dependencias del adaptador HTTP.
type Config struct {
	Servicio ServicioStock
	// Ordenes puede ser nil: en ese caso las rutas de órdenes no se registran
	// y el servicio expone solo la reserva.
	Ordenes ServicioOrdenes
	Salud   Salud
	APIKeys []string
	Logger  *slog.Logger
}

// NuevoHandler arma el router con sus middlewares. Las rutas de negocio van
// detrás de conAPIKey; las de salud, no: las consulta el healthcheck del
// contenedor, que no tiene la clave.
func NuevoHandler(cfg Config) http.Handler {
	h := &handler{svc: cfg.Servicio, ordenesSvc: cfg.Ordenes, salud: cfg.Salud}

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/stock/reservas",
		conAPIKey(cfg.APIKeys, http.HandlerFunc(h.reservar)))
	if cfg.Ordenes != nil {
		// Van antes de GET /api/v1/stock/{sku} en este archivo por claridad;
		// el mux de Go 1.22 elige igual el patrón más específico, así que
		// "ordenes" no se confunde con un SKU.
		mux.Handle("GET /api/v1/stock/ordenes",
			conAPIKey(cfg.APIKeys, http.HandlerFunc(h.ordenes)))
		mux.Handle("POST /api/v1/stock/ordenes/{id}/transiciones",
			conAPIKey(cfg.APIKeys, http.HandlerFunc(h.transicionarOrden)))
	}
	mux.Handle("GET /api/v1/stock/{sku}",
		conAPIKey(cfg.APIKeys, http.HandlerFunc(h.stock)))
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)

	return conRequestLog(cfg.Logger, mux)
}

type handler struct {
	svc        ServicioStock
	ordenesSvc ServicioOrdenes
	salud      Salud
}

// cuerpoReserva es el cuerpo de POST /api/v1/stock/reservas. `cantidad` queda
// sin decodificar para poder distinguir "falta" de "no es un entero": con un
// int, un 6.5 o un "6" entre comillas darían el mismo error que un JSON roto, y
// con un json.Number el "6" entre comillas pasaría como válido.
type cuerpoReserva struct {
	SKU      string          `json:"sku"`
	Cantidad json.RawMessage `json:"cantidad"`
}

// respuestaReserva es la vista pública de una reserva. No devuelve la clave de
// idempotencia: la mandó el cliente y no agrega nada a la respuesta.
type respuestaReserva struct {
	ID       string `json:"id"`
	SKU      string `json:"sku"`
	Cantidad int    `json:"cantidad"`
	Estado   string `json:"estado"`
	CreadoEn string `json:"creado_en"`
}

func (h *handler) reservar(w http.ResponseWriter, r *http.Request) {
	var cuerpo cuerpoReserva
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCuerpo)).Decode(&cuerpo); err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"el cuerpo tiene que ser un JSON con 'sku' y 'cantidad'")
		return
	}

	sku := strings.TrimSpace(cuerpo.SKU)
	if sku == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"'sku' es obligatorio")
		return
	}
	if len(cuerpo.Cantidad) == 0 {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"'cantidad' es obligatoria")
		return
	}
	// Atoi sobre el JSON crudo: rechaza 1.5, "6" entre comillas y null de una.
	cantidad, err := strconv.Atoi(string(cuerpo.Cantidad))
	if err != nil || cantidad <= 0 {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"'cantidad' debe ser un entero mayor a 0")
		return
	}
	clave := strings.TrimSpace(r.Header.Get(headerIdempotencia))
	if clave == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"falta el header "+headerIdempotencia+", que hace la reserva idempotente")
		return
	}

	reserva, repetida, err := h.svc.Reservar(r.Context(), sku, cantidad, clave)
	if err != nil {
		h.escribirErrorDeNegocio(w, r, err, sku)
		return
	}

	// 200 y no 201 cuando la clave de idempotencia ya se había aplicado: el
	// reintento no creó nada (CA-13.2).
	status := http.StatusCreated
	if repetida {
		status = http.StatusOK
	}
	escribirJSON(w, status, respuestaReserva{
		ID:       reserva.ID,
		SKU:      reserva.SKU,
		Cantidad: reserva.Cantidad,
		Estado:   reserva.Estado,
		CreadoEn: reserva.CreadoEn.UTC().Format(time.RFC3339),
	})
}

func (h *handler) stock(w http.ResponseWriter, r *http.Request) {
	sku := strings.TrimSpace(r.PathValue("sku"))
	if sku == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"el SKU es obligatorio")
		return
	}
	st, err := h.svc.Stock(r.Context(), sku)
	if err != nil {
		h.escribirErrorDeNegocio(w, r, err, sku)
		return
	}
	escribirJSON(w, http.StatusOK, st)
}

// escribirErrorDeNegocio es la única traducción de errores del servicio a HTTP.
// Lo que no sea un error de negocio conocido se trata como falla de
// infraestructura: 503, porque el cliente puede reintentar.
func (h *handler) escribirErrorDeNegocio(w http.ResponseWriter, r *http.Request, err error, sku string) {
	var insuficiente *service.StockInsuficienteError
	switch {
	case errors.Is(err, service.ErrParametroInvalido):
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido, err.Error())
	case errors.Is(err, service.ErrProductoNoEncontrado):
		escribirProblema(w, r, http.StatusNotFound, CodigoProductoNoEncontrado,
			fmt.Sprintf("no hay stock registrado para el SKU '%s'", sku))
	case errors.As(err, &insuficiente):
		escribirProblema(w, r, http.StatusConflict, CodigoStockInsuficiente,
			fmt.Sprintf("se pidieron %d unidades de '%s' y hay %d disponibles",
				insuficiente.Solicitado, insuficiente.SKU, insuficiente.Disponible))
	case r.Context().Err() != nil:
		// El cliente canceló la request: no es una falla de la base.
		return
	default:
		LoggerDe(r.Context()).Error("falla al operar sobre el stock", "sku", sku, "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"el almacenamiento de stock no está disponible; reintentar más tarde")
	}
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
