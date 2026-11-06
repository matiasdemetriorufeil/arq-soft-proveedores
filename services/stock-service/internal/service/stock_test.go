package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
)

// repoFalso es el doble de la capa de datos: guarda el stock y las reservas en
// memoria y cuenta cuántas transacciones se abrieron. No hay base de datos ni
// bloqueo de filas acá: lo que se prueba es la regla, no la concurrencia (eso
// lo prueban los tests de integración de internal/repository).
type repoFalso struct {
	stock    map[string]repository.Stock
	reservas map[string]repository.Reserva // por clave de idempotencia

	transacciones int
	errStock      error // lo devuelve StockPorSKU y BloquearStock
	clavesOcultas int   // cuántas lecturas por clave mienten con ErrNoEncontrado
	duplicada     bool  // InsertarReserva devuelve ErrClaveDuplicada
	aplicadas     int   // veces que se descontó el stock
}

func nuevoRepoFalso(sku string, disponible int) *repoFalso {
	return &repoFalso{
		stock: map[string]repository.Stock{
			sku: {SKU: sku, Disponible: disponible, Reservado: 0, Minimo: 0},
		},
		reservas: map[string]repository.Reserva{},
	}
}

func (f *repoFalso) StockPorSKU(_ context.Context, sku string) (repository.Stock, error) {
	if f.errStock != nil {
		return repository.Stock{}, f.errStock
	}
	s, ok := f.stock[sku]
	if !ok {
		return repository.Stock{}, repository.ErrNoEncontrado
	}
	return s, nil
}

func (f *repoFalso) ReservaPorClave(_ context.Context, clave string) (repository.Reserva, error) {
	if f.clavesOcultas > 0 {
		// Simula la lectura que llega antes del commit del reintento ganador:
		// todavía no ve la reserva, así que el llamador entra a la transacción.
		f.clavesOcultas--
		return repository.Reserva{}, repository.ErrNoEncontrado
	}
	r, ok := f.reservas[clave]
	if !ok {
		return repository.Reserva{}, repository.ErrNoEncontrado
	}
	return r, nil
}

// EnTransaccion no simula atomicidad: cuenta la apertura y corre la función. Le
// pasa un Tx nil porque el doble no lo usa —es un tipo opaco— y eso mismo
// prueba que la capa de servicio no hace nada con él más que pasarlo.
func (f *repoFalso) EnTransaccion(_ context.Context, fn func(repository.Tx) error) error {
	f.transacciones++
	return fn(nil)
}

func (f *repoFalso) BloquearStock(_ context.Context, _ repository.Tx, sku string) (repository.Stock, error) {
	if f.errStock != nil {
		return repository.Stock{}, f.errStock
	}
	s, ok := f.stock[sku]
	if !ok {
		return repository.Stock{}, repository.ErrNoEncontrado
	}
	return s, nil
}

func (f *repoFalso) InsertarReserva(_ context.Context, _ repository.Tx, res *repository.Reserva) error {
	if f.duplicada {
		return repository.ErrClaveDuplicada
	}
	if _, existe := f.reservas[res.ClaveIdempotencia]; existe {
		return repository.ErrClaveDuplicada
	}
	f.reservas[res.ClaveIdempotencia] = *res
	return nil
}

func (f *repoFalso) AplicarReserva(_ context.Context, _ repository.Tx, sku string, cantidad int) error {
	s := f.stock[sku]
	s.Disponible -= cantidad
	s.Reservado += cantidad
	f.stock[sku] = s
	f.aplicadas++
	return nil
}

func TestReservarCaminoFeliz(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 10)
	svc := service.NuevoServicio(repo)

	reserva, repetida, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if err != nil {
		t.Fatalf("Reservar devolvió error: %v", err)
	}
	if repetida {
		t.Error("una reserva nueva no puede venir marcada como repetida")
	}
	if reserva.ID == "" {
		t.Error("la reserva tiene que tener un id")
	}
	if reserva.Estado != "CONFIRMADA" {
		t.Errorf("estado = %q; se esperaba CONFIRMADA", reserva.Estado)
	}
	if got := repo.stock["PAN-450W-MONO"]; got.Disponible != 4 || got.Reservado != 6 {
		t.Errorf("stock = disponible %d / reservado %d; se esperaba 4 / 6", got.Disponible, got.Reservado)
	}
	// El límite transaccional es uno y solo uno: lo abre el service, y las dos
	// escrituras (reserva y stock) caen adentro.
	if repo.transacciones != 1 {
		t.Errorf("se abrieron %d transacciones; se esperaba exactamente 1", repo.transacciones)
	}
}

func TestReservarEsIdempotente(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 10)
	svc := service.NuevoServicio(repo)

	primera, _, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if err != nil {
		t.Fatalf("primera reserva: %v", err)
	}

	segunda, repetida, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if err != nil {
		t.Fatalf("reintento: %v", err)
	}
	if !repetida {
		t.Error("el reintento tiene que venir marcado como repetido para responder 200")
	}
	if segunda.ID != primera.ID {
		t.Errorf("el reintento devolvió otra reserva: %q != %q", segunda.ID, primera.ID)
	}
	// CA-13.2: el stock se movió una sola vez.
	if repo.aplicadas != 1 {
		t.Errorf("el stock se descontó %d veces; se esperaba 1", repo.aplicadas)
	}
	if got := repo.stock["PAN-450W-MONO"]; got.Disponible != 4 {
		t.Errorf("disponible = %d; se esperaba 4", got.Disponible)
	}
	// El reintento se resuelve con la lectura por clave, sin tomar el lock.
	if repo.transacciones != 1 {
		t.Errorf("se abrieron %d transacciones; se esperaba 1", repo.transacciones)
	}
}

