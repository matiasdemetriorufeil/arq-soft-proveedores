package http

import (
	"encoding/json"
	"net/http"
)

// Códigos de error del contrato.
const (
	CodigoParametroInvalido    = "PARAMETRO_INVALIDO"
	CodigoNoAutorizado         = "NO_AUTORIZADO"
	CodigoProductoNoEncontrado = "PRODUCTO_NO_ENCONTRADO"
	CodigoNoDisponible         = "NO_DISPONIBLE"
	tipoProblemaBase           = "https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#"
	contentTypeProblem         = "application/problem+json"
	contentTypeJSON            = "application/json"
	headerRequestID            = "X-Request-ID"
	headerAPIKey               = "X-API-Key"
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
	CodigoParametroInvalido:    "Parámetro inválido",
	CodigoNoAutorizado:         "No autorizado",
	CodigoProductoNoEncontrado: "Producto no encontrado",
	CodigoNoDisponible:         "Servicio no disponible",
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
