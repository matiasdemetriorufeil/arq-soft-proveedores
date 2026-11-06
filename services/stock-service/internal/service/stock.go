// Package service es la capa de negocio de stock-service: es la dueña del
// límite transaccional y de la regla de "sin sobreventa" (RN-13). No conoce
// HTTP ni el motor de base de datos: sus errores son de negocio y el SQL lo
// escribe internal/repository.
//
// Para comprobar el aislamiento: `go list -deps ./internal/service` no muestra
// ningún paquete de jackc/pgx.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
)

// estadoConfirmada es el único estado con el que nace una reserva: hoy la
// reserva se confirma en el acto. La cancelación es otra operación, que no
// existe todavía.
const estadoConfirmada = "CONFIRMADA"

// Errores de negocio. El handler los traduce a su status HTTP y a su `code`.
var (
	// ErrParametroInvalido lo devuelve la validación de la entrada.
	ErrParametroInvalido = errors.New("parámetro inválido")
	// ErrProductoNoEncontrado indica que el SKU no está en stock_productos.
	ErrProductoNoEncontrado = errors.New("producto no encontrado")
	// ErrStockInsuficiente indica que no hay unidades disponibles para la
	// cantidad pedida. Se compara con errors.Is; el detalle viaja en
	// StockInsuficienteError.
	ErrStockInsuficiente = errors.New("stock insuficiente")
)

// StockInsuficienteError lleva cuánto había disponible al momento del rechazo,
// para que el handler lo pueda poner en el `detail` del problem+json.
type StockInsuficienteError struct {
	SKU        string
	Disponible int
	Solicitado int
}

func (e *StockInsuficienteError) Error() string {
	return fmt.Sprintf("stock insuficiente de '%s': se pidieron %d unidades y hay %d disponibles",
		e.SKU, e.Solicitado, e.Disponible)
}

// Is hace que errors.Is(err, ErrStockInsuficiente) sea verdadero: el handler
// decide el status por el centinela y el detalle lo saca del tipo concreto.
func (e *StockInsuficienteError) Is(target error) bool { return target == ErrStockInsuficiente }

// Repositorio es lo que la capa de negocio necesita de la capa de datos. Está
// declarada acá y no en repository para poder sustituirla por un doble en los
// tests sin levantar PostgreSQL. repository.Tx es un tipo opaco: esta capa lo
// recibe y lo vuelve a pasar, no puede ejecutar nada con él.
type Repositorio interface {
	StockPorSKU(ctx context.Context, sku string) (repository.Stock, error)
	ReservaPorClave(ctx context.Context, clave string) (repository.Reserva, error)
	EnTransaccion(ctx context.Context, fn func(repository.Tx) error) error
	BloquearStock(ctx context.Context, tx repository.Tx, sku string) (repository.Stock, error)
	InsertarReserva(ctx context.Context, tx repository.Tx, res *repository.Reserva) error
	AplicarReserva(ctx context.Context, tx repository.Tx, sku string, cantidad int) error
}

// Servicio implementa las operaciones de stock.
type Servicio struct {
	repo Repositorio
}

// NuevoServicio crea la capa de negocio sobre un repositorio.
func NuevoServicio(repo Repositorio) *Servicio { return &Servicio{repo: repo} }

// Stock devuelve la fila de stock del SKU o ErrProductoNoEncontrado.
func (s *Servicio) Stock(ctx context.Context, sku string) (repository.Stock, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return repository.Stock{}, fmt.Errorf("%w: el SKU es obligatorio", ErrParametroInvalido)
	}
	st, err := s.repo.StockPorSKU(ctx, sku)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return repository.Stock{}, ErrProductoNoEncontrado
	}
	return st, err
}

