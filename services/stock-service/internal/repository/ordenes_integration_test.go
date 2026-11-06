//go:build integration

package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// nuevoUUIDDePrueba genera el id de la orden como lo hace la capa de servicio:
// la columna es UUID y el id se decide antes de tocar la base.
func nuevoUUIDDePrueba() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// Cada test usa su propio SKU: la base es compartida.
const (
	skuBajoMinimo = "TEST-BAJO-MINIMO"
	skuUnaAbierta = "TEST-UNA-ABIERTA"
	skuHistorico  = "TEST-HISTORICO"
)

// limpiarOrdenes borra las órdenes del SKU para que el test arranque de un
// estado conocido; el índice único parcial no perdona una sobra de otra corrida.
func limpiarOrdenes(t *testing.T, sku string) {
	t.Helper()
	if _, err := poolCompartido.Exec(context.Background(),
		`DELETE FROM ordenes_reposicion WHERE sku = $1`, sku); err != nil {
		t.Fatalf("limpiar órdenes de %s: %v", sku, err)
	}
}

func nuevaOrden(t *testing.T, sku string, cantidad int, estado string) *repository.Orden {
	t.Helper()
	id, err := nuevoUUIDDePrueba()
	if err != nil {
		t.Fatalf("generar el id de la orden: %v", err)
	}
	return &repository.Orden{ID: id, SKU: sku, Cantidad: cantidad, Estado: estado}
}

// TestSKUsBajoMinimoContraElSeed: el seed de 002_seed.sql deja a propósito
// PROT-DC-1000V con 4 unidades y mínimo 10 (RN-14).
func TestSKUsBajoMinimoContraElSeed(t *testing.T) {
	prepararSKU(t, skuBajoMinimo, 1, 50)   // bajo el mínimo
	prepararSKU(t, "TEST-SOBRADO", 500, 5) // por encima
	repo := repository.NuevoRepo(poolCompartido)

	bajos, err := repo.SKUsBajoMinimo(context.Background())
	if err != nil {
		t.Fatalf("SKUsBajoMinimo: %v", err)
	}

	porSKU := map[string]repository.Stock{}
	for _, s := range bajos {
		porSKU[s.SKU] = s
	}
	if _, ok := porSKU["PROT-DC-1000V"]; !ok {
		t.Error("PROT-DC-1000V tendría que estar bajo el mínimo: el seed lo deja en 4 con mínimo 10")
	}
	if _, ok := porSKU[skuBajoMinimo]; !ok {
		t.Errorf("%s tendría que estar bajo el mínimo (1 < 50)", skuBajoMinimo)
	}
	if _, ok := porSKU["TEST-SOBRADO"]; ok {
		t.Error("TEST-SOBRADO está por encima del mínimo y no tendría que aparecer")
	}
	for _, s := range bajos {
		if s.Disponible >= s.Minimo {
			t.Errorf("%s: disponible %d >= minimo %d; no es un faltante", s.SKU, s.Disponible, s.Minimo)
		}
	}
}

// TestUnaSolaOrdenAbiertaPorSKU es la evidencia de CA-14.2 en la base: la
// invariante la garantiza el índice único parcial, no la lógica. Es también la
// carrera entre dos réplicas corriendo el ciclo a la vez.
func TestUnaSolaOrdenAbiertaPorSKU(t *testing.T) {
	prepararSKU(t, skuUnaAbierta, 2, 10)
	limpiarOrdenes(t, skuUnaAbierta)
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	primera := nuevaOrden(t, skuUnaAbierta, 18, service.EstadoBorrador)
	if err := repo.CrearOrden(ctx, primera); err != nil {
		t.Fatalf("crear la primera orden: %v", err)
	}
	if primera.CreadoEn.IsZero() || primera.ActualizadoEn.IsZero() {
		t.Error("CrearOrden tendría que devolver creado_en y actualizado_en")
	}

	// La segunda choca con ux_ordenes_abierta_por_sku.
	segunda := nuevaOrden(t, skuUnaAbierta, 5, service.EstadoBorrador)
	if err := repo.CrearOrden(ctx, segunda); !errors.Is(err, repository.ErrOrdenAbiertaExistente) {
		t.Fatalf("crear la segunda orden: error = %v; se esperaba ErrOrdenAbiertaExistente", err)
	}

	abierta, err := repo.OrdenAbiertaPorSKU(ctx, skuUnaAbierta)
	if err != nil || !abierta {
		t.Errorf("OrdenAbiertaPorSKU = %v, %v; se esperaba true", abierta, err)
	}

	// VENCIDA y EN_REVISION siguen contando como abiertas (ADR-009): tampoco
	// dejan crear otra.
	for _, estado := range []string{service.EstadoSolicitada, service.EstadoVencida, service.EstadoEnRevision} {
		if _, err := repo.ActualizarEstadoOrden(ctx, primera.ID, ultimoEstado(t, primera.ID), estado); err != nil {
			t.Fatalf("mover la orden a %s: %v", estado, err)
		}
		otra := nuevaOrden(t, skuUnaAbierta, 5, service.EstadoBorrador)
		if err := repo.CrearOrden(ctx, otra); !errors.Is(err, repository.ErrOrdenAbiertaExistente) {
			t.Errorf("con la orden en %s: error = %v; tendría que seguir bloqueando", estado, err)
		}
	}
}

