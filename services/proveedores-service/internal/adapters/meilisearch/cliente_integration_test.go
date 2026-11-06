//go:build integration

package meilisearch_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/meilisearch"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// La misma imagen que el docker-compose.yml.
const (
	imagenMeili  = "getmeili/meilisearch:v1.14"
	claveMaestra = "clave-maestra-de-prueba-16"
)

var urlMeili string

func TestMain(m *testing.M) { os.Exit(correr(m)) }

func correr(m *testing.M) int {
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        imagenMeili,
			Env:          map[string]string{"MEILI_MASTER_KEY": claveMaestra, "MEILI_ENV": "development", "MEILI_NO_ANALYTICS": "true"},
			ExposedPorts: []string{"7700/tcp"},
			WaitingFor:   wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(time.Minute),
		},
		Started: true,
	})
	if ctr != nil {
		defer func() { _ = testcontainers.TerminateContainer(ctr) }()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo levantar %s: %v\n", imagenMeili, err)
		return 1
	}
	if urlMeili, err = ctr.PortEndpoint(ctx, "7700/tcp", "http"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func precio(v float64) *float64 { return &v }

func doc(sku, nombre, categoria string, p *float64, proveedores ...string) ports.DocumentoProducto {
	return ports.DocumentoProducto{
		SKU: sku, Nombre: nombre, Categoria: categoria, Unidad: "unidad", PrecioMin: p,
		ProveedoresConStock: len(proveedores), StockTotal: int64(10 * len(proveedores)), Proveedores: proveedores,
		ActualizadoEn: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
}

func catalogo() []ports.DocumentoProducto {
	return []ports.DocumentoProducto{
		doc("PAN-550W", "Panel solar 550 W monocristalino", "paneles", precio(200), "Andes Solar SRL", "Grupo Litio SA"),
		doc("PAN-450W", "Panel solar 450 W monocristalino", "paneles", precio(150), "Andes Solar SRL"),
		doc("PAN-100W", "Panel flexible 100 W", "paneles", precio(300), "Grupo Litio SA"),
		doc("PAN-SIN", "Panel descontinuado", "paneles", nil),
		doc("INV-5KW", "Inversor híbrido 5 kW", "inversores", precio(900), "Grupo Litio SA"),
		doc("FAV-var-buzo-s", "Buzo canguro · talle S · Verde", "ropa-buzos", precio(38000), "FAV (tienda de ropa)"),
		// Un SKU que el motor no aceptaría como id, y una categoría con comillas.
		doc("FAV/var ñ.38", `Jean "mom"`, `ropa-"jeans"`, precio(39900), "FAV (tienda de ropa)"),
	}
}

// indice arma un índice propio por test, así no se pisan.
func indice(t *testing.T) *meilisearch.Cliente {
	t.Helper()
	c := meilisearch.NuevoCliente(urlMeili, claveMaestra, "t_"+fmt.Sprintf("%d", time.Now().UnixNano()))
	if err := c.Reconstruir(context.Background(), catalogo()); err != nil {
		t.Fatal(err)
	}
	return c
}

func skus(r ports.ResultadoProductos) []string {
	out := make([]string, len(r.Resultados))
	for i, d := range r.Resultados {
		out[i] = d.SKU
	}
	return out
}

func buscar(t *testing.T, c *meilisearch.Cliente, q ports.ConsultaProductos) ports.ResultadoProductos {
	t.Helper()
	if q.Pagina == 0 {
		q.Pagina = 1
	}
	if q.Tamanio == 0 {
		q.Tamanio = 20
	}
	r, err := c.Buscar(context.Background(), q)
	if err != nil {
		t.Fatalf("buscar %+v: %v", q, err)
	}
	return r
}

func TestBusquedaFiltrosOrdenYPaginacion(t *testing.T) {
	c := indice(t)

	t.Run("texto con error de tipeo", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Q: "invesor"})
		if r.Total != 1 || r.Resultados[0].SKU != "INV-5KW" {
			t.Errorf("resultado = %v", skus(r))
		}
	})
	t.Run("categoría", func(t *testing.T) {
		if r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles"}); r.Total != 4 {
			t.Errorf("total = %d (%v)", r.Total, skus(r))
		}
	})
	t.Run("proveedor", func(t *testing.T) {
		if r := buscar(t, c, ports.ConsultaProductos{Proveedor: "Andes Solar SRL"}); r.Total != 2 {
			t.Errorf("total = %d (%v)", r.Total, skus(r))
		}
	})
	t.Run("con stock", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", ConStock: true})
		if r.Total != 3 {
			t.Errorf("total = %d (%v)", r.Total, skus(r))
		}
	})
	t.Run("rango de precio", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{PrecioMin: precio(150), PrecioMax: precio(250)})
		if r.Total != 2 {
			t.Errorf("total = %d (%v)", r.Total, skus(r))
		}
	})
	t.Run("orden ascendente", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", ConStock: true, Orden: ports.OrdenPrecioAsc})
		if got := skus(r); got[0] != "PAN-450W" || got[1] != "PAN-550W" || got[2] != "PAN-100W" {
			t.Errorf("orden = %v", got)
		}
	})
	t.Run("orden descendente con texto manda sobre la relevancia", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Q: "panel", ConStock: true, Orden: ports.OrdenPrecioDesc})
		if got := skus(r); len(got) != 3 || got[0] != "PAN-100W" || got[2] != "PAN-450W" {
			t.Errorf("orden = %v", got)
		}
	})
	t.Run("un producto sin stock queda al final", func(t *testing.T) {
		for _, orden := range []string{ports.OrdenPrecioAsc, ports.OrdenPrecioDesc} {
			got := skus(buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", Orden: orden}))
			if got[len(got)-1] != "PAN-SIN" {
				t.Errorf("%s = %v", orden, got)
			}
		}
	})
	t.Run("paginación", func(t *testing.T) {
		todos := map[string]bool{}
		for pagina := 1; pagina <= 2; pagina++ {
			r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", Orden: ports.OrdenPrecioAsc, Pagina: pagina, Tamanio: 2})
			if r.Total != 4 || r.Pagina != pagina || r.Tamanio != 2 || len(r.Resultados) != 2 {
				t.Errorf("página %d = %+v", pagina, r)
			}
			for _, s := range skus(r) {
				todos[s] = true
			}
		}
		if len(todos) != 4 {
			t.Errorf("las dos páginas juntas = %v", todos)
		}
	})
	t.Run("página que no existe", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", Pagina: 99})
		if len(r.Resultados) != 0 || r.Total != 4 {
			t.Errorf("resultado = %+v", r)
		}
	})
	t.Run("campos del documento", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Q: "PAN-550W"})
		d := r.Resultados[0]
		if d.SKU != "PAN-550W" || d.Categoria != "paneles" || *d.PrecioMin != 200 || d.ProveedoresConStock != 2 || d.StockTotal != 20 || len(d.Proveedores) != 2 {
			t.Errorf("documento = %+v", d)
		}
	})
}

