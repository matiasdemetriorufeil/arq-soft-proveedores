package sincronizacion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/resiliencia"
)

// --- dobles -------------------------------------------------------------------

type almacenMem struct {
	mu   sync.Mutex
	imps []Importacion
}

func (a *almacenMem) UltimaVigente(context.Context) (Importacion, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var mejor *Importacion
	for i := range a.imps {
		if a.imps[i].Vigentes != nil && (mejor == nil || a.imps[i].Secuencia > mejor.Secuencia) {
			mejor = &a.imps[i]
		}
	}
	if mejor == nil {
		return Importacion{}, false, nil
	}
	return *mejor, true, nil
}

func (a *almacenMem) UltimaSincronizacionOK(context.Context) (time.Time, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var t time.Time
	ok := false
	for _, i := range a.imps {
		if (i.Estado == EstadoConCambios || i.Estado == EstadoSinCambios) && i.TerminadaEn.After(t) {
			t, ok = i.TerminadaEn, true
		}
	}
	return t, ok, nil
}

func (a *almacenMem) Guardar(_ context.Context, imp Importacion) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.imps = append(a.imps, imp)
	return nil
}

func (a *almacenMem) Pendientes(_ context.Context, limite int) ([]Importacion, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Importacion
	for _, i := range a.imps {
		if i.Evento != nil && !i.Evento.Publicado {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secuencia < out[j].Secuencia })
	if len(out) > limite {
		out = out[:limite]
	}
	return out, nil
}

func (a *almacenMem) ContarPendientes(ctx context.Context) (int64, error) {
	p, _ := a.Pendientes(ctx, 1<<30)
	return int64(len(p)), nil
}

func (a *almacenMem) MarcarPublicado(_ context.Context, id string, en time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.imps {
		if a.imps[i].ID == id {
			a.imps[i].Evento.Publicado, a.imps[i].Evento.PublicadoEn = true, en
			return nil
		}
	}
	return errors.New("no existe")
}

func (a *almacenMem) Ping(context.Context) error { return nil }

func (a *almacenMem) estados() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, i := range a.imps {
		out = append(out, i.Estado)
	}
	return out
}

type pubFalso struct {
	caido      bool
	publicados []mensajeria.Sobre
}

func (p *pubFalso) Publicar(_ context.Context, clave string, s mensajeria.Sobre) error {
	if p.caido {
		return errors.New("broker caído")
	}
	if clave != mensajeria.ClaveListaPrecios {
		return errors.New("clave equivocada: " + clave)
	}
	p.publicados = append(p.publicados, s)
	return nil
}

func (p *pubFalso) Conectado() bool { return !p.caido }

type fuenteFalsa struct {
	cat fav.Catalogo
	err error
	n   int
}

func (f *fuenteFalsa) ListarProductos(context.Context, string) (fav.Resultado, error) {
	f.n++
	if f.err != nil {
		return fav.Resultado{}, f.err
	}
	crudo, _ := json.Marshal(f.cat)
	return fav.Resultado{Catalogo: f.cat, Crudo: crudo, Intentos: 1}, nil
}

type entorno struct {
	s     *Sincronizador
	alm   *almacenMem
	pub   *pubFalso
	fte   *fuenteFalsa
	ahora time.Time
}

func nuevoEntorno() *entorno {
	e := &entorno{
		alm:   &almacenMem{},
		pub:   &pubFalso{},
		fte:   &fuenteFalsa{cat: catalogo(map[string]float64{"v1": 100, "v2": 200})},
		ahora: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
	}
	reloj := func() time.Time { return e.ahora }
	e.s = Nuevo(Config{
		Proveedor: mensajeria.ProveedorEvento{ID: 11, Codigo: "fav", Nombre: "FAV"},
		Reglas:    dominio.ReglasFAV(0),
		Fuente:    e.fte,
		Breaker:   resiliencia.NuevoBreaker(3, 30*time.Second, reloj),
		Almacen:   e.alm,
		Pub:       e.pub,
		Logger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Ahora:     reloj,
	})
	return e
}

func catalogo(precios map[string]float64) fav.Catalogo {
	p := fav.Producto{ProductoID: "p", Nombre: "Remera", Categoria: "remeras"}
	ids := make([]string, 0, len(precios))
	for id := range precios {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p.Variantes = append(p.Variantes, fav.Variante{VarianteID: id, Talle: "M", Color: "Negro", Precio: precios[id]})
	}
	return fav.Catalogo{Items: []fav.Producto{p}}
}

