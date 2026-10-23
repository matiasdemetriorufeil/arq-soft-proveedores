package gateway

import (
	"encoding/json"
	"net/http"
)

// Códigos de error. NO_AUTORIZADO y NO_DISPONIBLE son los del contrato de la
// capacidad: el gateway los devuelve con la misma forma que proveedores-service
// para que el cliente vea un solo tipo de error, venga de donde venga.
// RUTA_NO_ENCONTRADA es propio del gateway: una ruta que no expone no es un
// producto inexistente, así que no puede reusar PRODUCTO_NO_ENCONTRADO.
const (
	CodigoNoAutorizado     = "NO_AUTORIZADO"
	CodigoNoDisponible     = "NO_DISPONIBLE"
	CodigoRutaNoEncontrada = "RUTA_NO_ENCONTRADA"
)

const (
	tipoProblemaBase   = "https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#"
	contentTypeProblem = "application/problem+json"
	contentTypeJSON    = "application/json"
	headerRequestID    = "X-Request-ID"
	headerAPIKey       = "X-API-Key"
)

// Problem es un error RFC 9457 (application/problem+json) con la extensión
// `code`, igual que el del servicio.
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
	CodigoNoAutorizado:     "No autorizado",
	CodigoNoDisponible:     "Servicio no disponible",
	CodigoRutaNoEncontrada: "Ruta no encontrada",
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
