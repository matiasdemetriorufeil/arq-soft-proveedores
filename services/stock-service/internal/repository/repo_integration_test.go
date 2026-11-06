//go:build integration

package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// Cada test usa su propio SKU: la base es compartida y así no se pisan.
const (
	skuLectura   = "TEST-LECTURA"
	skuReserva   = "TEST-RESERVA"
	skuDuplicada = "TEST-DUPLICADA"
	skuCheck     = "TEST-CHECK"
)

func TestMigracionesYSeed(t *testing.T) {
	ctx := context.Background()

	// El seed trae los 30 SKUs de proveedores-service (002_seed.sql) y 3
	// prendas de FAV (004_pedidos_fav.sql). Se cuentan excluyendo los SKUs
	// TEST-* y FAV-TEST-* que crean los otros tests de este paquete.
	var n, prendas int
	if err := poolCompartido.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE sku NOT LIKE 'FAV-%'),
		        count(*) FILTER (WHERE sku LIKE 'FAV-var-%')
		 FROM stock_productos WHERE sku NOT LIKE 'TEST-%'`).
		Scan(&n, &prendas); err != nil {
		t.Fatalf("contar el seed: %v", err)
	}
	if n != 30 {
		t.Errorf("el seed dejó %d SKUs; se esperaban los 30 de 002_seed.sql", n)
	}
	if prendas != 3 {
		t.Errorf("el seed dejó %d prendas de FAV; se esperaban las 3 de 004_pedidos_fav.sql", prendas)
	}

	// Migrar es idempotente: se reaplica entero en cada arranque.
	if err := repository.Migrar(ctx, poolCompartido, migracionesDePrueba(), loggerSilencioso()); err != nil {
		t.Fatalf("reaplicar las migraciones: %v", err)
	}
}

func TestStockPorSKU(t *testing.T) {
	prepararSKU(t, skuLectura, 10, 3)
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	st, err := repo.StockPorSKU(ctx, skuLectura)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.SKU != skuLectura || st.Disponible != 10 || st.Reservado != 0 || st.Minimo != 3 {
		t.Errorf("stock = %+v; se esperaba disponible 10 / reservado 0 / minimo 3", st)
	}

	if _, err := repo.StockPorSKU(ctx, "NO-EXISTE-EN-NINGUN-LADO"); !errors.Is(err, repository.ErrNoEncontrado) {
		t.Errorf("error = %v; se esperaba ErrNoEncontrado", err)
	}
}

func TestPing(t *testing.T) {
	repo := repository.NuevoRepo(poolCompartido)
	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping contra una base que responde: %v", err)
	}
}

// El camino completo contra PostgreSQL real: la capa de servicio abre la
// transacción y el repositorio bloquea la fila y escribe las dos filas.
func TestReservarContraPostgresReal(t *testing.T) {
	prepararSKU(t, skuReserva, 10, 2)
	repo := repository.NuevoRepo(poolCompartido)
	svc := service.NuevoServicio(repo)
	ctx := context.Background()

	reserva, repetida, err := svc.Reservar(ctx, skuReserva, 6, "integracion-"+skuReserva)
	if err != nil {
		t.Fatalf("Reservar: %v", err)
	}
	if repetida {
		t.Error("la primera reserva no puede venir marcada como repetida")
	}
	if reserva.CreadoEn.IsZero() {
		t.Error("creado_en tiene que venir de la base")
	}

	st, err := repo.StockPorSKU(ctx, skuReserva)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.Disponible != 4 || st.Reservado != 6 {
		t.Errorf("stock = disponible %d / reservado %d; se esperaba 4 / 6", st.Disponible, st.Reservado)
	}

	// Mismo reintento: 200 con la misma reserva y el stock quieto (CA-13.2).
	otra, repetida, err := svc.Reservar(ctx, skuReserva, 6, "integracion-"+skuReserva)
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if !repetida || otra.ID != reserva.ID {
		t.Errorf("el reintento devolvió %q (repetida=%v); se esperaba %q repetida", otra.ID, repetida, reserva.ID)
	}
	st, err = repo.StockPorSKU(ctx, skuReserva)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.Disponible != 4 {
		t.Errorf("disponible = %d tras el reintento; se esperaba 4", st.Disponible)
	}

	// Stock insuficiente: queda 4 y se piden 6.
	if _, _, err := svc.Reservar(ctx, skuReserva, 6, "integracion-insuficiente"); !errors.Is(err, service.ErrStockInsuficiente) {
		t.Errorf("error = %v; se esperaba ErrStockInsuficiente", err)
	}

	// SKU que no está en stock_productos.
	if _, _, err := svc.Reservar(ctx, "NO-EXISTE-EN-NINGUN-LADO", 1, "integracion-inexistente"); !errors.Is(err, service.ErrProductoNoEncontrado) {
		t.Errorf("error = %v; se esperaba ErrProductoNoEncontrado", err)
	}
}

// El UNIQUE sobre clave_idempotencia tiene que llegar al repositorio como
// ErrClaveDuplicada: es lo que la capa de servicio usa para resolver la carrera
// entre dos reintentos.
func TestInsertarReservaDetectaLaClaveDuplicada(t *testing.T) {
	prepararSKU(t, skuDuplicada, 10, 0)
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()
	clave := "integracion-" + skuDuplicada

	escribir := func(id string) error {
		return repo.EnTransaccion(ctx, func(tx repository.Tx) error {
			res := repository.Reserva{
				ID: id, SKU: skuDuplicada, Cantidad: 1,
				ClaveIdempotencia: clave, Estado: "CONFIRMADA",
			}
			return repo.InsertarReserva(ctx, tx, &res)
		})
	}

	if err := escribir("22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatalf("primer insert: %v", err)
	}
	if err := escribir("33333333-3333-4333-8333-333333333333"); !errors.Is(err, repository.ErrClaveDuplicada) {
		t.Fatalf("error = %v; se esperaba ErrClaveDuplicada", err)
	}
}

// La red de seguridad de la base: aunque la lógica fallara, el CHECK impide que
// el disponible quede negativo (RN-13).
func TestElCheckDeLaBaseImpideElDisponibleNegativo(t *testing.T) {
	prepararSKU(t, skuCheck, 4, 0)
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	// Se saltea la validación de la capa de servicio a propósito.
	err := repo.EnTransaccion(ctx, func(tx repository.Tx) error {
		if _, err := repo.BloquearStock(ctx, tx, skuCheck); err != nil {
			return err
		}
		return repo.AplicarReserva(ctx, tx, skuCheck, 6)
	})
	if err == nil {
		t.Fatal("la base aceptó dejar el disponible en -2")
	}

	st, err := repo.StockPorSKU(ctx, skuCheck)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.Disponible != 4 {
		t.Errorf("disponible = %d; la transacción tenía que deshacerse entera y dejarlo en 4", st.Disponible)
	}
}
