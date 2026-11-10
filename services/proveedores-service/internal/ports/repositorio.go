// Package ports define los puertos (interfaces) que el núcleo necesita del
// mundo exterior. Los adaptadores los implementan.
package ports

import (
	"context"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
)

// OfertasRepo es el puerto de lectura de ofertas.
type OfertasRepo interface {
	// OfertasPorProducto devuelve todas las ofertas del SKU. Si el SKU no
	// existe devuelve domain.ErrProductoNoEncontrado; si existe pero no tiene
	// ofertas devuelve una lista vacía y nil. Cualquier otro error indica que
	// el almacenamiento no está disponible.
	OfertasPorProducto(ctx context.Context, sku string) ([]domain.Oferta, error)
}

// Salud es el puerto que usa el chequeo de readiness.
type Salud interface {
	Ping(ctx context.Context) error
}

// --- Cambios de lista de precios ------------------------------------------------

// ProductoCambio es un producto nuevo o modificado por un cambio de lista.
type ProductoCambio struct {
	SKU       string
	Nombre    string
	Categoria string
	Unidad    string
}

// OfertaCambio es una oferta nueva o modificada por un cambio de lista.
type OfertaCambio struct {
	ProductoSKU             string
	PrecioUnitario          float64
	DescuentoPct            float64
	CantidadMinimaDescuento int
	Flete                   float64
	StockDisponible         int
}

// CambioListaPrecios es lo que un proveedor cambió, ya sin el formato del evento
// que lo trajo.
type CambioListaPrecios struct {
	EventoID    string
	ProveedorID int64
	Secuencia   int64
	// Completa indica que Ofertas es el catálogo entero del proveedor: las suyas
	// que no vengan se borran.
	Completa  bool
	Productos []ProductoCambio
	Ofertas   []OfertaCambio
	Bajas     []string
}

// ResultadoAplicacion dice qué pasó con un cambio.
type ResultadoAplicacion struct {
	// Aplicado es false si el evento era un duplicado o traía una secuencia vieja.
	Aplicado bool
	// Motivo explica por qué no se aplicó; vacío si se aplicó.
	Motivo string
	// SKUs son los productos tocados, sin repetir: incluye las ofertas borradas.
	SKUs []string
}

// AplicadorListaPrecios aplica un cambio de forma atómica e idempotente.
type AplicadorListaPrecios interface {
	AplicarListaPrecios(ctx context.Context, c CambioListaPrecios) (ResultadoAplicacion, error)
}

// InvalidadorOfertas borra lo que se tenga guardado de esos SKU en la caché.
type InvalidadorOfertas interface {
	InvalidarOfertas(ctx context.Context, skus []string) error
}

// AvisoIndexacion avisa que hay SKU nuevos para indexar. No bloquea.
type AvisoIndexacion interface {
	AvisarPendientes()
}

// --- Búsqueda ---------------------------------------------------------------------

// Órdenes de la búsqueda. Sin orden, los resultados van por relevancia.
const (
	OrdenPrecioAsc  = "precio_asc"
	OrdenPrecioDesc = "precio_desc"
)

// DocumentoProducto es un producto con sus agregados ya calculados, tal como se
// indexa y se devuelve en la búsqueda (ADR-006). PrecioMin es nil si ningún
// proveedor tiene stock.
type DocumentoProducto struct {
	SKU                 string    `json:"sku"`
	Nombre              string    `json:"nombre"`
	Categoria           string    `json:"categoria"`
	Unidad              string    `json:"unidad"`
	PrecioMin           *float64  `json:"precio_min"`
	ProveedoresConStock int       `json:"proveedores_con_stock"`
	StockTotal          int64     `json:"stock_total"`
	Proveedores         []string  `json:"proveedores"`
	ActualizadoEn       time.Time `json:"actualizado_en"`
}

// ConsultaProductos es una búsqueda ya validada.
type ConsultaProductos struct {
	Q         string
	Categoria string
	Proveedor string
	PrecioMin *float64
	PrecioMax *float64
	ConStock  bool
	Orden     string
	Pagina    int
	Tamanio   int
}

// ResultadoProductos es una página de resultados.
type ResultadoProductos struct {
	Resultados []DocumentoProducto `json:"resultados"`
	Total      int                 `json:"total"`
	Pagina     int                 `json:"pagina"`
	Tamanio    int                 `json:"tamanio"`
}

// BuscadorProductos es la lectura del índice. Cualquier error indica que el
// motor de búsqueda no está disponible.
type BuscadorProductos interface {
	Buscar(ctx context.Context, c ConsultaProductos) (ResultadoProductos, error)
}

// IndiceProductos es la escritura del índice. Es derivado: siempre se puede
// reconstruir desde PostgreSQL.
type IndiceProductos interface {
	// IndiceCargado dice si el índice existe y tiene documentos.
	IndiceCargado(ctx context.Context) (bool, error)
	// Indexar crea o reemplaza esos documentos y vuelve cuando quedaron visibles.
	Indexar(ctx context.Context, docs []DocumentoProducto) error
	// Reconstruir carga los documentos en un índice nuevo y lo intercambia con
	// el vigente: la búsqueda nunca queda vacía.
	Reconstruir(ctx context.Context, docs []DocumentoProducto) error
}

// SKUPendiente es un SKU por indexar, con la versión que se leyó.
type SKUPendiente struct {
	SKU     string
	Version int64
}

// AlmacenIndexacion es lo que la indexación necesita de la fuente de verdad.
type AlmacenIndexacion interface {
	SKUsPendientes(ctx context.Context, limite int) ([]SKUPendiente, error)
	// QuitarPendientes borra cada SKU solo si sigue en la versión leída.
	QuitarPendientes(ctx context.Context, pendientes []SKUPendiente) error
	// DocumentosDeProductos arma los documentos de esos SKU; los que no existen
	// como producto no aparecen.
	DocumentosDeProductos(ctx context.Context, skus []string) ([]DocumentoProducto, error)
	TodosLosDocumentos(ctx context.Context) ([]DocumentoProducto, error)
	// ConExclusionIndexacion corre fn si ninguna otra réplica está escribiendo
	// el índice, y devuelve si la corrió.
	ConExclusionIndexacion(ctx context.Context, fn func(context.Context) error) (bool, error)
}