// Dos reintentos simultáneos que los dos pasan el atajo de lectura: el segundo
// choca con el UNIQUE de clave_idempotencia, su transacción se deshace y la
// respuesta sale de releer la reserva que sí quedó.
// El stock arranca en 20 y no en 10 a propósito: el que pierde la carrera llega
// a su INSERT solo si antes pasó el chequeo de disponible, porque la validación
// va primero (ver el comentario de Reservar).
func TestReservarCarreraDeReintentosNoDuplicaElStock(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 20)
	svc := service.NuevoServicio(repo)

	primera, _, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if err != nil {
		t.Fatalf("primera reserva: %v", err)
	}

	// El atajo de lectura no la ve —llega antes del commit del ganador—, así
	// que entra a la transacción, donde el INSERT choca con el UNIQUE.
	repo.clavesOcultas = 1
	repo.duplicada = true

	segunda, repetida, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if err != nil {
		t.Fatalf("relectura tras la carrera: %v", err)
	}
	if repo.transacciones != 2 {
		t.Errorf("se abrieron %d transacciones; se esperaban 2 (la ganadora y la que se deshizo)",
			repo.transacciones)
	}
	if !repetida || segunda.ID != primera.ID {
		t.Errorf("tras la carrera se esperaba la reserva ganadora %q repetida; se obtuvo %q (repetida=%v)",
			primera.ID, segunda.ID, repetida)
	}
	if repo.aplicadas != 1 {
		t.Errorf("el stock se descontó %d veces; se esperaba 1", repo.aplicadas)
	}
}

func TestReservarStockInsuficiente(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 4)
	svc := service.NuevoServicio(repo)

	_, _, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 6, "clave-1")
	if !errors.Is(err, service.ErrStockInsuficiente) {
		t.Fatalf("error = %v; se esperaba ErrStockInsuficiente", err)
	}
	var insuficiente *service.StockInsuficienteError
	if !errors.As(err, &insuficiente) {
		t.Fatalf("el error tiene que llevar el detalle del disponible; es %T", err)
	}
	if insuficiente.Disponible != 4 || insuficiente.Solicitado != 6 {
		t.Errorf("detalle = disponible %d / solicitado %d; se esperaba 4 / 6",
			insuficiente.Disponible, insuficiente.Solicitado)
	}
	if repo.aplicadas != 0 {
		t.Error("una reserva rechazada no puede mover el stock")
	}
}

func TestReservarSKUInexistente(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 10)
	svc := service.NuevoServicio(repo)

	_, _, err := svc.Reservar(context.Background(), "NO-EXISTE", 1, "clave-1")
	if !errors.Is(err, service.ErrProductoNoEncontrado) {
		t.Fatalf("error = %v; se esperaba ErrProductoNoEncontrado", err)
	}
}

func TestReservarParametrosInvalidos(t *testing.T) {
	casos := []struct {
		nombre   string
		sku      string
		cantidad int
		clave    string
	}{
		{"sin sku", "", 1, "clave-1"},
		{"cantidad cero", "PAN-450W-MONO", 0, "clave-1"},
		{"cantidad negativa", "PAN-450W-MONO", -3, "clave-1"},
		{"sin clave de idempotencia", "PAN-450W-MONO", 1, "   "},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			repo := nuevoRepoFalso("PAN-450W-MONO", 10)
			svc := service.NuevoServicio(repo)

			_, _, err := svc.Reservar(context.Background(), c.sku, c.cantidad, c.clave)
			if !errors.Is(err, service.ErrParametroInvalido) {
				t.Fatalf("error = %v; se esperaba ErrParametroInvalido", err)
			}
			if repo.transacciones != 0 {
				t.Error("una entrada inválida no tiene que abrir ninguna transacción")
			}
		})
	}
}

// Un error de infraestructura no se disfraza de error de negocio: sube tal cual
// y el handler lo traduce a 503.
func TestReservarPropagaLaFallaDeLaBase(t *testing.T) {
	falla := errors.New("conexión rechazada")
	repo := nuevoRepoFalso("PAN-450W-MONO", 10)
	repo.errStock = falla
	svc := service.NuevoServicio(repo)

	_, _, err := svc.Reservar(context.Background(), "PAN-450W-MONO", 1, "clave-1")
	if !errors.Is(err, falla) {
		t.Fatalf("error = %v; se esperaba la falla de la base", err)
	}
	if errors.Is(err, service.ErrStockInsuficiente) || errors.Is(err, service.ErrProductoNoEncontrado) {
		t.Error("una falla de la base no puede parecer un error de negocio")
	}
}

func TestStockPorSKU(t *testing.T) {
	repo := nuevoRepoFalso("PAN-450W-MONO", 10)
	svc := service.NuevoServicio(repo)

	st, err := svc.Stock(context.Background(), "PAN-450W-MONO")
	if err != nil {
		t.Fatalf("Stock devolvió error: %v", err)
	}
	if st.Disponible != 10 {
		t.Errorf("disponible = %d; se esperaba 10", st.Disponible)
	}

	if _, err := svc.Stock(context.Background(), "NO-EXISTE"); !errors.Is(err, service.ErrProductoNoEncontrado) {
		t.Errorf("error = %v; se esperaba ErrProductoNoEncontrado", err)
	}
	if _, err := svc.Stock(context.Background(), "  "); !errors.Is(err, service.ErrParametroInvalido) {
		t.Errorf("error = %v; se esperaba ErrParametroInvalido", err)
	}
}
