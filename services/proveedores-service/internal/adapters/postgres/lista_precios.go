package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// Motivos por los que un cambio no se aplica.
const (
	MotivoDuplicado      = "duplicado"
	MotivoSecuenciaVieja = "secuencia_vieja"
)

var _ ports.AplicadorListaPrecios = (*Repo)(nil)

const (
	sqlRegistrarEvento = `INSERT INTO eventos_procesados (evento_id) VALUES ($1) ON CONFLICT DO NOTHING`

	// El WHERE del DO UPDATE es lo que descarta lo viejo: si la secuencia no
	// avanza, no se afecta ninguna fila. La fila bloqueada además serializa dos
	// eventos del mismo proveedor.
	sqlAvanzarSecuencia = `
INSERT INTO secuencias_proveedor (proveedor_id, ultima_secuencia) VALUES ($1, $2)
ON CONFLICT (proveedor_id) DO UPDATE
   SET ultima_secuencia = EXCLUDED.ultima_secuencia, actualizado_en = now()
 WHERE secuencias_proveedor.ultima_secuencia < EXCLUDED.ultima_secuencia`

	sqlUpsertProductos = `
INSERT INTO productos (sku, nombre, categoria, unidad)
SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[])
ON CONFLICT (sku) DO UPDATE
   SET nombre = EXCLUDED.nombre, categoria = EXCLUDED.categoria, unidad = EXCLUDED.unidad`

	sqlUpsertOfertas = `
INSERT INTO ofertas (proveedor_id, producto_sku, precio_unitario, descuento_pct,
                     cantidad_minima_descuento, flete, stock_disponible)
SELECT $1::bigint, t.sku, t.precio, t.descuento, t.minima, t.flete, t.stock
FROM unnest($2::text[], $3::float8[], $4::float8[], $5::int4[], $6::float8[], $7::int4[])
     AS t(sku, precio, descuento, minima, flete, stock)
ON CONFLICT (proveedor_id, producto_sku) DO UPDATE
   SET precio_unitario = EXCLUDED.precio_unitario,
       descuento_pct = EXCLUDED.descuento_pct,
       cantidad_minima_descuento = EXCLUDED.cantidad_minima_descuento,
       flete = EXCLUDED.flete,
       stock_disponible = EXCLUDED.stock_disponible,
       actualizado_en = now()`

	sqlBorrarBajas = `
DELETE FROM ofertas WHERE proveedor_id = $1 AND producto_sku = ANY($2::text[])
RETURNING producto_sku`

	// Con una lista vacía, <> ALL es verdadero para todas las filas: es lo que
	// retira el catálogo de un proveedor vencido. Con NULL no borraría nada, y por
	// eso la lista nunca se pasa como nil.
	sqlBorrarAusentes = `
DELETE FROM ofertas WHERE proveedor_id = $1 AND producto_sku <> ALL($2::text[])
RETURNING producto_sku`

	sqlRegistrarPendientes = `
INSERT INTO skus_por_reindexar (sku) SELECT unnest($1::text[])
ON CONFLICT (sku) DO UPDATE
   SET version = skus_por_reindexar.version + 1, registrado_en = now()`
)

