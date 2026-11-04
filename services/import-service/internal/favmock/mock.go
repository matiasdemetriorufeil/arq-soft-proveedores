// Package favmock es un mock del catálogo de FAV que cumple su contrato
// v1.1.0. Sirve para levantar el sistema sin depender de que FAV esté
// desplegado y para simular sus fallas en el Game Day. El test de contrato
// (internal/fav) comprueba que sus respuestas validan contra el OpenAPI de FAV:
// si el mock se desvía del contrato, el test falla.
//
// No reemplaza al mock que publica FAV en su repositorio: cuando tengamos su
// URL, import-service apunta ahí y este queda para desarrollo.
package favmock

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed catalogo.json
var catalogoInicial []byte

// Modos de falla que se pueden activar con POST /_mock/modo.
const (
	ModoNormal   = "normal"
	ModoCaido    = "caido"    // 503 NO_DISPONIBLE
	ModoLento    = "lento"    // responde después de 5 s (más que el timeout de 3 s)
	ModoLimite   = "limite"   // 429 LIMITE_EXCEDIDO con Retry-After
	ModoError    = "error"    // 500 ERROR_INTERNO
	ModoInvalido = "invalido" // 200 con un cuerpo que no cumple el contrato
)

var modos = map[string]bool{ModoNormal: true, ModoCaido: true, ModoLento: true, ModoLimite: true, ModoError: true, ModoInvalido: true}

// DemoraLenta es cuánto tarda en modo lento.
var DemoraLenta = 5 * time.Second

type variante struct {
	VarianteID string  `json:"varianteId"`
	Talle      string  `json:"talle"`
	Color      string  `json:"color"`
	Precio     float64 `json:"precio"`
}

type producto struct {
	ProductoID  string     `json:"productoId"`
	Nombre      string     `json:"nombre"`
	Categoria   string     `json:"categoria"`
	Descripcion string     `json:"descripcion,omitempty"`
	FotoURL     string     `json:"fotoUrl,omitempty"`
	Variantes   []variante `json:"variantes"`
}

// Mock es el servidor. Es seguro para uso concurrente.
type Mock struct {
	clave string

	mu       sync.Mutex
	modo     string
	catalogo []producto
	pedidos  int
}

// Nuevo arma el mock con el catálogo de ejemplo y la clave que acepta.
func Nuevo(clave string) (*Mock, error) {
	var cat struct {
		Items []producto `json:"items"`
	}
	if err := json.Unmarshal(catalogoInicial, &cat); err != nil {
		return nil, fmt.Errorf("catálogo del mock: %w", err)
	}
	return &Mock{clave: clave, modo: ModoNormal, catalogo: cat.Items}, nil
}

// Handler devuelve las rutas del mock.
func (m *Mock) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/productos", m.listarProductos)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// La sonda del contenedor pregunta por /readyz, igual que en los servicios.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	// Rutas de control: no son parte del contrato de FAV, son del mock.
	mux.HandleFunc("GET /_mock/estado", m.estado)
	mux.HandleFunc("POST /_mock/modo", m.cambiarModo)
	mux.HandleFunc("POST /_mock/precio", m.cambiarPrecio)
	return mux
}

func (m *Mock) listarProductos(w http.ResponseWriter, r *http.Request) {
	corr := r.Header.Get("X-Correlation-Id")
	if corr == "" {
		corr = fmt.Sprintf("mock-%d", time.Now().UnixNano())
	}
	w.Header().Set("X-Correlation-Id", corr)

	if r.Header.Get("X-API-Key") == "" || r.Header.Get("X-API-Key") != m.clave {
		problema(w, http.StatusUnauthorized, "no-autenticado", "No autenticado", "NO_AUTENTICADO")
		return
	}

	m.mu.Lock()
	m.pedidos++
	modo := m.modo
	cuerpo, err := json.Marshal(struct {
		Items []producto `json:"items"`
	}{m.catalogo})
	m.mu.Unlock()
	if err != nil {
		problema(w, http.StatusInternalServerError, "error-interno", "Error interno", "ERROR_INTERNO")
		return
	}

	switch modo {
	case ModoCaido:
		w.Header().Set("Retry-After", "5")
		problema(w, http.StatusServiceUnavailable, "no-disponible", "Servicio no disponible", "NO_DISPONIBLE")
		return
	case ModoLimite:
		w.Header().Set("Retry-After", "2")
		problema(w, http.StatusTooManyRequests, "limite-excedido", "Límite excedido", "LIMITE_EXCEDIDO")
		return
	case ModoError:
		problema(w, http.StatusInternalServerError, "error-interno", "Error interno", "ERROR_INTERNO")
		return
	case ModoInvalido:
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"productos":"esto no es el contrato"}`))
		return
	case ModoLento:
		select {
		case <-r.Context().Done():
			return
		case <-time.After(DemoraLenta):
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(cuerpo)
}

func problema(w http.ResponseWriter, status int, tipo, titulo, codigo string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type":   "https://fav.example/errores/" + tipo,
		"title":  titulo,
		"status": status,
		"codigo": codigo,
	})
}

func (m *Mock) estado(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"modo": m.modo, "pedidos_de_catalogo": m.pedidos, "productos": len(m.catalogo)})
}

func (m *Mock) cambiarModo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Modo string `json:"modo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !modos[body.Modo] {
		http.Error(w, "modo inválido: usar normal, caido, lento, limite, error o invalido", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.modo = body.Modo
	m.mu.Unlock()
	m.estado(w, r)
}

// cambiarPrecio cambia el precio de una variante, para mostrar en la demo que
// un cambio en FAV llega al ranking.
func (m *Mock) cambiarPrecio(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VarianteID string  `json:"varianteId"`
		Precio     float64 `json:"precio"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.VarianteID) == "" || body.Precio < 0 {
		http.Error(w, `cuerpo esperado: {"varianteId":"...","precio":12345}`, http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.catalogo {
		for j := range m.catalogo[i].Variantes {
			if m.catalogo[i].Variantes[j].VarianteID == body.VarianteID {
				m.catalogo[i].Variantes[j].Precio = body.Precio
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}
	http.Error(w, "variante inexistente", http.StatusNotFound)
}