func TestUnFiltroHostilNoAmpliaLosResultados(t *testing.T) {
	c := indice(t)
	hostiles := []string{
		`x" OR categoria="paneles`,
		`x" OR categoria = "paneles" OR categoria = "y`,
		`paneles" OR "1"="1`,
		`\" OR categoria = \"paneles`,
		`x\`,
		`x\\`,
		`\`,
		`"`,
		`""`,
		`' OR categoria = 'paneles`,
		`paneles OR categoria = inversores`,
		`(categoria = paneles)`,
	}
	for _, valor := range hostiles {
		t.Run(valor, func(t *testing.T) {
			for _, q := range []ports.ConsultaProductos{{Categoria: valor}, {Proveedor: valor}} {
				r := buscar(t, c, q)
				if r.Total != 0 {
					t.Errorf("con %+v devolvió %d: %v", q, r.Total, skus(r))
				}
			}
		})
	}
	t.Run("una comilla que sí es parte del valor encuentra lo suyo", func(t *testing.T) {
		r := buscar(t, c, ports.ConsultaProductos{Categoria: `ropa-"jeans"`})
		if r.Total != 1 || r.Resultados[0].SKU != "FAV/var ñ.38" {
			t.Errorf("resultado = %v", skus(r))
		}
	})
}

func TestIndexarReemplazaElDocumentoYEsIdempotente(t *testing.T) {
	c := indice(t)
	ctx := context.Background()
	actualizado := doc("PAN-450W", "Panel solar 450 W monocristalino", "paneles", precio(120), "Andes Solar SRL")
	for range 2 {
		if err := c.Indexar(ctx, []ports.DocumentoProducto{actualizado}); err != nil {
			t.Fatal(err)
		}
	}
	r := buscar(t, c, ports.ConsultaProductos{Categoria: "paneles", Orden: ports.OrdenPrecioAsc})
	if r.Total != 4 || r.Resultados[0].SKU != "PAN-450W" || *r.Resultados[0].PrecioMin != 120 {
		t.Errorf("resultado = %+v", r)
	}
}

func TestReconstruirReemplazaTodoYDejaElIndiceCargado(t *testing.T) {
	c := indice(t)
	ctx := context.Background()

	if err := c.Indexar(ctx, []ports.DocumentoProducto{doc("VIEJO", "Producto que ya no existe", "paneles", precio(1))}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconstruir(ctx, catalogo()); err != nil {
		t.Fatal(err)
	}
	if r := buscar(t, c, ports.ConsultaProductos{Q: "VIEJO"}); r.Total != 0 {
		t.Errorf("sigue el documento viejo: %v", skus(r))
	}
	if r := buscar(t, c, ports.ConsultaProductos{}); r.Total != len(catalogo()) {
		t.Errorf("total = %d", r.Total)
	}
	cargado, err := c.IndiceCargado(ctx)
	if err != nil || !cargado {
		t.Errorf("cargado = %v, err = %v", cargado, err)
	}
}

func TestIndiceQueNoExisteNoEstaCargadoYBuscarFalla(t *testing.T) {
	c := meilisearch.NuevoCliente(urlMeili, claveMaestra, "no_existe")
	cargado, err := c.IndiceCargado(context.Background())
	if err != nil || cargado {
		t.Errorf("cargado = %v, err = %v", cargado, err)
	}
	if _, err := c.Buscar(context.Background(), ports.ConsultaProductos{Pagina: 1, Tamanio: 1}); err == nil {
		t.Error("buscar en un índice inexistente tiene que ser un error (503 en el handler)")
	}
}
