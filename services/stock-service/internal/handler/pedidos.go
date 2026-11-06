package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// maxAviso corta un webhook desmedido: el aviso de FAV son cuatro campos.
const maxAviso = 64 << 10

// enviarPedido hace el pedido a FAV de una orden en BORRADOR, en el momento,
// sin esperar el ciclo (POST /api/v1/stock/ordenes/{id}/pedido).
func (h *handler) enviarPedido(w http.ResponseWriter, r *http.Request) {
	orden, err := h.pedidosSvc.EnviarOrden(r.Context(), r.PathValue("id"))
	var fallo *service.EnvioFallidoError
	switch {
	case err == nil:
		escribirJSON(w, http.StatusOK, orden)
	case errors.Is(err, service.ErrOrdenNoEsDeFAV):
		escribirProblema(w, r, http.StatusConflict, CodigoPedidoRechazado, err.Error())
	case errors.As(err, &fallo) && fallo.Reintentable:
		// La orden sigue en BORRADOR y el ciclo reintenta con la misma clave.
		LoggerDe(r.Context()).Warn("FAV no aceptó el pedido; se reintenta", "orden", r.PathValue("id"), "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoProveedorNoDisponible, fallo.Motivo)
	case errors.As(err, &fallo):
		LoggerDe(r.Context()).Warn("FAV rechazó el pedido", "orden", r.PathValue("id"), "error", err.Error())
		escribirProblema(w, r, http.StatusConflict, CodigoPedidoRechazado, fallo.Motivo)
	default:
		h.escribirErrorDeOrden(w, r, err)
	}
}

// webhookFAV recibe el aviso pedidoActualizado de FAV (ADR-009 y su
// contrato): verifica la firma, deduplica por eventoId, responde enseguida y
// procesa después, confirmando el estado con GET /v1/pedidos/{pedidoId}.
func (h *handler) webhookFAV(w http.ResponseWriter, r *http.Request) {
	cuerpo, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAviso))
	if err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoAvisoInvalido, "el aviso es demasiado grande")
		return
	}
	// 1. La firma, antes de confiar en nada del cuerpo. Con 401, FAV no
	// reintenta: un aviso con firma mala no se va a arreglar solo.
	if err := fav.VerificarFirma(h.secreto, r.Header.Get("X-FAV-Firma"), cuerpo, h.ahora()); err != nil {
		LoggerDe(r.Context()).Warn("aviso de FAV rechazado", "error", err.Error(),
			"evento_id", r.Header.Get("X-FAV-Evento-Id"))
		escribirProblema(w, r, http.StatusUnauthorized, CodigoFirmaInvalida, err.Error())
		return
	}
	aviso, err := fav.LeerAviso(cuerpo)
	if err != nil {
		escribirProblema(w, r, http.StatusBadRequest, CodigoAvisoInvalido, err.Error())
		return
	}
	if id := strings.TrimSpace(r.Header.Get("X-FAV-Evento-Id")); id != "" && id != aviso.EventoID {
		escribirProblema(w, r, http.StatusBadRequest, CodigoAvisoInvalido,
			"X-FAV-Evento-Id no coincide con el eventoId del cuerpo")
		return
	}

	// 2 y 3. Deduplicar y responder rápido; el trabajo sigue en segundo plano.
	log := LoggerDe(r.Context()).With("evento_id", aviso.EventoID, "pedido_id", aviso.Pedido.PedidoID,
		"estado_fav", aviso.Pedido.Estado)
	nuevo, err := h.pedidosSvc.RecibirAviso(r.Context(), aviso, func(err error) {
		if err != nil {
			log.Error("no se pudo procesar el aviso de FAV; lo corrige la consulta de respaldo", "error", err.Error())
			return
		}
		log.Info("aviso de FAV procesado")
	})
	if err != nil {
		// Con un 5xx FAV reintenta: es lo que queremos si la base no respondió.
		log.Error("no se pudo registrar el aviso de FAV", "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"no se pudo registrar el aviso; reintentar")
		return
	}
	if !nuevo {
		log.Info("aviso de FAV repetido: ya se había recibido")
	}
	escribirJSON(w, http.StatusOK, map[string]any{"recibido": true, "repetido": !nuevo})
}
