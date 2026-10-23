// Package postgres implementa los puertos de persistencia sobre PostgreSQL con pgx.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// TimeoutConsulta es el tiempo máximo de cada consulta a la base.
const TimeoutConsulta = 2 * time.Second

// Repo implementa ports.OfertasRepo y ports.Salud.
type Repo struct {
	pool *pgxpool.Pool
}

var (
	_ ports.OfertasRepo = (*Repo)(nil)
	_ ports.Salud       = (*Repo)(nil)
)

// NuevoRepo crea el repositorio sobre un pool ya abierto.
func NuevoRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// NuevoPool abre un pool pgx a partir de DATABASE_URL.
func NuevoPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL inválida: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = TimeoutConsulta
	// Instrumenta el acceso a la base: cada consulta —y cada intento de conexión
	// que haga el pool— abre su span dentro de la traza de la request (ver
	// tracer.go).
	cfg.ConnConfig.Tracer = NuevoTracer(nil)
	return pgxpool.NewWithConfig(ctx, cfg)
}

// El LEFT JOIN desde productos permite distinguir en una sola consulta
// "el SKU no existe" (0 filas) de "existe pero no tiene ofertas" (1 fila con
// columnas de oferta en NULL).
const queryOfertas = `
SELECT o.proveedor_id,
       pr.nombre,
       o.precio_unitario::float8,
       o.descuento_pct::float8,
       o.cantidad_minima_descuento,
       o.flete::float8,
       o.stock_disponible
FROM productos p
LEFT JOIN ofertas o      ON o.producto_sku = p.sku
LEFT JOIN proveedores pr ON pr.id = o.proveedor_id
WHERE p.sku = $1`

// OfertasPorProducto devuelve las ofertas del SKU o domain.ErrProductoNoEncontrado.
func (r *Repo) OfertasPorProducto(ctx context.Context, sku string) ([]domain.Oferta, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	rows, err := r.pool.Query(ctx, queryOfertas, sku)
	if err != nil {
		return nil, fmt.Errorf("consultar ofertas: %w", err)
	}
	defer rows.Close()

	existe := false
	ofertas := make([]domain.Oferta, 0)
	for rows.Next() {
		existe = true
		var (
			provID              *int64
			provNombre          *string
			precio, desc, flete *float64
			minDesc, stock      *int32
		)
		if err := rows.Scan(&provID, &provNombre, &precio, &desc, &minDesc, &flete, &stock); err != nil {
			return nil, fmt.Errorf("leer oferta: %w", err)
		}
		if provID == nil {
			continue // producto sin ofertas
		}
		ofertas = append(ofertas, domain.Oferta{
			ProveedorID:             *provID,
			ProveedorNombre:         *provNombre,
			ProductoSKU:             sku,
			PrecioUnitario:          *precio,
			DescuentoPct:            *desc,
			CantidadMinimaDescuento: int(*minDesc),
			Flete:                   *flete,
			StockDisponible:         int(*stock),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorrer ofertas: %w", err)
	}
	if !existe {
		return nil, domain.ErrProductoNoEncontrado
	}
	return ofertas, nil
}

// Ping verifica que la base responda dentro del timeout.
func (r *Repo) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return r.pool.Ping(ctx)
}
