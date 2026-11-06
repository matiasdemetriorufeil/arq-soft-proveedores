//go:build integration

package repository_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// Los SKU de estos tests empiezan con FAV- (si no, OrdenesParaEnviar no los
// ve) y siguen con TEST- para no confundirse con el seed.
const (
	skuPedido     = "FAV-TEST-PEDIDO"
	skuConfirmado = "FAV-TEST-CONFIRMADO"
	skuCarrera    = "FAV-TEST-CARRERA"
)

func ordenEnBorrador(t *testing.T, sku string, cantidad int) *repository.Orden {
	t.Helper()
	prepararSKU(t, sku, 1, 5)
	limpiarOrdenes(t, sku)
	o := nuevaOrden(t, sku, cantidad, service.EstadoBorrador)
	if err := repository.NuevoRepo(poolCompartido).CrearOrden(context.Background(), o); err != nil {
		t.Fatalf("crear la orden: %v", err)
	}
	return o
}

// TestIntentosYClavesDelPedido: la Idempotency-Key es `<id>-<intento>`, se
// mantiene mientras haya un intento en curso y cambia después de un rechazo
// definitivo (ADR-009).
func TestIntentosYClavesDelPedido(t *testing.T) {
	ctx := context.Background()
	repo := repository.NuevoRepo(poolCompartido)
	o := ordenEnBorrador(t, skuPedido, 4)

	uno, err := repo.NuevoIntentoDePedido(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if uno.Intento != 1 || uno.ClavePedido == nil || *uno.ClavePedido != o.ID+"-1" {
		t.Fatalf("intento %d, clave %v; quiero 1 y %s-1", uno.Intento, uno.ClavePedido, o.ID)
	}
	if _, err := repo.NuevoIntentoDePedido(ctx, o.ID); !errors.Is(err, repository.ErrNoEncontrado) {
		t.Errorf("con un intento en curso no se arma otro: err = %v", err)
	}

	// Un fallo reintentable deja la clave; uno definitivo la libera.
	if err := repo.RegistrarFalloDeEnvio(ctx, o.ID, "FAV no respondió", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.OrdenPorID(ctx, o.ID); got.ClavePedido == nil || *got.Motivo != "FAV no respondió" {
		t.Errorf("después de un fallo reintentable: clave %v, motivo %v", got.ClavePedido, got.Motivo)
	}
	if err := repo.RegistrarFalloDeEnvio(ctx, o.ID, "sin stock", true); err != nil {
		t.Fatal(err)
	}
	dos, err := repo.NuevoIntentoDePedido(ctx, o.ID)
	if err != nil || *dos.ClavePedido != o.ID+"-2" {
		t.Fatalf("segundo intento: clave %v, err %v; quiero %s-2", dos.ClavePedido, err, o.ID)
	}

	// La respuesta de un intento viejo no se registra.
	datos := repository.DatosPedido{PedidoID: "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f", Estado: "PENDIENTE_DE_PAGO",
		Total: 120000, PagoVenceEn: time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)}
	if _, err := repo.RegistrarPedido(ctx, o.ID, o.ID+"-1", datos); !errors.Is(err, repository.ErrNoEncontrado) {
		t.Errorf("registrar con la clave vieja: err = %v; quiero ErrNoEncontrado", err)
	}
	sol, err := repo.RegistrarPedido(ctx, o.ID, o.ID+"-2", datos)
	if err != nil {
		t.Fatal(err)
	}
	if sol.Estado != service.EstadoSolicitada || *sol.PedidoID != datos.PedidoID || *sol.PedidoTotal != 120000 ||
		!sol.PagoVenceEn.Equal(datos.PagoVenceEn) || sol.Motivo != nil {
		t.Errorf("orden registrada = %+v", sol)
	}

	porPedido, err := repo.OrdenPorPedidoID(ctx, datos.PedidoID)
	if err != nil || porPedido.ID != o.ID {
		t.Errorf("OrdenPorPedidoID: %v, %v", porPedido.ID, err)
	}
	seg, _ := repo.OrdenesEnSeguimiento(ctx)
	env, _ := repo.OrdenesParaEnviar(ctx)
	if !contiene(seg, o.ID) || contiene(env, o.ID) {
		t.Error("una orden SOLICITADA se sigue y ya no se envía")
	}
	limpiarOrdenes(t, skuPedido)
}

func contiene(ordenes []repository.Orden, id string) bool {
	for _, o := range ordenes {
		if o.ID == id {
			return true
		}
	}
	return false
}

// TestAvisoRepetidoAlMismoTiempo: 20 entregas simultáneas del mismo aviso, y
// la PRIMARY KEY deja pasar una sola.
func TestAvisoRepetidoAlMismoTiempo(t *testing.T) {
	ctx := context.Background()
	repo := repository.NuevoRepo(poolCompartido)
	evento, _ := nuevoUUIDDePrueba()

	var wg sync.WaitGroup
	var mu sync.Mutex
	nuevos, errores := 0, 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nuevo, err := repo.RegistrarAviso(ctx, evento, "pedido-x", "PAGADO")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errores++
			} else if nuevo {
				nuevos++
			}
		}()
	}
	wg.Wait()
	if nuevos != 1 || errores != 0 {
		t.Errorf("%d nuevos y %d errores; quiero 1 nuevo y 0 errores", nuevos, errores)
	}
}

