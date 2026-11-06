package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

type buscadorFalso struct {
	res    ports.ResultadoProductos
	err    error
	ultima ports.ConsultaProductos
	llamo  bool
}

func (b *buscadorFalso) Buscar(_ context.Context, c ports.ConsultaProductos) (ports.ResultadoProductos, error) {
	b.ultima, b.llamo = c, true
	return b.res, b.err
}

func servidorDeBusqueda(b ports.BuscadorProductos) http.Handler {
	repo := repoDemo()
	return NuevoHandler(Config{
		Repo: repo, Salud: repo, Buscador: b, APIKeys: []string{keyValida},
		Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	})
}

const rutaBuscar = "/api/v1/productos/buscar"

func TestBuscarDevuelveLaPaginaConTotal(t *testing.T) {
	precio := 10.5
	b := &buscadorFalso{res: ports.ResultadoProductos{
		Resultados: []ports.DocumentoProducto{{SKU: "A", Nombre: "Buzo", PrecioMin: &precio, Proveedores: []string{"FAV"}}},
		Total:      21, Pagina: 2, Tamanio: 5,
	}}
	rec := hacer(servidorDeBusqueda(b), rutaBuscar+"?q=buzo&categoria=ropa&proveedor=FAV&precio_min=5&precio_max=100.5&con_stock=true&orden=precio_desc&pagina=2&tamanio=5", keyValida)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var cuerpo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cuerpo); err != nil {
		t.Fatal(err)
	}
	if cuerpo["total"] != 21.0 || cuerpo["pagina"] != 2.0 || cuerpo["tamanio"] != 5.0 {
		t.Errorf("cuerpo = %v", cuerpo)
	}
	if cuerpo["resultados"].([]any)[0].(map[string]any)["sku"] != "A" {
		t.Errorf("resultados = %v", cuerpo["resultados"])
	}
	c := b.ultima
	if c.Q != "buzo" || c.Categoria != "ropa" || c.Proveedor != "FAV" || *c.PrecioMin != 5 || *c.PrecioMax != 100.5 ||
		!c.ConStock || c.Orden != ports.OrdenPrecioDesc || c.Pagina != 2 || c.Tamanio != 5 {
		t.Errorf("consulta = %+v", c)
	}
}

func TestBuscarDefaults(t *testing.T) {
	b := &buscadorFalso{}
	if rec := hacer(servidorDeBusqueda(b), rutaBuscar, keyValida); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	c := b.ultima
	if c.Pagina != 1 || c.Tamanio != 20 || c.Orden != "" || c.ConStock || c.PrecioMin != nil || c.PrecioMax != nil {
		t.Errorf("consulta = %+v", c)
	}
}

func TestBuscarAceptaLosLimites(t *testing.T) {
	for _, q := range []string{"?tamanio=50", "?precio_min=0", "?con_stock=false", "?orden=precio_asc", "?orden=precio_desc"} {
		if rec := hacer(servidorDeBusqueda(&buscadorFalso{}), rutaBuscar+q, keyValida); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d", q, rec.Code)
		}
	}
}

func TestBuscarParametrosInvalidos(t *testing.T) {
	casos := []string{
		"?tamanio=51", "?tamanio=0", "?tamanio=abc", "?tamanio=-1",
		"?pagina=0", "?pagina=-1", "?pagina=1.5", "?pagina=99999999999999999999",
		"?orden=foo", "?orden=PRECIO_ASC", "?orden=relevancia",
		"?precio_min=abc", "?precio_min=-1", "?precio_min=NaN", "?precio_min=Inf",
		"?precio_max=abc", "?precio_max=-0.5", "?precio_max=1e999",
		"?con_stock=si", "?con_stock=1",
	}
	for _, q := range casos {
		t.Run(q, func(t *testing.T) {
			b := &buscadorFalso{}
			rec := hacer(servidorDeBusqueda(b), rutaBuscar+q, keyValida)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", rec.Code)
			}
			if rec.Header().Get("Content-Type") != contentTypeProblem {
				t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != CodigoParametroInvalido {
				t.Errorf("problema = %+v, err = %v", p, err)
			}
			if b.llamo {
				t.Error("no tenía que llegar al motor")
			}
		})
	}
}

func TestBuscarExigeAPIKey(t *testing.T) {
	b := &buscadorFalso{}
	for _, key := range []string{"", "otra-clave"} {
		rec := hacer(servidorDeBusqueda(b), rutaBuscar+"?q=x", key)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("clave %q: status = %d", key, rec.Code)
		}
	}
	if b.llamo {
		t.Error("no tenía que llegar al motor")
	}
}

func TestBuscarConElMotorCaidoEs503YLaCapacidadSigue(t *testing.T) {
	for nombre, b := range map[string]ports.BuscadorProductos{
		"motor caído":    &buscadorFalso{err: errors.New("meilisearch no responde")},
		"sin configurar": nil,
	} {
		t.Run(nombre, func(t *testing.T) {
			h := servidorDeBusqueda(b)
			rec := hacer(h, rutaBuscar+"?q=x", keyValida)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d", rec.Code)
			}
			var p Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != CodigoNoDisponible {
				t.Errorf("problema = %+v, err = %v", p, err)
			}
			if rec := hacer(h, "/api/v1/proveedores/recomendados?producto=PAN-1", keyValida); rec.Code != http.StatusOK {
				t.Errorf("capacidad: status = %d", rec.Code)
			}
		})
	}
}

// El valor hostil llega tal cual al adaptador, que es el que lo escapa; acá se
// comprueba que el handler no lo descarta ni lo interpreta.
func TestBuscarPasaElFiltroHostilComoValor(t *testing.T) {
	b := &buscadorFalso{}
	rec := hacer(servidorDeBusqueda(b), rutaBuscar+`?categoria=x%22%20OR%20categoria%3D%22y`, keyValida)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if b.ultima.Categoria != `x" OR categoria="y` {
		t.Errorf("categoría = %q", b.ultima.Categoria)
	}
}
