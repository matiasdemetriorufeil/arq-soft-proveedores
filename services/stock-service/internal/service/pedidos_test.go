package service_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// repoPedidosFalso guarda las órdenes, el stock y los avisos en memoria. Las
// garantías del esquema (PRIMARY KEY de los avisos, UPDATE condicionado) las
// prueban los tests de integración de internal/repository.
type repoPedidosFalso struct {
	ordenes map[string]*repository.Orden
	stock   map[string]int
	avisos  map[string]bool
	// errRegistrarAviso simula la base caída al recibir un webhook.
	errRegistrarAviso error
}

func nuevoRepoPedidos(ordenes ...repository.Orden) *repoPedidosFalso {
	r := &repoPedidosFalso{ordenes: map[string]*repository.Orden{}, stock: map[string]int{}, avisos: map[string]bool{}}
	for i := range ordenes {
		o := ordenes[i]
		r.ordenes[o.ID] = &o
	}
	return r
}

func (r *repoPedidosFalso) OrdenPorID(_ context.Context, id string) (repository.Orden, error) {
	o, ok := r.ordenes[id]
	if !ok {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	return *o, nil
}

func (r *repoPedidosFalso) NuevoIntentoDePedido(_ context.Context, id string) (repository.Orden, error) {
	o, ok := r.ordenes[id]
	if !ok || o.Estado != service.EstadoBorrador || o.ClavePedido != nil {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	o.Intento++
	clave := fmt.Sprintf("%s-%d", o.ID, o.Intento)
	o.ClavePedido = &clave
	return *o, nil
}

func (r *repoPedidosFalso) RegistrarPedido(_ context.Context, id, clave string, p repository.DatosPedido) (repository.Orden, error) {
	o, ok := r.ordenes[id]
	if !ok || o.Estado != service.EstadoBorrador || o.ClavePedido == nil || *o.ClavePedido != clave {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	o.Estado, o.PedidoID, o.PedidoEstado, o.PedidoTotal, o.PagoVenceEn, o.Motivo =
		service.EstadoSolicitada, &p.PedidoID, &p.Estado, &p.Total, &p.PagoVenceEn, nil
	return *o, nil
}

func (r *repoPedidosFalso) RegistrarFalloDeEnvio(_ context.Context, id, motivo string, liberar bool) error {
	o := r.ordenes[id]
	o.Motivo = &motivo
	if liberar {
		o.ClavePedido = nil
	}
	return nil
}

func (r *repoPedidosFalso) filtrar(f func(*repository.Orden) bool) []repository.Orden {
	var out []repository.Orden
	for _, o := range r.ordenes {
		if f(o) {
			out = append(out, *o)
		}
	}
	return out
}

func (r *repoPedidosFalso) OrdenesParaEnviar(context.Context) ([]repository.Orden, error) {
	return r.filtrar(func(o *repository.Orden) bool {
		return o.Estado == service.EstadoBorrador && strings.HasPrefix(o.SKU, "FAV-")
	}), nil
}

func (r *repoPedidosFalso) OrdenesEnSeguimiento(context.Context) ([]repository.Orden, error) {
	return r.filtrar(func(o *repository.Orden) bool {
		return o.PedidoID != nil && (o.Estado == service.EstadoSolicitada || o.Estado == service.EstadoVencida)
	}), nil
}

func (r *repoPedidosFalso) OrdenPorPedidoID(_ context.Context, pid string) (repository.Orden, error) {
	for _, o := range r.ordenes {
		if o.PedidoID != nil && *o.PedidoID == pid {
			return *o, nil
		}
	}
	return repository.Orden{}, repository.ErrNoEncontrado
}

func (r *repoPedidosFalso) RegistrarAviso(_ context.Context, eventoID, _, _ string) (bool, error) {
	if r.errRegistrarAviso != nil {
		return false, r.errRegistrarAviso
	}
	if r.avisos[eventoID] {
		return false, nil
	}
	r.avisos[eventoID] = true
	return true, nil
}

func (r *repoPedidosFalso) ActualizarEstadoDelPedido(_ context.Context, id, estado string) error {
	r.ordenes[id].PedidoEstado = &estado
	return nil
}

func (r *repoPedidosFalso) EnTransaccion(_ context.Context, fn func(repository.Tx) error) error {
	// Sin base no hay rollback: alcanza con que el error se propague.
	return fn(nil)
}

func (r *repoPedidosFalso) AvanzarOrden(_ context.Context, _ repository.Tx, id, desde, hacia, estadoFAV string) (repository.Orden, error) {
	o, ok := r.ordenes[id]
	if !ok || o.Estado != desde {
		return repository.Orden{}, repository.ErrNoEncontrado
	}
	o.Estado, o.PedidoEstado = hacia, &estadoFAV
	return *o, nil
}

func (r *repoPedidosFalso) SumarStock(_ context.Context, _ repository.Tx, sku string, n int) error {
	r.stock[sku] += n
	return nil
}

// favFalso es un FAV que guarda estado: lo que el mock de FAV no hace
// (ADR-009 pide probar los caminos de estado con un doble propio).
type favFalso struct {
	errCrear   []error // un error por llamada, en orden; nil = 201
	claves     []string
	pedidos    []fav.PedidoNuevo
	estados    map[string]string // estado vigente por pedidoId
	errConsult error
	consultas  int
}

func (f *favFalso) CrearPedido(_ context.Context, clave string, p fav.PedidoNuevo) (fav.Pedido, error) {
	f.claves = append(f.claves, clave)
	f.pedidos = append(f.pedidos, p)
	if n := len(f.claves) - 1; n < len(f.errCrear) && f.errCrear[n] != nil {
		return fav.Pedido{}, f.errCrear[n]
	}
	if f.estados == nil {
		f.estados = map[string]string{}
	}
	// Mismo pedido para la misma clave, como la idempotencia de FAV.
	id := "pedido-" + clave
	f.estados[id] = service.PedidoPendienteDePago
	out := fav.Pedido{PedidoID: id, Estado: service.PedidoPendienteDePago, Total: 45000, Moneda: "ARS"}
	out.Pago.VenceEn = time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	return out, nil
}

func (f *favFalso) ConsultarPedido(_ context.Context, id string) (fav.Pedido, error) {
	f.consultas++
	if f.errConsult != nil {
		return fav.Pedido{}, f.errConsult
	}
	return fav.Pedido{PedidoID: id, Estado: f.estados[id]}, nil
}

func ordenFAV(id string) repository.Orden {
	return repository.Orden{ID: id, SKU: "FAV-var-remera-basica-blanca-m", Cantidad: 8, Estado: service.EstadoBorrador}
}

// sincronico corre el procesamiento del aviso en el momento, para el test.
func sincronico(s *service.ServicioPedidos) *service.ServicioPedidos {
	service.ProcesarAvisosEnElMomento(s)
	return s
}

const urlAvisos = "http://stock-service:8080/webhooks/fav"

func TestEnviarOrdenHaceElPedidoYPasaASolicitada(t *testing.T) {
	repo := nuevoRepoPedidos(ordenFAV("o1"))
	f := &favFalso{}
	o, err := service.NuevoServicioPedidos(repo, f, urlAvisos).EnviarOrden(context.Background(), "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Estado != service.EstadoSolicitada || o.PedidoID == nil || *o.PedidoID != "pedido-o1-1" || *o.PedidoTotal != 45000 {
		t.Errorf("orden = %+v", o)
	}
	p := f.pedidos[0]
	if f.claves[0] != "o1-1" || p.ReferenciaExterna != "o1" || p.NotificacionURL != urlAvisos ||
		len(p.Items) != 1 || p.Items[0] != (fav.ItemPedido{VarianteID: "var-remera-basica-blanca-m", Cantidad: 8}) {
		t.Errorf("clave %q, pedido %+v; quiero o1-1, referencia o1, la variante sin el prefijo FAV- y 8 unidades", f.claves[0], p)
	}
}

func TestEnviarOrdenFAVCaidoReintentaConLaMismaClave(t *testing.T) {
	repo := nuevoRepoPedidos(ordenFAV("o1"))
	f := &favFalso{errCrear: []error{fav.ErrNoDisponible, nil}}
	svc := service.NuevoServicioPedidos(repo, f, urlAvisos)

	o, err := svc.EnviarOrden(context.Background(), "o1")
	var fallo *service.EnvioFallidoError
	if !errors.As(err, &fallo) || !fallo.Reintentable || o.Estado != service.EstadoBorrador || o.Motivo == nil {
		t.Fatalf("err = %v, orden = %+v; quiero un envío fallido reintentable y la orden en BORRADOR con motivo", err, o)
	}
	if o, err = svc.EnviarOrden(context.Background(), "o1"); err != nil || o.Estado != service.EstadoSolicitada {
		t.Fatalf("segundo envío: err = %v, estado %s", err, o.Estado)
	}
	if f.claves[0] != "o1-1" || f.claves[1] != "o1-1" {
		t.Errorf("claves = %v; el reintento de un timeout tiene que usar la MISMA clave", f.claves)
	}
	if o.Motivo != nil {
		t.Error("al quedar SOLICITADA el motivo del fallo anterior tiene que borrarse")
	}
}

func TestEnviarOrdenRechazadaUsaUnaClaveNuevaEnElIntentoSiguiente(t *testing.T) {
	repo := nuevoRepoPedidos(ordenFAV("o1"))
	f := &favFalso{errCrear: []error{fmt.Errorf("pedido: %w", fav.ErrStockInsuficiente), nil}}
	svc := service.NuevoServicioPedidos(repo, f, urlAvisos)

	_, err := svc.EnviarOrden(context.Background(), "o1")
	var fallo *service.EnvioFallidoError
	if !errors.As(err, &fallo) || fallo.Reintentable || !strings.Contains(fallo.Motivo, "stock") {
		t.Fatalf("err = %v; quiero un rechazo definitivo por stock", err)
	}
	if _, err := svc.EnviarOrden(context.Background(), "o1"); err != nil {
		t.Fatal(err)
	}
	if f.claves[0] != "o1-1" || f.claves[1] != "o1-2" {
		t.Errorf("claves = %v; después de un 4xx el intento siguiente usa una clave nueva", f.claves)
	}
}

func TestEnviarOrdenRechazaLoQueNoCorresponde(t *testing.T) {
	solar := repository.Orden{ID: "o2", SKU: "PROT-DC-1000V", Cantidad: 16, Estado: service.EstadoBorrador}
	ya := ordenFAV("o3")
	ya.Estado = service.EstadoSolicitada
	grande := ordenFAV("o4")
	grande.Cantidad = 11
	repo := nuevoRepoPedidos(solar, ya, grande)
	f := &favFalso{}
	svc := service.NuevoServicioPedidos(repo, f, urlAvisos)

	if _, err := svc.EnviarOrden(context.Background(), "o2"); !errors.Is(err, service.ErrOrdenNoEsDeFAV) {
		t.Errorf("SKU solar: err = %v; quiero ErrOrdenNoEsDeFAV", err)
	}
	if _, err := svc.EnviarOrden(context.Background(), "o3"); !errors.Is(err, service.ErrTransicionInvalida) {
		t.Errorf("orden ya SOLICITADA: err = %v; quiero ErrTransicionInvalida", err)
	}
	if _, err := svc.EnviarOrden(context.Background(), "o4"); !errors.Is(err, service.ErrParametroInvalido) {
		t.Errorf("11 unidades: err = %v; FAV acepta hasta 10", err)
	}
	if _, err := svc.EnviarOrden(context.Background(), "no-existe"); !errors.Is(err, service.ErrOrdenNoEncontrada) {
		t.Errorf("err = %v; quiero ErrOrdenNoEncontrada", err)
	}
	if len(f.claves) != 0 {
		t.Errorf("se llamó a FAV %d veces; ninguno de estos casos tiene que salir", len(f.claves))
	}
}

func TestEnviarPendientesSoloLasDeFAVEnBorrador(t *testing.T) {
	solar := repository.Orden{ID: "o2", SKU: "PROT-DC-1000V", Cantidad: 16, Estado: service.EstadoBorrador}
	repo := nuevoRepoPedidos(ordenFAV("o1"), solar)
	f := &favFalso{}
	res, err := service.NuevoServicioPedidos(repo, f, urlAvisos).EnviarPendientes(context.Background())
	if err != nil || res.Enviadas != 1 || res.Fallidas != 0 || len(f.claves) != 1 {
		t.Errorf("res = %+v, err = %v, llamadas %d; quiero 1 enviada", res, err, len(f.claves))
	}
}

// enviada deja una orden SOLICITADA con su pedido en FAV.
func enviada(t *testing.T, f *favFalso) (*repoPedidosFalso, *service.ServicioPedidos, string) {
	t.Helper()
	repo := nuevoRepoPedidos(ordenFAV("o1"))
	repo.stock["FAV-var-remera-basica-blanca-m"] = 2
	svc := sincronico(service.NuevoServicioPedidos(repo, f, urlAvisos))
	o, err := svc.EnviarOrden(context.Background(), "o1")
	if err != nil {
		t.Fatal(err)
	}
	return repo, svc, *o.PedidoID
}

func aviso(eventoID, pedidoID, estado string) fav.Aviso {
	var a fav.Aviso
	a.EventoID, a.Tipo = eventoID, "pedido.actualizado"
	a.Pedido.PedidoID, a.Pedido.Estado = pedidoID, estado
	return a
}

func TestAvisoPagadoConfirmaLaOrdenYSumaElStock(t *testing.T) {
	f := &favFalso{}
	repo, svc, pid := enviada(t, f)
	f.estados[pid] = service.PedidoPagado

	var procesado error = errors.New("no se procesó")
	nuevo, err := svc.RecibirAviso(context.Background(), aviso("ev-1", pid, service.PedidoPagado), func(err error) { procesado = err })
	if err != nil || !nuevo || procesado != nil {
		t.Fatalf("nuevo = %v, err = %v, procesado = %v", nuevo, err, procesado)
	}
	o := repo.ordenes["o1"]
	if o.Estado != service.EstadoConfirmada || *o.PedidoEstado != service.PedidoPagado {
		t.Errorf("orden en %s (%s); quiero CONFIRMADA", o.Estado, *o.PedidoEstado)
	}
	if repo.stock["FAV-var-remera-basica-blanca-m"] != 2+8 {
		t.Errorf("stock = %d; quiero 10 (2 + las 8 que llegaron)", repo.stock["FAV-var-remera-basica-blanca-m"])
	}
	if f.consultas != 1 {
		t.Errorf("consultas a FAV = %d; el aviso se confirma con GET antes de mover la orden", f.consultas)
	}

	// El mismo aviso otra vez (FAV reintenta): no se procesa de nuevo.
	nuevo, err = svc.RecibirAviso(context.Background(), aviso("ev-1", pid, service.PedidoPagado), nil)
	if err != nil || nuevo || repo.stock["FAV-var-remera-basica-blanca-m"] != 10 || f.consultas != 1 {
		t.Errorf("aviso repetido: nuevo = %v, stock %d, consultas %d", nuevo, repo.stock["FAV-var-remera-basica-blanca-m"], f.consultas)
	}
}

func TestElEstadoVigenteEsElDeLaConsultaNoElDelAviso(t *testing.T) {
	f := &favFalso{}
	repo, svc, pid := enviada(t, f)
	// El aviso dice PAGADO, pero FAV todavía lo tiene pendiente.
	svc.RecibirAviso(context.Background(), aviso("ev-1", pid, service.PedidoPagado), nil)
	if o := repo.ordenes["o1"]; o.Estado != service.EstadoSolicitada {
		t.Errorf("orden en %s; si la consulta dice PENDIENTE_DE_PAGO no se confirma", o.Estado)
	}
}

func TestCaminosDeEstadoDeADR009(t *testing.T) {
	casos := []struct {
		nombre  string
		estados []string // estados sucesivos en FAV
		quiero  string
		stock   int
	}{
		{"vencido", []string{service.PedidoVencido}, service.EstadoVencida, 2},
		{"pago tarde", []string{service.PedidoVencido, service.PedidoAVerificar}, service.EstadoEnRevision, 2},
		{"estado desconocido", []string{"EN_DISPUTA"}, service.EstadoEnRevision, 2},
		{"aviso viejo después de pagado", []string{service.PedidoPagado, service.PedidoVencido}, service.EstadoConfirmada, 10},
		{"sigue pendiente", []string{service.PedidoPendienteDePago}, service.EstadoSolicitada, 2},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			f := &favFalso{}
			repo, svc, pid := enviada(t, f)
			for i, e := range c.estados {
				f.estados[pid] = e
				if _, err := svc.RecibirAviso(context.Background(), aviso(fmt.Sprintf("ev-%d", i), pid, e), nil); err != nil {
					t.Fatal(err)
				}
			}
			if o := repo.ordenes["o1"]; o.Estado != c.quiero {
				t.Errorf("orden en %s; quiero %s", o.Estado, c.quiero)
			}
			if got := repo.stock["FAV-var-remera-basica-blanca-m"]; got != c.stock {
				t.Errorf("stock = %d; quiero %d", got, c.stock)
			}
		})
	}
}

func TestSeguimientoRecuperaUnAvisoPerdido(t *testing.T) {
	f := &favFalso{}
	repo, svc, pid := enviada(t, f)
	f.estados[pid] = service.PedidoPagado // el aviso nunca llegó
	res, err := svc.Seguimiento(context.Background())
	if err != nil || res.Seguidas != 1 || res.Cambiadas != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if o := repo.ordenes["o1"]; o.Estado != service.EstadoConfirmada {
		t.Errorf("orden en %s; quiero CONFIRMADA", o.Estado)
	}
	// Una orden confirmada ya no se sigue.
	if res, _ := svc.Seguimiento(context.Background()); res.Seguidas != 0 {
		t.Errorf("se siguieron %d órdenes; una CONFIRMADA no cambia más", res.Seguidas)
	}
}

func TestSeguimientoConFAVCaidoNoMueveNada(t *testing.T) {
	f := &favFalso{}
	repo, svc, _ := enviada(t, f)
	f.errConsult = fav.ErrNoDisponible
	res, err := svc.Seguimiento(context.Background())
	if err != nil || len(res.Problemas) != 1 || repo.ordenes["o1"].Estado != service.EstadoSolicitada {
		t.Errorf("res = %+v, err = %v, estado %s", res, err, repo.ordenes["o1"].Estado)
	}
}

func TestAvisoConLaBaseCaida(t *testing.T) {
	f := &favFalso{}
	repo, svc, pid := enviada(t, f)
	repo.errRegistrarAviso = errors.New("la base no responde")
	if _, err := svc.RecibirAviso(context.Background(), aviso("ev-1", pid, service.PedidoPagado), nil); err == nil {
		t.Error("si no se pudo registrar el aviso tiene que devolver error, para que FAV lo reintente")
	}
}

func TestAvisoDeUnPedidoQueNoEsNuestro(t *testing.T) {
	f := &favFalso{}
	_, svc, _ := enviada(t, f)
	var procesado error
	svc.RecibirAviso(context.Background(), aviso("ev-1", "pedido-ajeno", service.PedidoPagado), func(err error) { procesado = err })
	if !errors.Is(procesado, service.ErrOrdenNoEncontrada) {
		t.Errorf("procesado = %v; quiero ErrOrdenNoEncontrada", procesado)
	}
}
