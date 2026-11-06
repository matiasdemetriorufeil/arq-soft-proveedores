package mensajeria

// TipoListaPreciosActualizada es el evento que publica import-service cuando
// cambian las ofertas de un proveedor externo, y que consume
// proveedores-service para actualizar sus ofertas, invalidar la caché y
// reindexar la búsqueda (ADR-005). El formato completo está en
// docs/contracts/eventos.md.
const (
	TipoListaPreciosActualizada    = "ListaPreciosActualizada"
	VersionListaPreciosActualizada = 1
)

// Motivos posibles de un ListaPreciosActualizada.
const (
	// MotivoSincronizacion: la importación encontró cambios en el catálogo.
	MotivoSincronizacion = "sincronizacion"
	// MotivoCatalogoVencido: el proveedor lleva más de 24 h sin poder
	// sincronizarse y sus ofertas se retiran del ranking (ADR-009).
	MotivoCatalogoVencido = "catalogo_vencido"
)

// ListaPreciosActualizada son los datos del evento. Trae solo lo que cambió
// respecto del evento anterior del mismo proveedor, salvo cuando Completa es
// true: entonces Ofertas es el catálogo entero y el consumidor puede borrar
// las ofertas de ese proveedor que no vengan en la lista.
type ListaPreciosActualizada struct {
	Proveedor     ProveedorEvento `json:"proveedor"`
	ImportacionID string          `json:"importacion_id"`
	// Secuencia crece con cada evento del mismo proveedor. El consumidor
	// descarta un evento con una secuencia menor o igual a la última que
	// aplicó: así un reintento atrasado no pisa precios más nuevos.
	Secuencia int64  `json:"secuencia"`
	Motivo    string `json:"motivo"`
	Completa  bool   `json:"completa"`
	// Productos trae los productos nuevos o con nombre o categoría cambiados.
	Productos []ProductoEvento `json:"productos"`
	// Ofertas trae las ofertas nuevas o modificadas (o todas, si Completa).
	Ofertas []OfertaEvento `json:"ofertas"`
	// Bajas son los SKU cuyas ofertas de este proveedor hay que borrar.
	Bajas []string `json:"bajas"`
}

// ProveedorEvento identifica al proveedor dueño de las ofertas.
type ProveedorEvento struct {
	ID     int64  `json:"id"`
	Codigo string `json:"codigo"`
	Nombre string `json:"nombre"`
}

// ProductoEvento tiene las columnas de la tabla productos de
// proveedores-service.
type ProductoEvento struct {
	SKU       string `json:"sku"`
	Nombre    string `json:"nombre"`
	Categoria string `json:"categoria"`
	Unidad    string `json:"unidad"`
}

// OfertaEvento tiene las columnas de la tabla ofertas de proveedores-service.
// Los importes van en pesos con dos decimales.
type OfertaEvento struct {
	ProductoSKU             string  `json:"producto_sku"`
	PrecioUnitario          float64 `json:"precio_unitario"`
	DescuentoPct            float64 `json:"descuento_pct"`
	CantidadMinimaDescuento int     `json:"cantidad_minima_descuento"`
	Flete                   float64 `json:"flete"`
	StockDisponible         int     `json:"stock_disponible"`
}
