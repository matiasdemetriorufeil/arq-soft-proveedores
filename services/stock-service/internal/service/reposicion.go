package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
)

// factorReposicion fija la cantidad a reponer: `minimo × 2 − disponible`, que
// deja el stock en el doble del mínimo. RN-14 no define la cantidad, así que se
// elige una, se documenta y queda como el punto obvio para cambiarla si el
// negocio lo pide.
const factorReposicion = 2

// ErrReposicionDeshabilitada indica que el ciclo se pidió sin cliente de
// proveedores configurado (sin PROVEEDORES_URL).
var ErrReposicionDeshabilitada = errors.New("la reposición automática está deshabilitada: falta configurar proveedores-service")

// Resumen es lo que el ciclo devuelve para que el llamador lo loguee. La capa
// de negocio no escribe logs: devuelve el resultado y quien la invoca decide
// cómo registrarlo.
type Resumen struct {
	// BajoMinimo es cuántos SKUs estaban por debajo de su mínimo (RN-14).
	BajoMinimo int
	// Creadas son las órdenes nuevas en BORRADOR (CA-14.1).
	Creadas int
	// Salteadas son los SKUs que ya tenían una orden abierta (CA-14.2).
	Salteadas int
	// SinProveedor son las órdenes que se crearon sin proveedor sugerido
	// porque proveedores-service no pudo responder quién es el más barato.
	SinProveedor int
	// Fallidas son los SKUs que no se pudieron procesar. Un SKU que falla no
	// corta el ciclo: se registra y se sigue con los demás.
	Fallidas int
	// Problemas describe cada falla, en el orden en que ocurrieron.
	Problemas []string
}

// CicloDeReposicion busca los SKUs que cayeron bajo su mínimo y crea una orden
// de reposición en BORRADOR para cada uno que no tenga ya una abierta,
// dirigida al proveedor de menor costo total (RN-14, RN-16, CA-14.1, CA-14.2).
//
// No envía nada: la orden queda en BORRADOR. Quien la pasa a SOLICITADA es
// quien le hace el pedido al proveedor (ADR-009), que todavía no existe.
//
// Un SKU que falla no corta el ciclo: queda contado en el resumen y se
// reintenta en la vuelta siguiente.
func (s *ServicioOrdenes) CicloDeReposicion(ctx context.Context) (Resumen, error) {
	var res Resumen
	if s.prov == nil {
		return res, ErrReposicionDeshabilitada
	}

	bajos, err := s.repo.SKUsBajoMinimo(ctx)
	if err != nil {
		return res, err
	}
	res.BajoMinimo = len(bajos)

	for _, st := range bajos {
		// El contexto cancelado es el apagado del servicio, no una falla del
		// SKU: se corta el ciclo y se devuelve lo hecho hasta acá.
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		s.reponerSKU(ctx, st, &res)
	}
	return res, nil
}

// reponerSKU procesa un solo SKU y acumula el resultado en el resumen.
func (s *ServicioOrdenes) reponerSKU(ctx context.Context, st repository.Stock, res *Resumen) {
	abierta, err := s.repo.OrdenAbiertaPorSKU(ctx, st.SKU)
	if err != nil {
		res.Fallidas++
		res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", st.SKU, err))
		return
	}
	if abierta {
		// CA-14.2: ya hay una orden sin cerrar para este producto.
		res.Salteadas++
		return
	}

	cantidad := cantidadAReponer(st)
	if EsSKUDeFAV(st.SKU) && cantidad > fav.MaxUnidadesPorVariante {
		// FAV acepta hasta 10 unidades por variante en un pedido: se repone
		// eso, y lo que falte lo pide el ciclo siguiente.
		cantidad = fav.MaxUnidadesPorVariante
	}
	id, err := nuevoUUID()
	if err != nil {
		res.Fallidas++
		res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", st.SKU, err))
		return
	}

	orden := repository.Orden{
		ID:       id,
		SKU:      st.SKU,
		Cantidad: cantidad,
		Estado:   EstadoBorrador,
	}

	// RN-16: el proveedor de menor costo total para la cantidad a reponer. Si
	// no se puede saber quién es, la orden se crea igual —el faltante existe
	// aunque proveedores-service esté caído— sin proveedor y con el motivo a la
	// vista, y el ciclo siguiente vuelve a intentarlo.
	//
	// Una prenda de FAV solo la vende FAV (ADR-009): el proveedor es FAV sin
	// preguntar, y la orden sale como pedido a FAV.
	if EsSKUDeFAV(st.SKU) && s.favID != 0 {
		provID, nombre := s.favID, s.favNombre
		orden.ProveedorID, orden.ProveedorNombre = &provID, &nombre
		s.crearOrden(ctx, &orden, res)
		return
	}
	rec, err := s.prov.MasBarato(ctx, st.SKU, cantidad)
	if err != nil {
		motivo := motivoSinProveedor(err)
		orden.Motivo = &motivo
		res.SinProveedor++
	} else {
		orden.ProveedorID = &rec.ProveedorID
		nombre := rec.ProveedorNombre
		costo := rec.CostoTotal
		orden.ProveedorNombre = &nombre
		orden.CostoTotal = &costo
	}

	s.crearOrden(ctx, &orden, res)
}

// crearOrden graba la orden y acumula el resultado en el resumen.
func (s *ServicioOrdenes) crearOrden(ctx context.Context, orden *repository.Orden, res *Resumen) {
	switch err := s.repo.CrearOrden(ctx, orden); {
	case err == nil:
		res.Creadas++
	case errors.Is(err, repository.ErrOrdenAbiertaExistente):
		// Otra réplica corrió el ciclo a la vez y ganó la carrera: el índice
		// único parcial la rechazó. Es el mismo caso de CA-14.2, no un error.
		res.Salteadas++
		if orden.Motivo != nil {
			res.SinProveedor--
		}
	default:
		res.Fallidas++
		res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", orden.SKU, err))
		if orden.Motivo != nil {
			res.SinProveedor--
		}
	}
}

// cantidadAReponer deja el stock en el doble del mínimo, y nunca pide menos de
// una unidad.
func cantidadAReponer(st repository.Stock) int {
	cantidad := st.Minimo*factorReposicion - st.Disponible
	if cantidad < 1 {
		cantidad = 1
	}
	return cantidad
}

// motivoSinProveedor traduce la falla al texto que queda guardado en la orden.
// Es lo que ve el operador cuando abre una orden sin proveedor sugerido.
func motivoSinProveedor(err error) string {
	switch {
	case errors.Is(err, proveedores.ErrSinOfertas):
		return "ningún proveedor con stock suficiente para la cantidad a reponer"
	case errors.Is(err, proveedores.ErrProductoNoEncontrado):
		return "proveedores-service no conoce este producto (404): ningún proveedor lo ofrece"
	case errors.Is(err, proveedores.ErrNoAutorizado):
		return "proveedores-service rechazó la clave (401): revisar PROVEEDORES_API_KEY"
	case errors.Is(err, proveedores.ErrNoDisponible):
		return "proveedores-service no respondió a tiempo: se reintenta en el ciclo siguiente"
	default:
		return "no se pudo obtener el proveedor recomendado: " + err.Error()
	}
}
