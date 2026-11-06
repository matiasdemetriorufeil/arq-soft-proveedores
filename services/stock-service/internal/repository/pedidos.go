package repository

import (
	"context"
	"fmt"
	"time"
)

// DatosPedido es lo que se guarda de un pedido que FAV aceptó.
type DatosPedido struct {
	PedidoID    string
	Estado      string
	Total       float64
	PagoVenceEn time.Time
}

// El intento nuevo solo se arma si la orden sigue en BORRADOR y no tiene un
// intento en curso: si lo tiene, hay que reintentar con esa misma clave.
const queryNuevoIntentoDePedido = `
UPDATE ordenes_reposicion
SET intento = intento + 1,
    clave_pedido = id::text || '-' || (intento + 1),
    actualizado_en = now()
WHERE id = $1::uuid AND estado = 'BORRADOR' AND clave_pedido IS NULL
RETURNING` + columnasOrden

// NuevoIntentoDePedido suma un intento y le asigna su Idempotency-Key,
// `<id>-<intento>`. Devuelve ErrNoEncontrado si la orden no existe, ya no
// está en BORRADOR o ya tiene un intento en curso.
func (r *Repo) NuevoIntentoDePedido(ctx context.Context, id string) (Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearOrden(r.pool.QueryRow(ctx, queryNuevoIntentoDePedido, id))
}

// La clave en el WHERE asegura que se registra la respuesta del intento que
// la pidió y no la de uno viejo.
const queryRegistrarPedido = `
UPDATE ordenes_reposicion
SET estado = 'SOLICITADA',
    pedido_id = $3, pedido_estado = $4, pedido_total = $5, pago_vence_en = $6,
    motivo = NULL, actualizado_en = now()
WHERE id = $1::uuid AND estado = 'BORRADOR' AND clave_pedido = $2
RETURNING` + columnasOrden

// RegistrarPedido pasa la orden a SOLICITADA con los datos del pedido que FAV
// aceptó. Devuelve ErrNoEncontrado si la orden ya no está en BORRADOR con esa
// clave (otro proceso la registró primero).
func (r *Repo) RegistrarPedido(ctx context.Context, id, clave string, p DatosPedido) (Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearOrden(r.pool.QueryRow(ctx, queryRegistrarPedido,
		id, clave, p.PedidoID, p.Estado, p.Total, p.PagoVenceEn))
}

const queryRegistrarFalloDeEnvio = `
UPDATE ordenes_reposicion
SET motivo = $2,
    clave_pedido = CASE WHEN $3 THEN NULL ELSE clave_pedido END,
    actualizado_en = now()
WHERE id = $1::uuid AND estado = 'BORRADOR'`

// RegistrarFalloDeEnvio deja el motivo a la vista. Con liberarClave, el
// intento terminó (FAV dio una respuesta definitiva) y el próximo usa una
// clave nueva; sin él, se reintenta con la misma.
func (r *Repo) RegistrarFalloDeEnvio(ctx context.Context, id, motivo string, liberarClave bool) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	if _, err := r.pool.Exec(ctx, queryRegistrarFalloDeEnvio, id, motivo, liberarClave); err != nil {
		return fmt.Errorf("registrar el fallo del pedido: %w", err)
	}
	return nil
}

const queryOrdenesParaEnviar = `
SELECT` + columnasOrden + `
FROM ordenes_reposicion
WHERE estado = 'BORRADOR' AND sku LIKE 'FAV-%'
ORDER BY creado_en, id`

// OrdenesParaEnviar son las órdenes de prendas de FAV que todavía no tienen
// pedido.
func (r *Repo) OrdenesParaEnviar(ctx context.Context) ([]Orden, error) {
	return r.listarOrdenes(ctx, queryOrdenesParaEnviar)
}