// AplicarListaPrecios deduplica, descarta lo viejo y aplica el cambio en una sola
// transacción: si algo falla, no queda nada aplicado ni el evento marcado como
// procesado. Los SKU tocados quedan pendientes de indexar en esa misma
// transacción, así que no se pierden aunque Meilisearch esté caído.
func (r *Repo) AplicarListaPrecios(ctx context.Context, c ports.CambioListaPrecios) (ports.ResultadoAplicacion, error) {
	var res ports.ResultadoAplicacion
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		res = ports.ResultadoAplicacion{}

		tag, err := tx.Exec(ctx, sqlRegistrarEvento, c.EventoID)
		if err != nil {
			return fmt.Errorf("registrar evento: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res.Motivo = MotivoDuplicado
			return nil
		}

		tag, err = tx.Exec(ctx, sqlAvanzarSecuencia, c.ProveedorID, c.Secuencia)
		if err != nil {
			return fmt.Errorf("avanzar secuencia: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res.Motivo = MotivoSecuenciaVieja
			return nil
		}

		tocados := map[string]struct{}{}
		if err := upsertProductos(ctx, tx, c.Productos, tocados); err != nil {
			return err
		}
		vigentes, err := upsertOfertas(ctx, tx, c.ProveedorID, c.Ofertas, tocados)
		if err != nil {
			return err
		}
		if len(c.Bajas) > 0 {
			for _, sku := range c.Bajas {
				tocados[sku] = struct{}{}
			}
			if err := borrarOfertas(ctx, tx, sqlBorrarBajas, c.ProveedorID, c.Bajas, tocados); err != nil {
				return fmt.Errorf("borrar bajas: %w", err)
			}
		}
		if c.Completa {
			if err := borrarOfertas(ctx, tx, sqlBorrarAusentes, c.ProveedorID, vigentes, tocados); err != nil {
				return fmt.Errorf("borrar ofertas ausentes: %w", err)
			}
		}

		res.Aplicado = true
		res.SKUs = ordenados(tocados)
		if len(res.SKUs) > 0 {
			if _, err := tx.Exec(ctx, sqlRegistrarPendientes, res.SKUs); err != nil {
				return fmt.Errorf("registrar SKU pendientes: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return ports.ResultadoAplicacion{}, fmt.Errorf("aplicar lista de precios: %w", err)
	}
	return res, nil
}

func upsertProductos(ctx context.Context, tx pgx.Tx, productos []ports.ProductoCambio, tocados map[string]struct{}) error {
	if len(productos) == 0 {
		return nil
	}
	// Un mismo SKU repetido en un INSERT … ON CONFLICT hace fallar la sentencia:
	// se queda el último.
	unicos := map[string]ports.ProductoCambio{}
	for _, p := range productos {
		unicos[p.SKU] = p
		tocados[p.SKU] = struct{}{}
	}
	var skus, nombres, categorias, unidades []string
	for _, p := range unicos {
		skus = append(skus, p.SKU)
		nombres = append(nombres, p.Nombre)
		categorias = append(categorias, p.Categoria)
		unidades = append(unidades, p.Unidad)
	}
	if _, err := tx.Exec(ctx, sqlUpsertProductos, skus, nombres, categorias, unidades); err != nil {
		return fmt.Errorf("upsert de productos: %w", err)
	}
	return nil
}

// upsertOfertas devuelve los SKU que vinieron: nunca nil, aunque no venga ninguno.
func upsertOfertas(ctx context.Context, tx pgx.Tx, proveedorID int64, ofertas []ports.OfertaCambio, tocados map[string]struct{}) ([]string, error) {
	unicas := map[string]ports.OfertaCambio{}
	for _, o := range ofertas {
		unicas[o.ProductoSKU] = o
		tocados[o.ProductoSKU] = struct{}{}
	}
	skus := make([]string, 0, len(unicas))
	if len(unicas) == 0 {
		return skus, nil
	}
	var (
		precios, descuentos, fletes []float64
		minimas, stocks             []int32
	)
	for sku, o := range unicas {
		skus = append(skus, sku)
		precios = append(precios, o.PrecioUnitario)
		descuentos = append(descuentos, o.DescuentoPct)
		minimas = append(minimas, int32(o.CantidadMinimaDescuento))
		fletes = append(fletes, o.Flete)
		stocks = append(stocks, int32(o.StockDisponible))
	}
	if _, err := tx.Exec(ctx, sqlUpsertOfertas, proveedorID, skus, precios, descuentos, minimas, fletes, stocks); err != nil {
		return nil, fmt.Errorf("upsert de ofertas: %w", err)
	}
	return skus, nil
}

func borrarOfertas(ctx context.Context, tx pgx.Tx, sql string, proveedorID int64, skus []string, tocados map[string]struct{}) error {
	filas, err := tx.Query(ctx, sql, proveedorID, skus)
	if err != nil {
		return err
	}
	borrados, err := pgx.CollectRows(filas, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, sku := range borrados {
		tocados[sku] = struct{}{}
	}
	return nil
}

func ordenados(conjunto map[string]struct{}) []string {
	lista := make([]string, 0, len(conjunto))
	for s := range conjunto {
		lista = append(lista, s)
	}
	sort.Strings(lista)
	return lista
}