// TestOrdenNuevaDespuesDeConfirmada: CONFIRMADA es el único estado que sale del
// índice parcial, así que el histórico no bloquea la reposición siguiente.
func TestOrdenNuevaDespuesDeConfirmada(t *testing.T) {
	prepararSKU(t, skuHistorico, 2, 10)
	limpiarOrdenes(t, skuHistorico)
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	vieja := nuevaOrden(t, skuHistorico, 18, service.EstadoConfirmada)
	if err := repo.CrearOrden(ctx, vieja); err != nil {
		t.Fatalf("crear la orden histórica: %v", err)
	}
	abierta, err := repo.OrdenAbiertaPorSKU(ctx, skuHistorico)
	if err != nil {
		t.Fatalf("OrdenAbiertaPorSKU: %v", err)
	}
	if abierta {
		t.Error("una orden CONFIRMADA no cuenta como abierta")
	}

	nueva := nuevaOrden(t, skuHistorico, 7, service.EstadoBorrador)
	if err := repo.CrearOrden(ctx, nueva); err != nil {
		t.Fatalf("crear la orden nueva con una CONFIRMADA previa: %v", err)
	}

	// Y una tercera abierta ya no entra.
	tercera := nuevaOrden(t, skuHistorico, 3, service.EstadoBorrador)
	if err := repo.CrearOrden(ctx, tercera); !errors.Is(err, repository.ErrOrdenAbiertaExistente) {
		t.Errorf("error = %v; se esperaba ErrOrdenAbiertaExistente", err)
	}

	// El listado trae las dos, la más nueva primero.
	todas, err := repo.OrdenesPorEstado(ctx, "")
	if err != nil {
		t.Fatalf("OrdenesPorEstado: %v", err)
	}
	var delSKU []repository.Orden
	for _, o := range todas {
		if o.SKU == skuHistorico {
			delSKU = append(delSKU, o)
		}
	}
	if len(delSKU) != 2 {
		t.Fatalf("el SKU tiene %d órdenes; se esperaban 2", len(delSKU))
	}
	if delSKU[0].CreadoEn.Before(delSKU[1].CreadoEn) {
		t.Error("el listado tiene que venir con la más nueva primero")
	}

	// El filtro por estado también.
	borradores, err := repo.OrdenesPorEstado(ctx, service.EstadoBorrador)
	if err != nil {
		t.Fatalf("OrdenesPorEstado(BORRADOR): %v", err)
	}
	for _, o := range borradores {
		if o.Estado != service.EstadoBorrador {
			t.Errorf("el filtro devolvió una orden en %s", o.Estado)
		}
	}
}

// TestActualizarEstadoOrdenExigeElEstadoDeOrigen: el UPDATE condicionado es lo
// que evita pisar un estado que cambió entre la lectura y la escritura.
func TestActualizarEstadoOrdenExigeElEstadoDeOrigen(t *testing.T) {
	prepararSKU(t, skuUnaAbierta+"-UPD", 2, 10)
	limpiarOrdenes(t, skuUnaAbierta+"-UPD")
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	o := nuevaOrden(t, skuUnaAbierta+"-UPD", 18, service.EstadoBorrador)
	if err := repo.CrearOrden(ctx, o); err != nil {
		t.Fatalf("crear la orden: %v", err)
	}

	movida, err := repo.ActualizarEstadoOrden(ctx, o.ID, service.EstadoBorrador, service.EstadoSolicitada)
	if err != nil {
		t.Fatalf("ActualizarEstadoOrden: %v", err)
	}
	if movida.Estado != service.EstadoSolicitada {
		t.Errorf("estado = %q; se esperaba SOLICITADA", movida.Estado)
	}
	if !movida.ActualizadoEn.After(o.ActualizadoEn) && !movida.ActualizadoEn.Equal(o.ActualizadoEn) {
		t.Error("actualizado_en tendría que haberse movido")
	}

	// Ya no está en BORRADOR: el mismo UPDATE no encuentra la fila.
	if _, err := repo.ActualizarEstadoOrden(ctx, o.ID, service.EstadoBorrador, service.EstadoSolicitada); !errors.Is(err, repository.ErrNoEncontrado) {
		t.Errorf("error = %v; se esperaba ErrNoEncontrado", err)
	}

	leida, err := repo.OrdenPorID(ctx, o.ID)
	if err != nil || leida.Estado != service.EstadoSolicitada {
		t.Errorf("OrdenPorID = %+v, %v; se esperaba la orden en SOLICITADA", leida, err)
	}
}

// ultimoEstado lee el estado vigente de la orden, para encadenar transiciones.
func ultimoEstado(t *testing.T, id string) string {
	t.Helper()
	o, err := repository.NuevoRepo(poolCompartido).OrdenPorID(context.Background(), id)
	if err != nil {
		t.Fatalf("leer la orden %s: %v", id, err)
	}
	return o.Estado
}
