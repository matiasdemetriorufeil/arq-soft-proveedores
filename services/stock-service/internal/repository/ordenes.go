package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrOrdenAbiertaExistente indica que el SKU ya tiene una orden sin cerrar: lo
// detecta el índice único parcial ux_ordenes_abierta_por_sku, no la lógica
// (CA-14.2). Es el mismo patrón que ErrClaveDuplicada: un SQLSTATE traducido a
// un error de dominio, para que la capa de servicio no conozca pgx.
var ErrOrdenAbiertaExistente = errors.New("el producto ya tiene una orden de reposición abierta")

// Orden es una fila de ordenes_reposicion.
//
// Proveedor y costo son punteros porque la orden puede nacer sin proveedor
// sugerido (proveedores-service caído, el SKU no existe para él o nadie tiene
// stock suficiente); en ese caso Motivo dice por qué.
type Orden struct {
	ID              string    `json:"id"`
	SKU             string    `json:"sku"`
	Cantidad        int       `json:"cantidad"`
	Estado          string    `json:"estado"`
	ProveedorID     *int64    `json:"proveedor_id"`
	ProveedorNombre *string   `json:"proveedor_nombre"`
	CostoTotal      *float64  `json:"costo_total"`
	Motivo          *string   `json:"motivo"`
	Intento         int       `json:"intento"`
	CreadoEn        time.Time `json:"creado_en"`
	ActualizadoEn   time.Time `json:"actualizado_en"`

	// El pedido a FAV (ADR-009). ClavePedido es la Idempotency-Key del
	// intento en curso: no se publica en la API.
	ClavePedido  *string    `json:"-"`
	PedidoID     *string    `json:"pedido_id"`
	PedidoEstado *string    `json:"pedido_estado"`
	PedidoTotal  *float64   `json:"pedido_total"`
	PagoVenceEn  *time.Time `json:"pago_vence_en"`
}

const columnasOrden = `
    id::text, sku, cantidad, estado, proveedor_id, proveedor_nombre,
    costo_total, motivo, intento, creado_en, actualizado_en,
    clave_pedido, pedido_id, pedido_estado, pedido_total, pago_vence_en`

const querySKUsBajoMinimo = `
SELECT sku, disponible, reservado, minimo
FROM stock_productos
WHERE disponible < minimo
ORDER BY sku`

// SKUsBajoMinimo devuelve las filas de stock que cayeron por debajo de su
// mínimo configurado (RN-14). Es una lectura sin bloqueo: el ciclo de
// reposición no compite con las reservas, y si el stock cambia entre esta
// lectura y la creación de la orden, el ciclo siguiente lo corrige.
func (r *Repo) SKUsBajoMinimo(ctx context.Context) ([]Stock, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	filas, err := r.pool.Query(ctx, querySKUsBajoMinimo)
	if err != nil {
		return nil, fmt.Errorf("consultar SKUs bajo el mínimo: %w", err)
	}
	defer filas.Close()

	var bajos []Stock
	for filas.Next() {
		var s Stock
		if err := filas.Scan(&s.SKU, &s.Disponible, &s.Reservado, &s.Minimo); err != nil {
			return nil, fmt.Errorf("leer SKU bajo el mínimo: %w", err)
		}
		bajos = append(bajos, s)
	}
	if err := filas.Err(); err != nil {
		return nil, fmt.Errorf("recorrer SKUs bajo el mínimo: %w", err)
	}
	return bajos, nil
}

// El predicado es el mismo del índice parcial: abierta es todo lo que no está
// CONFIRMADA.
const queryOrdenAbiertaPorSKU = `
SELECT EXISTS (
    SELECT 1 FROM ordenes_reposicion
    WHERE sku = $1 AND estado <> 'CONFIRMADA')`

// OrdenAbiertaPorSKU dice si el SKU ya tiene una orden sin cerrar. Es un atajo
// del ciclo para no preguntarle a proveedores-service por un SKU cuya orden ya
// existe; la garantía de CA-14.2 sigue siendo el índice único parcial, que es
// el que resuelve la carrera entre dos réplicas.
func (r *Repo) OrdenAbiertaPorSKU(ctx context.Context, sku string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	var abierta bool
	if err := r.pool.QueryRow(ctx, queryOrdenAbiertaPorSKU, sku).Scan(&abierta); err != nil {
		return false, fmt.Errorf("consultar orden abierta de '%s': %w", sku, err)
	}
	return abierta, nil
}

