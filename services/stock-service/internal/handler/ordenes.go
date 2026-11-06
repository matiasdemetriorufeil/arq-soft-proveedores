package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// cuerpoTransicion es el cuerpo de POST /api/v1/stock/ordenes/{id}/transiciones.
type cuerpoTransicion struct {
	Estado string `json:"estado"`
}

// ordenes lista las órdenes de reposición, la más nueva primero, con filtro
// opcional por estado.
func (h *handler) ordenes(w http.ResponseWriter, r *http.Request) {
	estado := strings.TrimSpace(r.URL.Query().Get("estado"))
	lista, err := h.ordenesSvc.Ordenes(r.Context(), estado)
	if err != nil {
		h.escribirErrorDeOrden(w, r, err)
		return
	}
	escribirJSON(w, http.StatusOK, lista)
}

// transicionarOrden mueve una orden al estado pedido. Existe para operar a mano
// las transiciones que todavía no dispara nada: el ciclo de reposición deja la
// orden en BORRADOR y quien la pasa a SOLICITADA es el pedido al proveedor
// (ADR-009), que no está implementado.
func (h *handler) transicionarOrden(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"el id de la orden es obligatorio")
		return
	}

	var cuerpo cuerpoTransicion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCuerpo)).Decode(&cuerpo); err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			`el cuerpo tiene que ser un JSON con 'estado', por ejemplo {"estado":"SOLICITADA"}`)
		return
	}

	orden, err := h.ordenesSvc.TransicionarOrden(r.Context(), id, cuerpo.Estado)
	if err != nil {
		h.escribirErrorDeOrden(w, r, err)
		return
	}
	escribirJSON(w, http.StatusOK, orden)
}

// escribirErrorDeOrden es la única traducción de los errores de negocio de las
// órdenes a HTTP. Lo que no sea un error conocido se trata como falla de
// infraestructura: 503, porque el cliente puede reintentar.
func (h *handler) escribirErrorDeOrden(w http.ResponseWriter, r *http.Request, err error) {
	var invalida *service.TransicionInvalidaError
	switch {
	case errors.Is(err, service.ErrParametroInvalido):
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido, err.Error())
	case errors.Is(err, service.ErrOrdenNoEncontrada):
		escribirProblema(w, r, http.StatusNotFound, CodigoOrdenNoEncontrada,
			"no hay ninguna orden de reposición con ese id")
	case errors.As(err, &invalida):
		// CA-14.3: la orden no cambió. El 409 dice en qué estado quedó y qué
		// se permitía desde ahí.
		escribirProblema(w, r, http.StatusConflict, CodigoTransicionInvalida, invalida.Error())
	case r.Context().Err() != nil:
		// El cliente canceló la request: no es una falla de la base.
		return
	default:
		LoggerDe(r.Context()).Error("falla al operar sobre las órdenes de reposición", "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"el almacenamiento de stock no está disponible; reintentar más tarde")
	}
}
