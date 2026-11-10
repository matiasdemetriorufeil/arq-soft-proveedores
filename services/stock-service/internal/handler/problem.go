package handler

import (
	"encoding/json"
	"net/http"
)

// Códigos de error del contrato. Son los mismos de proveedores-service más
// STOCK_INSUFICIENTE, que es propio de la reserva, y ORDEN_NO_ENCONTRADA y
// TRANSICION_INVALIDA, que son propios de la orden de reposición (RN-15), y
// los del pedido a FAV y su webhook (ADR-009).
const (
	CodigoParametroInvalido     = "PARAMETRO_INVALIDO"
	CodigoNoAutorizado          = "NO_AUTORIZADO"
	CodigoProductoNoEncontrado  = "PRODUCTO_NO_ENCONTRADO"
	CodigoStockInsuficiente     = "STOCK_INSUFICIENTE"
	CodigoOrdenNoEncontrada     = "ORDEN_NO_ENCONTRADA"
	CodigoTransicionInvalida    = "TRANSICION_INVALIDA"
	CodigoNoDisponible          = "NO_DISPONIBLE"
	CodigoPedidoRechazado       = "PEDIDO_RECHAZADO"
	CodigoProveedorNoDisponible = "PROVEEDOR_NO_DISPONIBLE"
	CodigoFirmaInvalida         = "FIRMA_INVALIDA"
	CodigoAvisoInvalido         = "AVISO_INVALIDO"
	tipoProblemaBase            = "https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#"
	contentTypeProblem          = "application/problem+json"
	contentTypeJSON             = "application/json"
	headerRequestID             = "X-Request-ID"
	headerAPIKey                = "X-API-Key"
	headerIdempotencia          = "Idempotency-Key"
)

// Problem es un error RFC 9457 (application/problem+json) con la extensión `code`.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	Instance  string `json:"instance,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

var titulos = map[string]string{
	CodigoParametroInvalido:     "Parámetro inválido",
	CodigoNoAutorizado:          "No autorizado",
	CodigoProductoNoEncontrado:  "Producto no encontrado",
	CodigoStockInsuficiente:     "Stock insuficiente",
	CodigoOrdenNoEncontrada:     "Orden de reposición no encontrada",
	CodigoTransicionInvalida:    "Transición de estado inválida",
	CodigoNoDisponible:          "Servicio no disponible",
	CodigoPedidoRechazado:       "FAV rechazó el pedido",
	CodigoProveedorNoDisponible: "FAV no está disponible",
	CodigoFirmaInvalida:         "Firma del aviso inválida",
	CodigoAvisoInvalido:         "Aviso inválido",
}

func escribirProblema(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	p := Problem{
		Type:      tipoProblemaBase + code,
		Title:     titulos[code],
		Status:    status,
		Detail:    detail,
		Code:      code,
		Instance:  r.URL.Path,
		RequestID: requestIDDe(r.Context()),
	}
	w.Header().Set("Content-Type", contentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func escribirJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
