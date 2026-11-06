package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
)

// Los cinco estados de una orden de reposición (RN-15). No hay más: la base los
// repite en un CHECK, pero el que decide qué transición vale es este paquete.
const (
	EstadoBorrador   = "BORRADOR"
	EstadoSolicitada = "SOLICITADA"
	EstadoConfirmada = "CONFIRMADA"
	EstadoVencida    = "VENCIDA"
	EstadoEnRevision = "EN_REVISION"
)

// transicionesPermitidas es la máquina de estados de RN-15, y es la única
// fuente de verdad: lo que no está acá se rechaza. CONFIRMADA es el único
// estado final de verdad; VENCIDA y EN_REVISION cuentan como abiertas (ADR-009:
// las dos necesitan que una persona intervenga), y EN_REVISION se resuelve a
// mano, fuera del sistema.
//
//	BORRADOR   → SOLICITADA
//	SOLICITADA → CONFIRMADA | VENCIDA | EN_REVISION
//	VENCIDA    → EN_REVISION
var transicionesPermitidas = map[string][]string{
	EstadoBorrador:   {EstadoSolicitada},
	EstadoSolicitada: {EstadoConfirmada, EstadoVencida, EstadoEnRevision},
	EstadoVencida:    {EstadoEnRevision},
	EstadoConfirmada: {},
	EstadoEnRevision: {},
}

// Errores de negocio de las órdenes. El handler los traduce a su status y su
// `code`, igual que los de la reserva.
var (
	// ErrOrdenNoEncontrada indica que no hay ninguna orden con ese id.
	ErrOrdenNoEncontrada = errors.New("orden de reposición no encontrada")
	// ErrTransicionInvalida indica que RN-15 no permite ese cambio de estado.
	// Se compara con errors.Is; el detalle viaja en TransicionInvalidaError.
	ErrTransicionInvalida = errors.New("transición de estado inválida")
)

// TransicionInvalidaError lleva el estado de origen y el pedido, para que el
// handler los pueda poner en el `detail` del problem+json.
type TransicionInvalidaError struct {
	ID         string
	Desde      string
	Hacia      string
	Permitidos []string
}

func (e *TransicionInvalidaError) Error() string {
	if len(e.Permitidos) == 0 {
		return fmt.Sprintf("la orden está en '%s', que es un estado final: no puede pasar a '%s'",
			e.Desde, e.Hacia)
	}
	return fmt.Sprintf("la orden está en '%s' y no puede pasar a '%s'; desde '%s' solo se permite %s",
		e.Desde, e.Hacia, e.Desde, strings.Join(e.Permitidos, ", "))
}

// Is hace que errors.Is(err, ErrTransicionInvalida) sea verdadero: el handler
// decide el status por el centinela y el detalle lo saca del tipo concreto.
func (e *TransicionInvalidaError) Is(target error) bool { return target == ErrTransicionInvalida }

// RepositorioOrdenes es lo que la capa de negocio necesita de la capa de datos
// para las órdenes. Está declarada acá, del lado del consumidor, para poder
// sustituirla por un doble en los tests sin levantar PostgreSQL.
type RepositorioOrdenes interface {
	SKUsBajoMinimo(ctx context.Context) ([]repository.Stock, error)
	OrdenAbiertaPorSKU(ctx context.Context, sku string) (bool, error)
	CrearOrden(ctx context.Context, o *repository.Orden) error
	OrdenPorID(ctx context.Context, id string) (repository.Orden, error)
	OrdenesPorEstado(ctx context.Context, estado string) ([]repository.Orden, error)
	ActualizarEstadoOrden(ctx context.Context, id, desde, hacia string) (repository.Orden, error)
}

// ClienteProveedores es lo que la capa de negocio necesita de
// proveedores-service. Es una interfaz y no el cliente concreto para que esta
// capa no conozca el transporte: el que sabe de HTTP, timeouts y reintentos es
// internal/proveedores, igual que el que sabe de SQL es internal/repository.
type ClienteProveedores interface {
	MasBarato(ctx context.Context, sku string, cantidad int) (proveedores.Recomendacion, error)
}

// ServicioOrdenes implementa la reposición automática y la máquina de estados
// de la orden (HU-14). Es un servicio aparte del de la reserva a propósito: no
// comparte ni la transacción ni el repositorio, así la reserva —que es la
// operación publicada— no se ve afectada por nada de esto.
type ServicioOrdenes struct {
	repo RepositorioOrdenes
	prov ClienteProveedores

	// El proveedor de las prendas de FAV (UsarProveedorFAV). Con favID en 0,
	// sus SKUs se tratan como cualquier otro.
	favID     int64
	favNombre string
}

// UsarProveedorFAV fija el id de FAV en la tabla de proveedores (el 11, de la
// migración 003 de proveedores-service): las órdenes de sus prendas van
// dirigidas a FAV sin consultar el ranking.
func (s *ServicioOrdenes) UsarProveedorFAV(id int64, nombre string) {
	s.favID, s.favNombre = id, nombre
}

