package domain

import (
	"fmt"
	"math"
	"sort"
)

// CostoTotal calcula el costo de comprar `cantidad` unidades de la oferta:
//
//	precio_unitario × cantidad × (1 − descuento_pct/100) + flete
//
// El descuento solo aplica si cantidad >= CantidadMinimaDescuento.
// El resultado se redondea a 2 decimales. Devuelve también si se aplicó el descuento.
func CostoTotal(o Oferta, cantidad int) (float64, bool) {
	aplica := o.DescuentoPct > 0 && cantidad >= o.CantidadMinimaDescuento
	factor := 1.0
	if aplica {
		factor = 1 - o.DescuentoPct/100
	}
	total := o.PrecioUnitario*float64(cantidad)*factor + o.Flete
	return redondear2(total), aplica
}

// Rankear evalúa las ofertas para la cantidad pedida, descarta las que no
// tienen stock suficiente, las ordena por costo total ascendente (desempate
// por nombre de proveedor y después por proveedor_id) y devuelve como máximo
// `limite` recomendaciones. Nunca devuelve nil: sin ofertas válidas devuelve
// una lista vacía.
func Rankear(ofertas []Oferta, cantidad, limite int) ([]Recomendacion, error) {
	if cantidad <= 0 {
		return nil, fmt.Errorf("%w: cantidad debe ser un entero mayor a 0", ErrParametroInvalido)
	}
	if limite <= 0 || limite > LimiteMaximo {
		return nil, fmt.Errorf("%w: limite debe estar entre 1 y %d", ErrParametroInvalido, LimiteMaximo)
	}

	recs := make([]Recomendacion, 0, len(ofertas))
	for _, o := range ofertas {
		if o.StockDisponible < cantidad {
			continue
		}
		total, aplica := CostoTotal(o, cantidad)
		recs = append(recs, Recomendacion{
			ProveedorID:       o.ProveedorID,
			ProveedorNombre:   o.ProveedorNombre,
			PrecioUnitario:    o.PrecioUnitario,
			DescuentoPct:      o.DescuentoPct,
			DescuentoAplicado: aplica,
			Flete:             o.Flete,
			StockDisponible:   o.StockDisponible,
			CostoTotal:        total,
		})
	}

	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.CostoTotal != b.CostoTotal {
			return a.CostoTotal < b.CostoTotal
		}
		if a.ProveedorNombre != b.ProveedorNombre {
			return a.ProveedorNombre < b.ProveedorNombre
		}
		return a.ProveedorID < b.ProveedorID
	})

	if len(recs) > limite {
		recs = recs[:limite]
	}
	return recs, nil
}

// redondear2 redondea a 2 decimales (mitad hacia arriba). Primero se lleva a
// 6 decimales para absorber el ruido de punto flotante: por ejemplo,
// 1654378.915 se representa como 1654378.91499999... y sin este paso
// quedaría en .91 en lugar de .92.
func redondear2(v float64) float64 {
	limpio := math.Round(v*1e6) / 1e4
	return math.Round(limpio) / 100
}