func (e *entorno) sincronizar(t *testing.T) Resumen {
	t.Helper()
	r, err := e.s.Sincronizar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func datosDe(t *testing.T, s mensajeria.Sobre) mensajeria.ListaPreciosActualizada {
	t.Helper()
	if s.Tipo != mensajeria.TipoListaPreciosActualizada || s.Version != 1 {
		t.Fatalf("sobre tipo %q versión %d", s.Tipo, s.Version)
	}
	var d mensajeria.ListaPreciosActualizada
	if err := json.Unmarshal(s.Datos, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// --- tests --------------------------------------------------------------------

func TestPrimeraSincronizacionPublicaElCatalogoCompleto(t *testing.T) {
	e := nuevoEntorno()
	r := e.sincronizar(t)

	if r.Estado != EstadoConCambios || r.Altas != 2 || r.Publicados != 1 || r.Pendientes != 0 {
		t.Fatalf("resumen = %+v", r)
	}
	d := datosDe(t, e.pub.publicados[0])
	if !d.Completa || d.Secuencia != 1 || d.Motivo != mensajeria.MotivoSincronizacion {
		t.Errorf("datos = %+v", d)
	}
	if len(d.Ofertas) != 2 || len(d.Productos) != 2 || d.Proveedor.ID != 11 {
		t.Errorf("ofertas = %d, productos = %d, proveedor = %+v", len(d.Ofertas), len(d.Productos), d.Proveedor)
	}
	if d.Ofertas[0].ProductoSKU != "FAV-v1" || d.Ofertas[0].StockDisponible != 10 {
		t.Errorf("oferta = %+v", d.Ofertas[0])
	}
	if d.Bajas == nil {
		t.Error("bajas tiene que ir como [] y no como null")
	}
}

func TestSinCambiosNoPublicaNada(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	r := e.sincronizar(t)
	if r.Estado != EstadoSinCambios || r.Publicados != 0 {
		t.Errorf("resumen = %+v", r)
	}
	if len(e.pub.publicados) != 1 {
		t.Errorf("publicó %d eventos, quiero 1", len(e.pub.publicados))
	}
}

func TestUnCambioPublicaSoloLaDiferencia(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	e.fte.cat = catalogo(map[string]float64{"v1": 150, "v3": 300}) // v1 cambia, v2 se va, v3 entra
	r := e.sincronizar(t)

	if r.Altas != 1 || r.Modificadas != 1 || r.Bajas != 1 {
		t.Fatalf("resumen = %+v", r)
	}
	d := datosDe(t, e.pub.publicados[1])
	if d.Completa || d.Secuencia != 2 {
		t.Errorf("completa = %v, secuencia = %d", d.Completa, d.Secuencia)
	}
	if len(d.Ofertas) != 2 || len(d.Bajas) != 1 || d.Bajas[0] != "FAV-v2" {
		t.Errorf("ofertas = %+v, bajas = %v", d.Ofertas, d.Bajas)
	}
	// Solo el alta trae el producto; el cambio de precio no.
	if len(d.Productos) != 1 || d.Productos[0].SKU != "FAV-v3" {
		t.Errorf("productos = %+v", d.Productos)
	}
}

func TestFAVCaidoNoCambiaNadaYAbreElBreaker(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	e.fte.err = fav.ErrNoDisponible
	for range 3 {
		r := e.sincronizar(t)
		if r.Estado != EstadoError || r.Bajas != 0 {
			t.Fatalf("resumen = %+v", r)
		}
	}
	llamadas := e.fte.n
	r := e.sincronizar(t)
	if e.fte.n != llamadas {
		t.Error("con el breaker abierto no tiene que llamar a FAV")
	}
	if r.Error != resiliencia.ErrAbierto.Error() {
		t.Errorf("error = %q, quiero el del breaker", r.Error)
	}
	if len(e.pub.publicados) != 1 {
		t.Errorf("publicó %d eventos: una caída de menos de 24 h no avisa nada", len(e.pub.publicados))
	}
	est, _ := e.s.Estado(context.Background())
	if est.Vencido || est.OfertasVigentes != 2 || est.Breaker != string(resiliencia.Abierto) {
		t.Errorf("estado = %+v", est)
	}
}

func TestDespuesDe24HorasRetiraLasOfertasUnaSolaVez(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	e.fte.err = fav.ErrNoDisponible

	e.ahora = e.ahora.Add(VigenciaMaxima - time.Minute)
	e.sincronizar(t)
	if len(e.pub.publicados) != 1 {
		t.Fatal("retiró las ofertas antes de las 24 h")
	}

	e.ahora = e.ahora.Add(2 * time.Minute)
	r := e.sincronizar(t)
	if r.Bajas != 2 || len(e.pub.publicados) != 2 {
		t.Fatalf("resumen = %+v, publicados = %d", r, len(e.pub.publicados))
	}
	d := datosDe(t, e.pub.publicados[1])
	if d.Motivo != mensajeria.MotivoCatalogoVencido || !d.Completa || len(d.Ofertas) != 0 || len(d.Bajas) != 2 {
		t.Errorf("datos = %+v", d)
	}

	e.ahora = e.ahora.Add(time.Hour)
	e.sincronizar(t)
	if len(e.pub.publicados) != 2 {
		t.Error("repitió el vencimiento")
	}
	est, _ := e.s.Estado(context.Background())
	if !est.Vencido || est.OfertasVigentes != 0 {
		t.Errorf("estado = %+v", est)
	}
}

func TestAlVolverDespuesDeVencerReenviaTodo(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	e.fte.err = fav.ErrNoDisponible
	e.ahora = e.ahora.Add(VigenciaMaxima + time.Minute)
	e.sincronizar(t)

	e.fte.err = nil
	e.ahora = e.ahora.Add(time.Hour) // pasa la espera del breaker
	r := e.sincronizar(t)
	if r.Estado != EstadoConCambios {
		t.Fatalf("resumen = %+v", r)
	}
	d := datosDe(t, e.pub.publicados[len(e.pub.publicados)-1])
	if !d.Completa || len(d.Ofertas) != 2 || d.Secuencia != 3 {
		t.Errorf("datos = %+v", d)
	}
}

func TestBrokerCaidoDejaElEventoEnElOutboxYLoPublicaDespues(t *testing.T) {
	e := nuevoEntorno()
	e.pub.caido = true
	r := e.sincronizar(t)
	if r.Publicados != 0 || r.Pendientes != 1 {
		t.Fatalf("resumen = %+v", r)
	}

	e.fte.cat = catalogo(map[string]float64{"v1": 999, "v2": 200})
	e.sincronizar(t)
	if n, _ := e.alm.ContarPendientes(context.Background()); n != 2 {
		t.Fatalf("pendientes = %d", n)
	}

	e.pub.caido = false
	if n := e.s.PublicarPendientes(context.Background()); n != 2 {
		t.Fatalf("publicó %d", n)
	}
	// En orden: primero la secuencia 1 y después la 2.
	if datosDe(t, e.pub.publicados[0]).Secuencia != 1 || datosDe(t, e.pub.publicados[1]).Secuencia != 2 {
		t.Error("publicó fuera de orden")
	}
}

func TestClaveInvalidaNoAbreElBreaker(t *testing.T) {
	e := nuevoEntorno()
	e.fte.err = fav.ErrNoAutenticado
	for range 5 {
		e.sincronizar(t)
	}
	if e.fte.n != 5 {
		t.Errorf("llamó %d veces; un 401 no es una caída y no tiene que abrir el breaker", e.fte.n)
	}
	for _, est := range e.alm.estados() {
		if est != EstadoError {
			t.Errorf("estado = %s", est)
		}
	}
}

func TestCatalogoVacioDaDeBajaTodoUnaSolaVez(t *testing.T) {
	e := nuevoEntorno()
	e.sincronizar(t)
	e.fte.cat = fav.Catalogo{Items: []fav.Producto{}}

	r := e.sincronizar(t)
	if r.Bajas != 2 || r.Publicados != 1 {
		t.Fatalf("resumen = %+v", r)
	}
	r = e.sincronizar(t)
	if r.Estado != EstadoSinCambios || len(e.pub.publicados) != 2 {
		t.Errorf("resumen = %+v, publicados = %d: las bajas no se tienen que repetir", r, len(e.pub.publicados))
	}
	est, _ := e.s.Estado(context.Background())
	if est.OfertasVigentes != 0 || est.SecuenciaVigente != 2 {
		t.Errorf("estado = %+v", est)
	}
}

// pubContador cuenta cuántas veces se publicó cada evento.
type pubContador struct {
	mu    sync.Mutex
	veces map[string]int
}

func (p *pubContador) Publicar(_ context.Context, _ string, s mensajeria.Sobre) error {
	time.Sleep(time.Millisecond) // agranda la ventana de la carrera
	p.mu.Lock()
	defer p.mu.Unlock()
	p.veces[s.EventoID]++
	return nil
}

func (p *pubContador) Conectado() bool { return true }

func TestPublicarPendientesEnParaleloNoDuplica(t *testing.T) {
	e := nuevoEntorno()
	e.pub.caido = true
	e.sincronizar(t)
	e.fte.cat = catalogo(map[string]float64{"v1": 1, "v2": 2})
	e.sincronizar(t)

	pc := &pubContador{veces: map[string]int{}}
	e.s.cfg.Pub = pc
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); e.s.PublicarPendientes(context.Background()) }()
	}
	wg.Wait()
	if len(pc.veces) != 2 {
		t.Fatalf("se publicaron %d eventos distintos, quiero 2", len(pc.veces))
	}
	for id, n := range pc.veces {
		if n != 1 {
			t.Errorf("el evento %s se publicó %d veces", id, n)
		}
	}
}