// Reservar reserva `cantidad` unidades de `sku`. Devuelve la reserva y si ya
// existía (reintento de la misma clave de idempotencia), para que el handler
// responda 200 en vez de 201.
//
// La idempotencia se chequea dos veces a propósito. La primera lectura, antes
// de abrir la transacción, es un atajo: resuelve el reintento normal —el que
// llega cuando la reserva original ya commiteó— sin tomar el lock de la fila de
// stock. La garantía real es el UNIQUE sobre clave_idempotencia: si dos
// reintentos llegan simultáneos y los dos pasan el atajo, el segundo INSERT
// falla con clave duplicada, su transacción se deshace entera (el stock no se
// movió) y se responde releyendo la reserva que sí quedó (CA-13.2).
//
// Caso de borde aceptado: el que pierde la carrera llega al INSERT solo si
// antes pasó el chequeo de disponible, porque la validación va primero. Si el
// ganador dejó el disponible por debajo de la cantidad pedida, el que pierde
// responde 409 en vez de 200. Es transitorio: el reintento siguiente ya
// encuentra la reserva en el atajo de lectura y responde 200. El stock, que es
// lo que RN-13 protege, nunca se mueve dos veces.
func (s *Servicio) Reservar(ctx context.Context, sku string, cantidad int, clave string) (repository.Reserva, bool, error) {
	sku = strings.TrimSpace(sku)
	clave = strings.TrimSpace(clave)
	switch {
	case sku == "":
		return repository.Reserva{}, false, fmt.Errorf("%w: el SKU es obligatorio", ErrParametroInvalido)
	case cantidad <= 0:
		return repository.Reserva{}, false, fmt.Errorf("%w: 'cantidad' debe ser un entero mayor a 0", ErrParametroInvalido)
	case clave == "":
		return repository.Reserva{}, false, fmt.Errorf("%w: falta el header Idempotency-Key", ErrParametroInvalido)
	}

	existente, err := s.repo.ReservaPorClave(ctx, clave)
	switch {
	case err == nil:
		return existente, true, nil
	case !errors.Is(err, repository.ErrNoEncontrado):
		return repository.Reserva{}, false, err
	}

	id, err := nuevoUUID()
	if err != nil {
		return repository.Reserva{}, false, err
	}
	nueva := repository.Reserva{
		ID:                id,
		SKU:               sku,
		Cantidad:          cantidad,
		ClaveIdempotencia: clave,
		Estado:            estadoConfirmada,
	}

	// Un solo límite transaccional: bloquear la fila, validar el disponible y
	// escribir las dos filas, o no escribir ninguna.
	err = s.repo.EnTransaccion(ctx, func(tx repository.Tx) error {
		st, err := s.repo.BloquearStock(ctx, tx, sku) // SELECT … FOR UPDATE
		if err != nil {
			if errors.Is(err, repository.ErrNoEncontrado) {
				return ErrProductoNoEncontrado
			}
			return err
		}
		if st.Disponible < cantidad {
			return &StockInsuficienteError{SKU: sku, Disponible: st.Disponible, Solicitado: cantidad}
		}
		if err := s.repo.InsertarReserva(ctx, tx, &nueva); err != nil {
			return err
		}
		return s.repo.AplicarReserva(ctx, tx, sku, cantidad)
	})

	if errors.Is(err, repository.ErrClaveDuplicada) {
		// Otro reintento de la misma reserva ganó la carrera y commiteó. Esta
		// transacción se deshizo completa, así que el stock se movió una sola
		// vez: alcanza con devolver la reserva que quedó.
		existente, errLectura := s.repo.ReservaPorClave(ctx, clave)
		if errLectura != nil {
			return repository.Reserva{}, false, errLectura
		}
		return existente, true, nil
	}
	if err != nil {
		return repository.Reserva{}, false, err
	}
	return nueva, false, nil
}

// nuevoUUID genera un UUID versión 4 textual. Se hace acá, con la biblioteca
// estándar, para no sumar una dependencia por 15 líneas y para que la reserva
// tenga su ID antes de tocar la base.
func nuevoUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generar el id de la reserva: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // versión 4
	b[8] = (b[8] & 0x3f) | 0x80 // variante RFC 4122
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