// NuevoServicioOrdenes crea la capa de negocio de las órdenes. `prov` puede ser
// nil: sin PROVEEDORES_URL configurada el ciclo queda apagado, pero las
// consultas y las transiciones siguen funcionando.
func NuevoServicioOrdenes(repo RepositorioOrdenes, prov ClienteProveedores) *ServicioOrdenes {
	return &ServicioOrdenes{repo: repo, prov: prov}
}

// EstadoValido dice si el estado es uno de los cinco de RN-15.
func EstadoValido(estado string) bool {
	_, ok := transicionesPermitidas[estado]
	return ok
}

// EstadosValidos devuelve los cinco estados, para los mensajes de error.
func EstadosValidos() []string {
	return []string{EstadoBorrador, EstadoSolicitada, EstadoConfirmada, EstadoVencida, EstadoEnRevision}
}

// Ordenes lista las órdenes, la más nueva primero. Un estado vacío devuelve
// todas; un estado que no existe es un parámetro inválido, no una lista vacía.
func (s *ServicioOrdenes) Ordenes(ctx context.Context, estado string) ([]repository.Orden, error) {
	estado = strings.TrimSpace(estado)
	if estado != "" && !EstadoValido(estado) {
		return nil, fmt.Errorf("%w: 'estado' tiene que ser uno de %s",
			ErrParametroInvalido, strings.Join(EstadosValidos(), ", "))
	}
	return s.repo.OrdenesPorEstado(ctx, estado)
}

// Orden devuelve una orden por su id.
func (s *ServicioOrdenes) Orden(ctx context.Context, id string) (repository.Orden, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return repository.Orden{}, fmt.Errorf("%w: el id de la orden es obligatorio", ErrParametroInvalido)
	}
	o, err := s.repo.OrdenPorID(ctx, id)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return repository.Orden{}, ErrOrdenNoEncontrada
	}
	return o, err
}

// TransicionarOrden mueve la orden al estado pedido si RN-15 lo permite, y la
// rechaza con TransicionInvalidaError si no (CA-14.3). La regla está acá, en la
// capa de negocio, y no en SQL: la base solo conoce los cinco estados posibles.
func (s *ServicioOrdenes) TransicionarOrden(ctx context.Context, id, hacia string) (repository.Orden, error) {
	id = strings.TrimSpace(id)
	hacia = strings.TrimSpace(hacia)
	switch {
	case id == "":
		return repository.Orden{}, fmt.Errorf("%w: el id de la orden es obligatorio", ErrParametroInvalido)
	case hacia == "":
		return repository.Orden{}, fmt.Errorf("%w: 'estado' es obligatorio", ErrParametroInvalido)
	case !EstadoValido(hacia):
		return repository.Orden{}, fmt.Errorf("%w: 'estado' tiene que ser uno de %s",
			ErrParametroInvalido, strings.Join(EstadosValidos(), ", "))
	}

	actual, err := s.repo.OrdenPorID(ctx, id)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return repository.Orden{}, ErrOrdenNoEncontrada
	}
	if err != nil {
		return repository.Orden{}, err
	}

	if err := validarTransicion(id, actual.Estado, hacia); err != nil {
		return repository.Orden{}, err
	}

	// El UPDATE exige que la orden siga en el estado que se leyó. Si otro la
	// movió en el medio, no actualiza nada y se vuelve a rechazar con el estado
	// que quedó, en lugar de pisarlo.
	actualizada, err := s.repo.ActualizarEstadoOrden(ctx, id, actual.Estado, hacia)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return repository.Orden{}, s.rechazarPorEstadoCambiado(ctx, id, hacia)
	}
	if err != nil {
		return repository.Orden{}, err
	}
	return actualizada, nil
}

// rechazarPorEstadoCambiado relee la orden para decir con precisión por qué se
// rechazó: alguien la movió entre la lectura y el UPDATE, o la borró.
func (s *ServicioOrdenes) rechazarPorEstadoCambiado(ctx context.Context, id, hacia string) error {
	actual, err := s.repo.OrdenPorID(ctx, id)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return ErrOrdenNoEncontrada
	}
	if err != nil {
		return err
	}
	if err := validarTransicion(id, actual.Estado, hacia); err != nil {
		return err
	}
	// La transición volvió a ser válida contra el estado nuevo: es una carrera,
	// y el cliente puede reintentar.
	return &TransicionInvalidaError{ID: id, Desde: actual.Estado, Hacia: hacia,
		Permitidos: transicionesPermitidas[actual.Estado]}
}

func validarTransicion(id, desde, hacia string) error {
	for _, permitido := range transicionesPermitidas[desde] {
		if permitido == hacia {
			return nil
		}
	}
	return &TransicionInvalidaError{ID: id, Desde: desde, Hacia: hacia,
		Permitidos: transicionesPermitidas[desde]}
}
