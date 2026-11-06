//go:build integration

package repository_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// TestSinSobreventaConReservasSimultaneas es la evidencia de RN-13 y CA-13.1.
//
// Con disponible 10 y 20 reservas de 6 lanzadas a la vez, el SELECT … FOR
// UPDATE serializa a los competidores sobre esa única fila: el primero
// commitea, los demás recién entonces leen el disponible ya descontado y se
// rechazan. El resultado tiene que ser exactamente una aceptada y 19
// rechazadas, con el disponible en 4 y el reservado en 6.
//
// No vale como evidencia correrlo con un repositorio falso: lo que se prueba es
// el comportamiento del motor, así que necesita PostgreSQL de verdad.
func TestSinSobreventaConReservasSimultaneas(t *testing.T) {
	const (
		sku        = "TEST-CONCURRENCIA"
		disponible = 10
		cantidad   = 6
		reservas   = 20
	)
	prepararSKU(t, sku, disponible, 0)

	repo := repository.NuevoRepo(poolCompartido)
	svc := service.NuevoServicio(repo)
	ctx := context.Background()

	// La barrera hace que las 20 goroutines salgan juntas: sin ella entrarían
	// en fila y el test no probaría nada de concurrencia.
	salida := make(chan struct{})
	var listas, terminadas sync.WaitGroup
	listas.Add(reservas)
	terminadas.Add(reservas)

	var mu sync.Mutex
	var aceptadas, rechazadas int
	var inesperados []error

	for i := 0; i < reservas; i++ {
		go func(i int) {
			defer terminadas.Done()
			listas.Done()
			<-salida

			// Clave de idempotencia distinta por goroutine: son 20 reservas
			// distintas compitiendo, no 20 reintentos de la misma.
			_, _, err := svc.Reservar(ctx, sku, cantidad, fmt.Sprintf("concurrencia-%d", i))

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				aceptadas++
			case errors.Is(err, service.ErrStockInsuficiente):
				rechazadas++
			default:
				inesperados = append(inesperados, err)
			}
		}(i)
	}

	listas.Wait()
	close(salida)
	terminadas.Wait()

	for _, err := range inesperados {
		t.Errorf("error inesperado: %v", err)
	}
	if aceptadas != 1 {
		t.Errorf("se aceptaron %d reservas; se esperaba exactamente 1", aceptadas)
	}
	if rechazadas != reservas-1 {
		t.Errorf("se rechazaron %d reservas; se esperaban %d", rechazadas, reservas-1)
	}

	st, err := repo.StockPorSKU(ctx, sku)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.Disponible != disponible-cantidad {
		t.Errorf("disponible = %d; se esperaba %d", st.Disponible, disponible-cantidad)
	}
	if st.Reservado != cantidad {
		t.Errorf("reservado = %d; se esperaba %d", st.Reservado, cantidad)
	}
	// Lo que RN-13 prohíbe de verdad: el disponible no puede quedar negativo.
	if st.Disponible < 0 {
		t.Errorf("sobreventa: el disponible quedó en %d", st.Disponible)
	}

	var filas int
	if err := poolCompartido.QueryRow(ctx,
		`SELECT count(*) FROM reservas WHERE sku = $1`, sku).Scan(&filas); err != nil {
		t.Fatalf("contar reservas: %v", err)
	}
	if filas != 1 {
		t.Errorf("quedaron %d reservas grabadas; se esperaba 1", filas)
	}
}

// El mismo escenario pero con una sola clave de idempotencia: 20 reintentos
// simultáneos de la misma reserva aplican el stock una sola vez (CA-13.2). El
// disponible arranca holgado para que todos los reintentos lleguen al INSERT y
// la carrera la resuelva el UNIQUE y no el chequeo de disponible.
func TestReintentosSimultaneosAplicanElStockUnaVez(t *testing.T) {
	const (
		sku        = "TEST-IDEMPOTENCIA"
		disponible = 100
		cantidad   = 6
		reintentos = 20
		clave      = "reintento-unico"
	)
	prepararSKU(t, sku, disponible, 0)

	svc := service.NuevoServicio(repository.NuevoRepo(poolCompartido))
	repo := repository.NuevoRepo(poolCompartido)
	ctx := context.Background()

	salida := make(chan struct{})
	var listas, terminadas sync.WaitGroup
	listas.Add(reintentos)
	terminadas.Add(reintentos)

	var mu sync.Mutex
	ids := map[string]int{}
	var fallos []error

	for i := 0; i < reintentos; i++ {
		go func() {
			defer terminadas.Done()
			listas.Done()
			<-salida

			reserva, _, err := svc.Reservar(ctx, sku, cantidad, clave)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				fallos = append(fallos, err)
				return
			}
			ids[reserva.ID]++
		}()
	}

	listas.Wait()
	close(salida)
	terminadas.Wait()

	for _, err := range fallos {
		t.Errorf("un reintento de una reserva idempotente no tiene que fallar: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("los reintentos devolvieron %d reservas distintas; se esperaba 1: %v", len(ids), ids)
	}

	st, err := repo.StockPorSKU(ctx, sku)
	if err != nil {
		t.Fatalf("StockPorSKU: %v", err)
	}
	if st.Disponible != disponible-cantidad || st.Reservado != cantidad {
		t.Errorf("stock = disponible %d / reservado %d; se esperaba %d / %d (el stock se movió más de una vez)",
			st.Disponible, st.Reservado, disponible-cantidad, cantidad)
	}
}
