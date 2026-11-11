// Package dominio es la capa anticorrupción de import-service (ADR-009):
// traduce el catálogo de FAV a ofertas con la forma de nuestro comparador y
// calcula qué cambió entre dos importaciones. No sabe nada de HTTP, MongoDB ni
// RabbitMQ.
package dominio

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
)

// Reglas para completar lo que el contrato de FAV no trae (ADR-009).
type Reglas struct {
	// PrefijoSKU distingue los productos de FAV del resto del catálogo.
	PrefijoSKU string
	// PrefijoCategoria separa los rubros: el comparador tiene energía solar y
	// suma ropa, y la búsqueda filtra por categoría (ADR-006).
	PrefijoCategoria string
	// Flete fijo por oferta, hasta acordarlo con FAV.
	Flete float64
	// StockPorVariante es el respaldo cuando no hay stock real: el catálogo de
	// FAV no informa stock, así que la oferta vale hasta lo máximo que se le
	// puede pedir de una vez por variante (10). Desde su contrato 1.2.0 hay
	// GET /v1/disponibilidad y este tope solo se usa si esa consulta no está
	// disponible (ADR-009).
	StockPorVariante int
}

// ReglasFAV son las de ADR-009.
func ReglasFAV(flete float64) Reglas {
	return Reglas{PrefijoSKU: "FAV-", PrefijoCategoria: "ropa-", Flete: flete, StockPorVariante: 10}
}

// Item es una oferta de FAV ya traducida: un producto nuestro por variante.
type Item struct {
	SKU             string  `json:"sku" bson:"sku"`
	Nombre          string  `json:"nombre" bson:"nombre"`
	Categoria       string  `json:"categoria" bson:"categoria"`
	Unidad          string  `json:"unidad" bson:"unidad"`
	PrecioUnitario  float64 `json:"precio_unitario" bson:"precio_unitario"`
	Flete           float64 `json:"flete" bson:"flete"`
	StockDisponible int     `json:"stock_disponible" bson:"stock_disponible"`
}

// Descarte registra una variante que no entró, con el motivo. Se guarda con la
// importación para poder explicarlo.
type Descarte struct {
	VarianteID string `json:"variante_id" bson:"variante_id"`
	Motivo     string `json:"motivo" bson:"motivo"`
}

// Normalizado es el resultado de traducir un catálogo.
type Normalizado struct {
	Items     map[string]Item
	Descartes []Descarte
	Variantes int
}

// Normalizar traduce el catálogo sin stock real: cada oferta queda con el tope
// fijo de Reglas.StockPorVariante. Es el camino de respaldo para cuando FAV no
// expone GET /v1/disponibilidad.
func Normalizar(cat fav.Catalogo, r Reglas) Normalizado {
	return NormalizarConStock(cat, r, nil)
}

// NormalizarConStock traduce el catálogo usando el stock real por variante que
// devuelve GET /v1/disponibilidad (contrato 1.2.0 de FAV).
//
// stock es varianteID → unidades. Con stock nil se usa el tope fijo, que es el
// comportamiento anterior a la 1.2.0. Con stock presente, una variante que no
// esté en el mapa queda en **0**, no en el tope: el contrato de FAV informa con
// 0 tanto la variante inexistente como la sin stock, y suponer que hay 10
// unidades de algo que FAV no reconoce es lo que hacía terminar un pedido en
// 409. Una oferta con 0 no se descarta: se publica con 0 y el comparador la
// deja afuera del ranking porque no alcanza la cantidad pedida.
func NormalizarConStock(cat fav.Catalogo, r Reglas, stock map[string]int) Normalizado {
	out := Normalizado{Items: map[string]Item{}}
	for _, p := range cat.Items {
		for _, v := range p.Variantes {
			out.Variantes++
			id := strings.TrimSpace(v.VarianteID)
			switch {
			case id == "":
				out.Descartes = append(out.Descartes, Descarte{VarianteID: "", Motivo: "sin varianteId en " + p.ProductoID})
				continue
			case math.IsNaN(v.Precio) || math.IsInf(v.Precio, 0) || v.Precio < 0:
				out.Descartes = append(out.Descartes, Descarte{VarianteID: id, Motivo: fmt.Sprintf("precio inválido: %v", v.Precio)})
				continue
			}
			sku := r.PrefijoSKU + id
			if _, repetida := out.Items[sku]; repetida {
				out.Descartes = append(out.Descartes, Descarte{VarianteID: id, Motivo: "varianteId repetido"})
				continue
			}
			disponible := r.StockPorVariante
			if stock != nil {
				disponible = stock[id]
			}
			out.Items[sku] = Item{
				SKU:             sku,
				Nombre:          nombreDe(p, v),
				Categoria:       r.PrefijoCategoria + strings.ToLower(strings.TrimSpace(p.Categoria)),
				Unidad:          "unidad",
				PrecioUnitario:  redondear(v.Precio),
				Flete:           redondear(r.Flete),
				StockDisponible: disponible,
			}
		}
	}
	return out
}

func nombreDe(p fav.Producto, v fav.Variante) string {
	partes := []string{strings.TrimSpace(p.Nombre)}
	if t := strings.TrimSpace(v.Talle); t != "" {
		partes = append(partes, "talle "+t)
	}
	if c := strings.TrimSpace(v.Color); c != "" {
		partes = append(partes, c)
	}
	return strings.Join(partes, " · ")
}

// redondear lleva a centavos, que es la precisión de la columna NUMERIC(14,2)
// de proveedores-service.
func redondear(x float64) float64 {
	return math.Round(x*100) / 100
}

// Cambios es lo que hay que avisar respecto de lo último publicado.
type Cambios struct {
	// Altas y Modificadas van ordenadas por SKU.
	Altas       []Item
	Modificadas []Item
	// ProductosCambiados son los SKU (entre altas y modificadas) cuyo nombre,
	// categoría o unidad cambió, o que son nuevos.
	ProductosCambiados map[string]bool
	Bajas              []string
}

// Vacio dice si no hay nada que avisar.
func (c Cambios) Vacio() bool {
	return len(c.Altas) == 0 && len(c.Modificadas) == 0 && len(c.Bajas) == 0
}

// Comparar calcula los cambios de anterior a nuevo.
func Comparar(anterior, nuevo map[string]Item) Cambios {
	c := Cambios{ProductosCambiados: map[string]bool{}}
	for sku, n := range nuevo {
		a, existia := anterior[sku]
		switch {
		case !existia:
			c.Altas = append(c.Altas, n)
			c.ProductosCambiados[sku] = true
		case a != n:
			c.Modificadas = append(c.Modificadas, n)
			if a.Nombre != n.Nombre || a.Categoria != n.Categoria || a.Unidad != n.Unidad {
				c.ProductosCambiados[sku] = true
			}
		}
	}
	for sku := range anterior {
		if _, sigue := nuevo[sku]; !sigue {
			c.Bajas = append(c.Bajas, sku)
		}
	}
	sort.Slice(c.Altas, func(i, j int) bool { return c.Altas[i].SKU < c.Altas[j].SKU })
	sort.Slice(c.Modificadas, func(i, j int) bool { return c.Modificadas[i].SKU < c.Modificadas[j].SKU })
	sort.Strings(c.Bajas)
	return c
}
