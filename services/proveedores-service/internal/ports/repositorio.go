// Package ports define los puertos (interfaces) que el núcleo necesita del
// mundo exterior. Los adaptadores los implementan.
package ports

import (
	"context"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
)

// OfertasRepo es el puerto de lectura de ofertas.
type OfertasRepo interface {
	// OfertasPorProducto devuelve todas las ofertas del SKU. Si el SKU no
	// existe devuelve domain.ErrProductoNoEncontrado; si existe pero no tiene
	// ofertas devuelve una lista vacía y nil. Cualquier otro error indica que
	// el almacenamiento no está disponible.
	OfertasPorProducto(ctx context.Context, sku string) ([]domain.Oferta, error)
}

// Salud es el puerto que usa el chequeo de readiness.
type Salud interface {
	Ping(ctx context.Context) error
}