const queryCrearOrden = `
INSERT INTO ordenes_reposicion
    (id, sku, cantidad, estado, proveedor_id, proveedor_nombre, costo_total, motivo, intento)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING creado_en, actualizado_en`

// CrearOrden graba la orden. Si el SKU ya tiene una abierta devuelve
// ErrOrdenAbiertaExistente: el único UNIQUE con el que puede chocar un INSERT
// con id nuevo es el índice parcial por SKU (CA-14.2 y la carrera entre dos
// réplicas corriendo el ciclo a la vez).
func (r *Repo) CrearOrden(ctx context.Context, o *Orden) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	err := r.pool.QueryRow(ctx, queryCrearOrden,
		o.ID, o.SKU, o.Cantidad, o.Estado, o.ProveedorID, o.ProveedorNombre,
		o.CostoTotal, o.Motivo, o.Intento).
		Scan(&o.CreadoEn, &o.ActualizadoEn)

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == codigoViolacionUnica {
		return ErrOrdenAbiertaExistente
	}
	if err != nil {
		return fmt.Errorf("crear orden de reposición: %w", err)
	}
	return nil
}

const queryOrdenPorID = `
SELECT` + columnasOrden + `
FROM ordenes_reposicion
WHERE id = $1::uuid`

// OrdenPorID devuelve la orden o ErrNoEncontrado.
func (r *Repo) OrdenPorID(ctx context.Context, id string) (Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearOrden(r.pool.QueryRow(ctx, queryOrdenPorID, id))
}

// El filtro por estado se resuelve con un solo plan: con $1 vacío la primera
// condición es verdadera para todas las filas.
const queryOrdenesPorEstado = `
SELECT` + columnasOrden + `
FROM ordenes_reposicion
WHERE ($1 = '' OR estado = $1)
ORDER BY creado_en DESC, id DESC`

// OrdenesPorEstado lista las órdenes, la más nueva primero. Un estado vacío
// devuelve todas; validar que el estado exista es de la capa de servicio.
func (r *Repo) OrdenesPorEstado(ctx context.Context, estado string) ([]Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	filas, err := r.pool.Query(ctx, queryOrdenesPorEstado, estado)
	if err != nil {
		return nil, fmt.Errorf("listar órdenes: %w", err)
	}
	defer filas.Close()

	ordenes := make([]Orden, 0)
	for filas.Next() {
		o, err := escanearOrden(filas)
		if err != nil {
			return nil, err
		}
		ordenes = append(ordenes, o)
	}
	if err := filas.Err(); err != nil {
		return nil, fmt.Errorf("recorrer órdenes: %w", err)
	}
	return ordenes, nil
}

// El WHERE incluye el estado de origen: si entre la lectura de la capa de
// servicio y este UPDATE alguien más movió la orden, se actualizan 0 filas y la
// transición se rechaza en lugar de pisar el estado nuevo.
const queryActualizarEstadoOrden = `
UPDATE ordenes_reposicion
SET estado = $3, actualizado_en = now()
WHERE id = $1::uuid AND estado = $2
RETURNING` + columnasOrden

// ActualizarEstadoOrden mueve la orden de `desde` a `hacia` y devuelve la fila
// resultante, o ErrNoEncontrado si la orden no existe o ya no está en `desde`.
// No valida la transición: eso es de la capa de servicio.
func (r *Repo) ActualizarEstadoOrden(ctx context.Context, id, desde, hacia string) (Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearOrden(r.pool.QueryRow(ctx, queryActualizarEstadoOrden, id, desde, hacia))
}

// escanearOrden traduce pgx.ErrNoRows a ErrNoEncontrado: fuera de este paquete
// nadie conoce los errores de pgx.
func escanearOrden(fila pgx.Row) (Orden, error) {
	var o Orden
	err := fila.Scan(&o.ID, &o.SKU, &o.Cantidad, &o.Estado, &o.ProveedorID,
		&o.ProveedorNombre, &o.CostoTotal, &o.Motivo, &o.Intento,
		&o.CreadoEn, &o.ActualizadoEn,
		&o.ClavePedido, &o.PedidoID, &o.PedidoEstado, &o.PedidoTotal, &o.PagoVenceEn)
	if errors.Is(err, pgx.ErrNoRows) {
		return Orden{}, ErrNoEncontrado
	}
	if err != nil {
		return Orden{}, fmt.Errorf("leer orden de reposición: %w", err)
	}
	return o, nil
}
