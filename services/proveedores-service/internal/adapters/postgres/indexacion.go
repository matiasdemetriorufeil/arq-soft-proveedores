package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

var _ ports.AlmacenIndexacion = (*Repo)(nil)

// lockIndexacion hace que una sola réplica por vez escriba el índice. Sin esto,
// un SKU drenado durante una reconstrucción se perdería al intercambiar los
// índices, y dos reconstrucciones se pisarían el índice temporal.
const lockIndexacion int64 = 727_002

// timeoutLecturaIndexacion alcanza para leer todos los documentos: es una
// operación de fondo, no una request.
const timeoutLecturaIndexacion = 30 * time.Second

// precio_min, proveedores_con_stock y proveedores salen solo de las ofertas con
// stock (ADR-006). El LEFT JOIN deja un producto sin ofertas con los agregados
// vacíos: tiene que seguir apareciendo en la búsqueda.
const sqlDocumentos = `
SELECT p.sku, p.nombre, p.categoria, p.unidad,
       (MIN(o.precio_unitario) FILTER (WHERE o.stock_disponible > 0))::float8,
       (COUNT(*) FILTER (WHERE o.stock_disponible > 0))::int8,
       COALESCE(SUM(o.stock_disponible), 0)::int8,
       COALESCE(array_agg(pr.nombre ORDER BY pr.nombre) FILTER (WHERE o.stock_disponible > 0), '{}'::text[]),
       COALESCE(MAX(o.actualizado_en), p.creado_en)
FROM productos p
LEFT JOIN ofertas o      ON o.producto_sku = p.sku
LEFT JOIN proveedores pr ON pr.id = o.proveedor_id
`

const sqlGrupoDocumentos = `GROUP BY p.sku, p.nombre, p.categoria, p.unidad, p.creado_en ORDER BY p.sku`

// SKUsPendientes devuelve los SKU por indexar, los más viejos primero.
func (r *Repo) SKUsPendientes(ctx context.Context, limite int) ([]ports.SKUPendiente, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	filas, err := r.pool.Query(ctx,
		`SELECT sku, version FROM skus_por_reindexar ORDER BY registrado_en LIMIT $1`, limite)
	if err != nil {
		return nil, fmt.Errorf("leer SKU pendientes: %w", err)
	}
	pendientes, err := pgx.CollectRows(filas, func(f pgx.CollectableRow) (ports.SKUPendiente, error) {
		var p ports.SKUPendiente
		err := f.Scan(&p.SKU, &p.Version)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("leer SKU pendientes: %w", err)
	}
	return pendientes, nil
}

// QuitarPendientes borra cada SKU solo si sigue en la versión leída: si cambió
// mientras se indexaba, queda pendiente para la próxima vuelta.
func (r *Repo) QuitarPendientes(ctx context.Context, pendientes []ports.SKUPendiente) error {
	if len(pendientes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	skus := make([]string, len(pendientes))
	versiones := make([]int64, len(pendientes))
	for i, p := range pendientes {
		skus[i], versiones[i] = p.SKU, p.Version
	}
	_, err := r.pool.Exec(ctx, `
DELETE FROM skus_por_reindexar p
USING unnest($1::text[], $2::int8[]) AS t(sku, version)
WHERE p.sku = t.sku AND p.version = t.version`, skus, versiones)
	if err != nil {
		return fmt.Errorf("quitar SKU pendientes: %w", err)
	}
	return nil
}

// DocumentosDeProductos arma los documentos de esos SKU.
func (r *Repo) DocumentosDeProductos(ctx context.Context, skus []string) ([]ports.DocumentoProducto, error) {
	return r.leerDocumentos(ctx, sqlDocumentos+`WHERE p.sku = ANY($1::text[]) `+sqlGrupoDocumentos, skus)
}

// TodosLosDocumentos arma los documentos de todos los productos.
func (r *Repo) TodosLosDocumentos(ctx context.Context) ([]ports.DocumentoProducto, error) {
	return r.leerDocumentos(ctx, sqlDocumentos+sqlGrupoDocumentos)
}

func (r *Repo) leerDocumentos(ctx context.Context, sql string, args ...any) ([]ports.DocumentoProducto, error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutLecturaIndexacion)
	defer cancel()
	filas, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("leer documentos: %w", err)
	}
	docs, err := pgx.CollectRows(filas, func(f pgx.CollectableRow) (ports.DocumentoProducto, error) {
		var (
			d          ports.DocumentoProducto
			conStock   int64
			actualizad time.Time
		)
		err := f.Scan(&d.SKU, &d.Nombre, &d.Categoria, &d.Unidad, &d.PrecioMin,
			&conStock, &d.StockTotal, &d.Proveedores, &actualizad)
		d.ProveedoresConStock = int(conStock)
		d.ActualizadoEn = actualizad.UTC()
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("leer documentos: %w", err)
	}
	return docs, nil
}

// ConExclusionIndexacion corre fn con un lock de sesión de PostgreSQL. Usa una
// conexión propia hasta el final, porque el lock vive con la sesión: si el
// proceso muere, la base lo suelta sola.
func (r *Repo) ConExclusionIndexacion(ctx context.Context, fn func(context.Context) error) (bool, error) {
	adquirirCtx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	conn, err := r.pool.Acquire(adquirirCtx)
	if err != nil {
		return false, fmt.Errorf("conexión para el lock de indexación: %w", err)
	}
	defer conn.Release()

	var obtenido bool
	if err := conn.QueryRow(adquirirCtx, `SELECT pg_try_advisory_lock($1)`, lockIndexacion).Scan(&obtenido); err != nil {
		return false, fmt.Errorf("tomar el lock de indexación: %w", err)
	}
	if !obtenido {
		return false, nil
	}
	defer func() {
		// Contexto nuevo: el de la vuelta puede estar cancelado por el apagado, y
		// una conexión devuelta al pool con el lock tomado frenaría a las demás.
		soltarCtx, cancelar := context.WithTimeout(context.Background(), TimeoutConsulta)
		defer cancelar()
		if _, err := conn.Exec(soltarCtx, `SELECT pg_advisory_unlock($1)`, lockIndexacion); err != nil {
			_ = conn.Conn().Close(soltarCtx)
		}
	}()
	return true, fn(ctx)
}
