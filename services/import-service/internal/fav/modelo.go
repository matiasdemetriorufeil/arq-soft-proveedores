// Package fav es el cliente del catálogo de FAV (la tienda de ropa) según su
// contrato v1.1.0 (docs/contracts/consumidos/). Solo conoce el formato de FAV:
// la traducción a nuestro modelo está en el paquete dominio.
package fav

// Catalogo es la respuesta de GET /v1/productos.
type Catalogo struct {
	Items []Producto `json:"items"`
}

// Producto publicado por FAV.
type Producto struct {
	ProductoID  string     `json:"productoId"`
	Nombre      string     `json:"nombre"`
	Categoria   string     `json:"categoria"`
	Descripcion string     `json:"descripcion,omitempty"`
	FotoURL     string     `json:"fotoUrl,omitempty"`
	Variantes   []Variante `json:"variantes"`
}

// Variante de un producto (talle y color). Precio es por unidad, en pesos, con
// la promoción aplicada.
type Variante struct {
	VarianteID string  `json:"varianteId"`
	Talle      string  `json:"talle"`
	Color      string  `json:"color"`
	Precio     float64 `json:"precio"`
}

// Disponibilidad es la respuesta de GET /v1/disponibilidad, que FAV agregó en
// su contrato 1.2.0. El catálogo no informa stock, así que hasta esa versión
// las ofertas entraban con un tope fijo por variante.
type Disponibilidad struct {
	Items []VarianteDisponible `json:"items"`
}

// VarianteDisponible son las unidades del stock online de una variante que no
// están reservadas. Una variante inexistente o sin stock viene con 0, sin
// error: lo dice su contrato.
type VarianteDisponible struct {
	VarianteID string `json:"varianteId"`
	Disponible int    `json:"disponible"`
}

// Problema es el error en formato RFC 9457 que devuelve FAV. Se decide
// siempre por Codigo (lo dice su contrato).
type Problema struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	Codigo string `json:"codigo"`
}