// Las VENCIDA se siguen 7 días: si la transferencia llegó tarde, FAV pasa el
// pedido a A_VERIFICAR (ADR-009, consulta de respaldo).
const queryOrdenesEnSeguimiento = `
SELECT` + columnasOrden + `
FROM ordenes_reposicion
WHERE pedido_id IS NOT NULL
  AND (estado = 'SOLICITADA'
       OR (estado = 'VENCIDA' AND actualizado_en > now() - interval '7 days'))
ORDER BY creado_en, id`

// OrdenesEnSeguimiento son las órdenes cuyo pedido todavía puede cambiar.
func (r *Repo) OrdenesEnSeguimiento(ctx context.Context) ([]Orden, error) {
	return r.listarOrdenes(ctx, queryOrdenesEnSeguimiento)
}

func (r *Repo) listarOrdenes(ctx context.Context, query string) ([]Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	filas, err := r.pool.Query(ctx, query)
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

const queryOrdenPorPedidoID = `
SELECT` + columnasOrden + `
FROM ordenes_reposicion
WHERE pedido_id = $1`

// OrdenPorPedidoID devuelve la orden del pedido de FAV, o ErrNoEncontrado.
func (r *Repo) OrdenPorPedidoID(ctx context.Context, pedidoID string) (Orden, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearOrden(r.pool.QueryRow(ctx, queryOrdenPorPedidoID, pedidoID))
}

const queryRegistrarAviso = `
INSERT INTO avisos_fav_procesados (evento_id, pedido_id, estado)
VALUES ($1::uuid, $2, $3)
ON CONFLICT (evento_id) DO NOTHING`

// RegistrarAviso guarda el eventoId del aviso y dice si es nuevo. Con dos
// entregas del mismo aviso a la vez, la PRIMARY KEY deja pasar una sola.
func (r *Repo) RegistrarAviso(ctx context.Context, eventoID, pedidoID, estado string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	tag, err := r.pool.Exec(ctx, queryRegistrarAviso, eventoID, pedidoID, estado)
	if err != nil {
		return false, fmt.Errorf("registrar el aviso de FAV: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

const queryActualizarEstadoDelPedido = `
UPDATE ordenes_reposicion
SET pedido_estado = $2, actualizado_en = now()
WHERE id = $1::uuid AND pedido_estado IS DISTINCT FROM $2`

// ActualizarEstadoDelPedido guarda el estado de FAV cuando no mueve la orden
// (por ejemplo, PENDIENTE_DE_PAGO).
func (r *Repo) ActualizarEstadoDelPedido(ctx context.Context, id, estadoFAV string) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	if _, err := r.pool.Exec(ctx, queryActualizarEstadoDelPedido, id, estadoFAV); err != nil {
		return fmt.Errorf("actualizar el estado del pedido: %w", err)
	}
	return nil
}

const queryAvanzarOrdenTx = `
UPDATE ordenes_reposicion
SET estado = $3, pedido_estado = $4, actualizado_en = now()
WHERE id = $1::uuid AND estado = $2
RETURNING` + columnasOrden

// AvanzarOrden mueve la orden de `desde` a `hacia` dentro de la transacción y
// guarda el estado de FAV que lo causó. ErrNoEncontrado si ya no estaba en
// `desde`. No valida la transición: eso es de la capa de servicio.
func (r *Repo) AvanzarOrden(ctx context.Context, tx Tx, id, desde, hacia, estadoFAV string) (Orden, error) {
	return escanearOrden(tx.pgxTx().QueryRow(ctx, queryAvanzarOrdenTx, id, desde, hacia, estadoFAV))
}

const querySumarStock = `
UPDATE stock_productos
SET disponible = disponible + $2, actualizado_en = now()
WHERE sku = $1`

// SumarStock registra el stock entrante de una orden confirmada.
func (r *Repo) SumarStock(ctx context.Context, tx Tx, sku string, cantidad int) error {
	tag, err := tx.pgxTx().Exec(ctx, querySumarStock, sku, cantidad)
	if err != nil {
		return fmt.Errorf("sumar el stock entrante: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("sumar el stock entrante: el producto '%s' no existe", sku)
	}
	return nil
}
