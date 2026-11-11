package sincronizacion

// El stock real de FAV (GET /v1/disponibilidad, su contrato 1.2.0) tiene que
// llegar a la oferta publicada, y su ausencia no tiene que tirar abajo la
// importación: ahí vale el tope fijo por variante (ADR-009).

import (
	"context"
	"errors"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
)

// fuenteConStock es la fuente falsa más la disponibilidad, para que el
// sincronizador la detecte por type assertion.
type fuenteConStock struct {
	*fuenteFalsa
	stock    map[string]int
	err      error
	pedidas  []string
	llamadas int
}

func (f *fuenteConStock) ConsultarDisponibilidad(_ context.Context, variantes []string, _ string) (map[string]int, error) {
	f.llamadas++
	f.pedidas = append(f.pedidas, variantes...)
	if f.err != nil {
		return nil, f.err
	}
	return f.stock, nil
}

func conStock(e *entorno, stock map[string]int, err error) *fuenteConStock {
	f := &fuenteConStock{fuenteFalsa: e.fte, stock: stock, err: err}
	e.s.cfg.Fuente = f
	return f
}

func TestElStockRealDeFAVLlegaALaOfertaPublicada(t *testing.T) {
	e := nuevoEntorno()
	f := conStock(e, map[string]int{"v1": 7, "v2": 0}, nil)

	e.sincronizar(t)
	e.s.PublicarPendientes(context.Background())

	if f.llamadas != 1 {
		t.Fatalf("llamadas a disponibilidad = %d; quiero 1", f.llamadas)
	}
	if len(f.pedidas) != 2 {
		t.Errorf("se pidieron %d variantes; quiero 2", len(f.pedidas))
	}
	d := datosDe(t, e.pub.publicados[len(e.pub.publicados)-1])
	got := map[string]int{}
	for _, o := range d.Ofertas {
		got[o.ProductoSKU] = o.StockDisponible
	}
	if got["FAV-v1"] != 7 {
		t.Errorf("FAV-v1 con stock %d; quiero 7, el real", got["FAV-v1"])
	}
	// Una variante sin stock se publica con 0, no se descarta: la oferta
	// existe y el comparador la deja afuera del ranking por no alcanzar.
	if v, ok := got["FAV-v2"]; !ok || v != 0 {
		t.Errorf("FAV-v2 = (%d, presente=%v); quiero (0, true)", v, ok)
	}
}

// El caso que importa para la resiliencia: FAV puede apagar la operación (su
// README lo avisa) y la importación tiene que seguir igual con el tope fijo.
func TestSiLaDisponibilidadEstaApagadaSeImportaConElTopeFijo(t *testing.T) {
	e := nuevoEntorno()
	conStock(e, nil, fav.ErrDisponibilidadApagada)

	r := e.sincronizar(t)
	e.s.PublicarPendientes(context.Background())

	if r.Estado == "error" {
		t.Fatalf("la importación falló por la disponibilidad: %+v", r)
	}
	tope := dominio.ReglasFAV(0).StockPorVariante
	d := datosDe(t, e.pub.publicados[len(e.pub.publicados)-1])
	for _, o := range d.Ofertas {
		if o.StockDisponible != tope {
			t.Errorf("%s con stock %d; quiero el tope fijo %d", o.ProductoSKU, o.StockDisponible, tope)
		}
	}
}

// Y lo mismo si la consulta falla por una caída, no por estar apagada.
func TestSiLaDisponibilidadFallaSeImportaConElTopeFijo(t *testing.T) {
	e := nuevoEntorno()
	conStock(e, nil, errors.New("timeout"))

	r := e.sincronizar(t)
	e.s.PublicarPendientes(context.Background())

	if r.Estado == "error" {
		t.Fatalf("la importación falló por la disponibilidad: %+v", r)
	}
	tope := dominio.ReglasFAV(0).StockPorVariante
	d := datosDe(t, e.pub.publicados[len(e.pub.publicados)-1])
	if len(d.Ofertas) == 0 {
		t.Fatal("no se publicó ninguna oferta")
	}
	for _, o := range d.Ofertas {
		if o.StockDisponible != tope {
			t.Errorf("%s con stock %d; quiero el tope fijo %d", o.ProductoSKU, o.StockDisponible, tope)
		}
	}
}

// Una fuente que no implementa la disponibilidad (el caso de FAV antes de la
// 1.2.0) tiene que seguir funcionando sin cambios.
func TestFuenteSinDisponibilidadUsaElTopeFijo(t *testing.T) {
	e := nuevoEntorno() // fuenteFalsa, sin ConsultarDisponibilidad
	r := e.sincronizar(t)
	e.s.PublicarPendientes(context.Background())

	if r.Estado == "error" {
		t.Fatalf("la importación falló: %+v", r)
	}
	tope := dominio.ReglasFAV(0).StockPorVariante
	d := datosDe(t, e.pub.publicados[len(e.pub.publicados)-1])
	for _, o := range d.Ofertas {
		if o.StockDisponible != tope {
			t.Errorf("%s con stock %d; quiero el tope fijo %d", o.ProductoSKU, o.StockDisponible, tope)
		}
	}
}
