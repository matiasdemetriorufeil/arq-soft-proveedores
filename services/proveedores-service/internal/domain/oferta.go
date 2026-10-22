// Package domain contiene las reglas de negocio del ranking de proveedores.
// No depende de nada fuera de la biblioteca estándar.
package domain

import "errors"

// Errores de dominio. Los adaptadores los traducen a respuestas concretas.
var (
	// ErrParametroInvalido indica que cantidad o límite están fuera de rango.
	ErrParametroInvalido = errors.New("parametro invalido")
	// ErrProductoNoEncontrado indica que el SKU consultado no existe.
	ErrProductoNoEncontrado = errors.New("producto no encontrado")
)

// Límites de los parámetros de consulta.
const (
	CantidadPorDefecto = 1
	LimitePorDefecto   = 5
	LimiteMaximo       = 50
)

// Oferta es lo que un proveedor ofrece para un producto: precio, descuento
// por volumen, flete y stock disponible.
type Oferta struct {
	ProveedorID             int64
	ProveedorNombre         string
	ProductoSKU             string
	PrecioUnitario          float64
	DescuentoPct            float64 // 0..100
	CantidadMinimaDescuento int     // el descuento aplica si cantidad >= este valor
	Flete                   float64
	StockDisponible         int
}

// Recomendacion es una oferta evaluada para una cantidad concreta.
type Recomendacion struct {
	ProveedorID       int64   `json:"proveedor_id"`
	ProveedorNombre   string  `json:"proveedor_nombre"`
	PrecioUnitario    float64 `json:"precio_unitario"`
	DescuentoPct      float64 `json:"descuento_pct"`
	DescuentoAplicado bool    `json:"descuento_aplicado"`
	Flete             float64 `json:"flete"`
	StockDisponible   int     `json:"stock_disponible"`
	CostoTotal        float64 `json:"costo_total"`
}