// TestConfirmarSumaElStockEnLaMismaTransaccion: si algo falla después de
// mover la orden, ni la orden ni el stock cambian.
func TestConfirmarSumaElStockEnLaMismaTransaccion(t *testing.T) {
	ctx := context.Background()
	repo := repository.NuevoRepo(poolCompartido)
	o := ordenEnBorrador(t, skuConfirmado, 4)
	if _, err := poolCompartido.Exec(ctx, `UPDATE ordenes_reposicion SET estado = 'SOLICITADA', pedido_id = $2 WHERE id = $1`,
		o.ID, "pedido-"+o.ID); err != nil {
		t.Fatal(err)
	}

	falla := errors.New("falla después de mover la orden")
	err := repo.EnTransaccion(ctx, func(tx repository.Tx) error {
		if _, err := repo.AvanzarOrden(ctx, tx, o.ID, service.EstadoSolicitada, service.EstadoConfirmada, "PAGADO"); err != nil {
			return err
		}
		if err := repo.SumarStock(ctx, tx, skuConfirmado, 4); err != nil {
			return err
		}
		return falla
	})
	if !errors.Is(err, falla) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := repo.OrdenPorID(ctx, o.ID); got.Estado != service.EstadoSolicitada {
		t.Errorf("la orden quedó en %s; el rollback la tenía que dejar SOLICITADA", got.Estado)
	}
	if st, _ := repo.StockPorSKU(ctx, skuConfirmado); st.Disponible != 1 {
		t.Errorf("disponible = %d; el rollback lo tenía que dejar en 1", st.Disponible)
	}

	err = repo.EnTransaccion(ctx, func(tx repository.Tx) error {
		if _, err := repo.AvanzarOrden(ctx, tx, o.ID, service.EstadoSolicitada, service.EstadoConfirmada, "PAGADO"); err != nil {
			return err
		}
		return repo.SumarStock(ctx, tx, skuConfirmado, 4)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := repo.OrdenPorID(ctx, o.ID)
	st, _ := repo.StockPorSKU(ctx, skuConfirmado)
	if got.Estado != service.EstadoConfirmada || *got.PedidoEstado != "PAGADO" || st.Disponible != 5 {
		t.Errorf("orden %s (%v), disponible %d; quiero CONFIRMADA (PAGADO) y 5", got.Estado, got.PedidoEstado, st.Disponible)
	}
	limpiarOrdenes(t, skuConfirmado)
}

// favPagado es un FAV que responde todo PAGADO.
type favPagado struct{}

func (favPagado) CrearPedido(context.Context, string, fav.PedidoNuevo) (fav.Pedido, error) {
	return fav.Pedido{}, errors.New("no se usa")
}
func (favPagado) ConsultarPedido(_ context.Context, id string) (fav.Pedido, error) {
	return fav.Pedido{PedidoID: id, Estado: "PAGADO"}, nil
}

// TestElAvisoYLaConsultaDeRespaldoALaVezSumanUnaSolaVez: el webhook y la
// consulta de respaldo pueden confirmar la misma orden en paralelo. El UPDATE
// condicionado al estado deja ganar a uno solo: el stock entra una vez.
func TestElAvisoYLaConsultaDeRespaldoALaVezSumanUnaSolaVez(t *testing.T) {
	ctx := context.Background()
	repo := repository.NuevoRepo(poolCompartido)
	o := ordenEnBorrador(t, skuCarrera, 8)
	pedido := "pedido-" + o.ID
	if _, err := poolCompartido.Exec(ctx, `UPDATE ordenes_reposicion SET estado = 'SOLICITADA', pedido_id = $2 WHERE id = $1`,
		o.ID, pedido); err != nil {
		t.Fatal(err)
	}
	svc := service.NuevoServicioPedidos(repo, favPagado{}, "")

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var a fav.Aviso
			a.EventoID, _ = nuevoUUIDDePrueba()
			a.Pedido.PedidoID, a.Pedido.Estado = pedido, "PAGADO"
			if i%2 == 0 {
				svc.ProcesarAviso(ctx, a)
			} else {
				svc.Seguimiento(ctx)
			}
		}()
	}
	wg.Wait()
	st, _ := repo.StockPorSKU(ctx, skuCarrera)
	if got, _ := repo.OrdenPorID(ctx, o.ID); got.Estado != service.EstadoConfirmada || st.Disponible != 1+8 {
		t.Errorf("orden %s, disponible %d; quiero CONFIRMADA y 9 (las 8 unidades entran una sola vez)", got.Estado, st.Disponible)
	}
	limpiarOrdenes(t, skuCarrera)
}
