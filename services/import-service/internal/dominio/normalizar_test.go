package dominio

import (
	"math"
	"reflect"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
)

// El ejemplo del contrato de FAV v1.1.0 (components.examples.Catalogo).
func catalogoDelContrato() fav.Catalogo {
	return fav.Catalogo{Items: []fav.Producto{
		{ProductoID: "prod-remera-basica", Nombre: "Remera básica", Categoria: "remeras", Variantes: []fav.Variante{
			{VarianteID: "var-remera-basica-blanca-m", Talle: "M", Color: "Blanco", Precio: 15000},
			{VarianteID: "var-remera-basica-negra-l", Talle: "L", Color: "Negro", Precio: 15000},
		}},
		{ProductoID: "prod-jean-mom", Nombre: "Jean mom", Categoria: "jeans", Variantes: []fav.Variante{
			{VarianteID: "var-jean-mom-celeste-38", Talle: "38", Color: "Celeste", Precio: 42000},
		}},
	}}
}

func TestNormalizarUnProductoPorVarianteConLasReglasDeADR009(t *testing.T) {
	n := Normalizar(catalogoDelContrato(), ReglasFAV(2500))
	if len(n.Items) != 3 || n.Variantes != 3 || len(n.Descartes) != 0 {
		t.Fatalf("items = %d, variantes = %d, descartes = %v", len(n.Items), n.Variantes, n.Descartes)
	}
	got := n.Items["FAV-var-jean-mom-celeste-38"]
	want := Item{
		SKU:             "FAV-var-jean-mom-celeste-38",
		Nombre:          "Jean mom · talle 38 · Celeste",
		Categoria:       "ropa-jeans",
		Unidad:          "unidad",
		PrecioUnitario:  42000,
		Flete:           2500,
		StockDisponible: 10,
	}
	if got != want {
		t.Errorf("item = %+v\nquiero  %+v", got, want)
	}
}

func TestNormalizarDescartaVariantesInvalidasSinFrenarLaImportacion(t *testing.T) {
	cat := fav.Catalogo{Items: []fav.Producto{{ProductoID: "p", Nombre: "Buzo", Categoria: "Buzos", Variantes: []fav.Variante{
		{VarianteID: "ok", Talle: "S", Color: "Gris", Precio: 30000.456},
		{VarianteID: "", Precio: 1},
		{VarianteID: "negativo", Precio: -1},
		{VarianteID: "nan", Precio: math.NaN()},
		{VarianteID: "ok", Precio: 99},
	}}}}
	n := Normalizar(cat, ReglasFAV(0))
	if len(n.Items) != 1 {
		t.Fatalf("items = %v", n.Items)
	}
	if len(n.Descartes) != 4 {
		t.Errorf("descartes = %v, quiero 4", n.Descartes)
	}
	it := n.Items["FAV-ok"]
	if it.PrecioUnitario != 30000.46 {
		t.Errorf("precio = %v: tiene que quedar en centavos", it.PrecioUnitario)
	}
	if it.Categoria != "ropa-buzos" {
		t.Errorf("categoría = %q", it.Categoria)
	}
	if it.Nombre != "Buzo · talle S · Gris" {
		t.Errorf("nombre = %q", it.Nombre)
	}
}

func items(xs ...Item) map[string]Item {
	m := map[string]Item{}
	for _, x := range xs {
		m[x.SKU] = x
	}
	return m
}

func TestCompararDetectaAltasModificacionesYBajas(t *testing.T) {
	a := Item{SKU: "A", Nombre: "A", Categoria: "c", Unidad: "unidad", PrecioUnitario: 100, StockDisponible: 10}
	b := Item{SKU: "B", Nombre: "B", Categoria: "c", Unidad: "unidad", PrecioUnitario: 200, StockDisponible: 10}
	c := Item{SKU: "C", Nombre: "C", Categoria: "c", Unidad: "unidad", PrecioUnitario: 300, StockDisponible: 10}

	bMasCaro := b
	bMasCaro.PrecioUnitario = 250
	cRenombrado := c
	cRenombrado.Nombre = "C nuevo"
	d := Item{SKU: "D", Nombre: "D", Categoria: "c", Unidad: "unidad", PrecioUnitario: 400, StockDisponible: 10}

	cambios := Comparar(items(a, b, c), items(bMasCaro, cRenombrado, d))

	if !reflect.DeepEqual(cambios.Altas, []Item{d}) {
		t.Errorf("altas = %v", cambios.Altas)
	}
	if !reflect.DeepEqual(cambios.Modificadas, []Item{bMasCaro, cRenombrado}) {
		t.Errorf("modificadas = %v", cambios.Modificadas)
	}
	if !reflect.DeepEqual(cambios.Bajas, []string{"A"}) {
		t.Errorf("bajas = %v", cambios.Bajas)
	}
	// Un cambio de precio no obliga a reenviar el producto; un alta o un
	// cambio de nombre sí.
	if !reflect.DeepEqual(cambios.ProductosCambiados, map[string]bool{"C": true, "D": true}) {
		t.Errorf("productos cambiados = %v", cambios.ProductosCambiados)
	}
}

func TestCompararSinCambiosEsVacio(t *testing.T) {
	n := Normalizar(catalogoDelContrato(), ReglasFAV(0))
	if c := Comparar(n.Items, Normalizar(catalogoDelContrato(), ReglasFAV(0)).Items); !c.Vacio() {
		t.Errorf("dos importaciones iguales dieron cambios: %+v", c)
	}
	if c := Comparar(nil, n.Items); len(c.Altas) != 3 {
		t.Errorf("contra nada, todo tiene que ser alta: %+v", c)
	}
}
