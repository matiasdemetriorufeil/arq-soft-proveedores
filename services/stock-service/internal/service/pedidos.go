package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
)

// PrefijoSKUDeFAV marca las prendas de FAV: el SKU es `FAV-<varianteId>`, el
// mismo que arma import-service (ADR-009).
const PrefijoSKUDeFAV = "FAV-"

// Estados del pedido en FAV (su contrato v1.1.0).
const (
	PedidoPendienteDePago = "PENDIENTE_DE_PAGO"
	PedidoPagado          = "PAGADO"
	PedidoVencido         = "VENCIDO"
	PedidoAVerificar      = "A_VERIFICAR"
)

// EsSKUDeFAV dice si el producto es una prenda de FAV. Cada variante de FAV
// solo la vende FAV, así que su orden de reposición siempre es un pedido a FAV.
func EsSKUDeFAV(sku string) bool { return strings.HasPrefix(sku, PrefijoSKUDeFAV) }

// Errores de negocio de los pedidos.
var (
	// ErrOrdenNoEsDeFAV: se pidió enviar a FAV una orden de otro producto.
	ErrOrdenNoEsDeFAV = errors.New("la orden no es de una prenda de FAV")
	// ErrEnvioFallido: FAV no aceptó el pedido. La orden sigue en BORRADOR
	// con el motivo a la vista; el detalle viaja en EnvioFallidoError.
	ErrEnvioFallido = errors.New("no se pudo hacer el pedido a FAV")
)

// EnvioFallidoError dice por qué no salió el pedido y si se puede reintentar
// con la misma clave (FAV no disponible) o hace falta corregir algo.
type EnvioFallidoError struct {
	Motivo       string
	Reintentable bool
	causa        error
}

func (e *EnvioFallidoError) Error() string        { return e.Motivo }
func (e *EnvioFallidoError) Unwrap() error        { return e.causa }
func (e *EnvioFallidoError) Is(target error) bool { return target == ErrEnvioFallido }

// RepositorioPedidos es lo que la capa de negocio necesita de la capa de
// datos para los pedidos. Declarada del lado del consumidor, como las otras.
type RepositorioPedidos interface {
	OrdenPorID(ctx context.Context, id string) (repository.Orden, error)
	NuevoIntentoDePedido(ctx context.Context, id string) (repository.Orden, error)
	RegistrarPedido(ctx context.Context, id, clave string, p repository.DatosPedido) (repository.Orden, error)
	RegistrarFalloDeEnvio(ctx context.Context, id, motivo string, liberarClave bool) error
	OrdenesParaEnviar(ctx context.Context) ([]repository.Orden, error)
	OrdenesEnSeguimiento(ctx context.Context) ([]repository.Orden, error)
	OrdenPorPedidoID(ctx context.Context, pedidoID string) (repository.Orden, error)
	RegistrarAviso(ctx context.Context, eventoID, pedidoID, estado string) (bool, error)
	ActualizarEstadoDelPedido(ctx context.Context, id, estadoFAV string) error
	EnTransaccion(ctx context.Context, fn func(repository.Tx) error) error
	AvanzarOrden(ctx context.Context, tx repository.Tx, id, desde, hacia, estadoFAV string) (repository.Orden, error)
	SumarStock(ctx context.Context, tx repository.Tx, sku string, cantidad int) error
}

// ClienteFAV es lo que la capa de negocio necesita de FAV. El transporte, los
// timeouts y los reintentos son de internal/fav.
type ClienteFAV interface {
	CrearPedido(ctx context.Context, clave string, p fav.PedidoNuevo) (fav.Pedido, error)
	ConsultarPedido(ctx context.Context, pedidoID string) (fav.Pedido, error)
}

// ServicioPedidos le hace los pedidos de reposición a FAV y sigue su estado
// por el webhook y por la consulta de respaldo (ADR-009).
type ServicioPedidos struct {
	repo            RepositorioPedidos
	fav             ClienteFAV
	notificacionURL string
	// enSegundoPlano corre el procesamiento de un aviso después de responder
	// al webhook. En los tests se reemplaza por una llamada directa.
	enSegundoPlano func(func())
}

// NuevoServicioPedidos arma el servicio. notificacionURL es nuestro receptor
// del webhook tal como lo alcanza FAV; vacía, el pedido sale sin avisos y el
// estado se sigue solo con la consulta de respaldo.
func NuevoServicioPedidos(repo RepositorioPedidos, cliente ClienteFAV, notificacionURL string) *ServicioPedidos {
	return &ServicioPedidos{repo: repo, fav: cliente, notificacionURL: notificacionURL,
		enSegundoPlano: func(f func()) { go f() }}
}

// EnviarOrden hace el pedido a FAV de una orden en BORRADOR (ADR-009):
// Idempotency-Key `<id>-<intento>`, referenciaExterna = id de la orden y
// notificacionUrl = nuestro receptor. Con 201 la orden pasa a SOLICITADA.
func (s *ServicioPedidos) EnviarOrden(ctx context.Context, id string) (repository.Orden, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return repository.Orden{}, fmt.Errorf("%w: el id de la orden es obligatorio", ErrParametroInvalido)
	}
	o, err := s.repo.OrdenPorID(ctx, id)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return repository.Orden{}, ErrOrdenNoEncontrada
	}
	if err != nil {
		return repository.Orden{}, err
	}
	return s.enviar(ctx, o)
}

func (s *ServicioPedidos) enviar(ctx context.Context, o repository.Orden) (repository.Orden, error) {
	if !EsSKUDeFAV(o.SKU) {
		return o, fmt.Errorf("%w: '%s' no es una prenda de FAV", ErrOrdenNoEsDeFAV, o.SKU)
	}
	if o.Estado != EstadoBorrador {
		return o, &TransicionInvalidaError{ID: o.ID, Desde: o.Estado, Hacia: EstadoSolicitada,
			Permitidos: transicionesPermitidas[o.Estado]}
	}
	if o.Cantidad > fav.MaxUnidadesPorVariante {
		return o, fmt.Errorf("%w: FAV acepta hasta %d unidades por variante y la orden pide %d",
			ErrParametroInvalido, fav.MaxUnidadesPorVariante, o.Cantidad)
	}

	if o.ClavePedido == nil {
		nueva, err := s.repo.NuevoIntentoDePedido(ctx, o.ID)
		if errors.Is(err, repository.ErrNoEncontrado) {
			// Otro proceso armó el intento o movió la orden en el medio.
			if nueva, err = s.repo.OrdenPorID(ctx, o.ID); err != nil {
				return o, err
			}
			if nueva.Estado != EstadoBorrador || nueva.ClavePedido == nil {
				return nueva, nil
			}
		} else if err != nil {
			return o, err
		}
		o = nueva
	}
	clave := *o.ClavePedido

	pedido, err := s.fav.CrearPedido(ctx, clave, fav.PedidoNuevo{
		ReferenciaExterna: o.ID,
		NotificacionURL:   s.notificacionURL,
		Items:             []fav.ItemPedido{{VarianteID: strings.TrimPrefix(o.SKU, PrefijoSKUDeFAV), Cantidad: o.Cantidad}},
	})
	if err != nil {
		motivo, reintentable := motivoDeEnvio(err)
		// Si se reintenta, va con la misma clave: si FAV llegó a crear el
		// pedido, devuelve el mismo en lugar de uno nuevo.
		if errReg := s.repo.RegistrarFalloDeEnvio(ctx, o.ID, motivo, !reintentable); errReg != nil {
			return o, errReg
		}
		o.Motivo = &motivo
		return o, &EnvioFallidoError{Motivo: motivo, Reintentable: reintentable, causa: err}
	}

	registrada, err := s.repo.RegistrarPedido(ctx, o.ID, clave, repository.DatosPedido{
		PedidoID: pedido.PedidoID, Estado: pedido.Estado, Total: pedido.Total, PagoVenceEn: pedido.Pago.VenceEn,
	})
	if errors.Is(err, repository.ErrNoEncontrado) {
		// Otro envío con la misma clave lo registró primero: FAV devolvió el
		// mismo pedido a los dos.
		return s.repo.OrdenPorID(ctx, o.ID)
	}
	return registrada, err
}

// motivoDeEnvio traduce la falla al texto que queda en la orden, y dice si se
// reintenta con la misma clave.
func motivoDeEnvio(err error) (string, bool) {
	var e *fav.ErrorFAV
	detalle := ""
	if errors.As(err, &e) && e.Detalle != "" {
		detalle = ": " + e.Detalle
	}
	switch {
	case errors.Is(err, fav.ErrNoDisponible):
		return "FAV no respondió: se reintenta con la misma Idempotency-Key", true
	case errors.Is(err, fav.ErrRespuestaInvalida):
		return "la respuesta de FAV no cumple su contrato: se reintenta con la misma Idempotency-Key", true
	case errors.Is(err, fav.ErrNoAutenticado):
		return "FAV rechazó la clave del grupo (401): revisar FAV_API_KEY", true
	case errors.Is(err, fav.ErrStockInsuficiente):
		return "FAV no tiene stock suficiente (409)" + detalle + "; se reintenta en el próximo ciclo con un intento nuevo", false
	default:
		return "FAV rechazó el pedido: " + err.Error(), false
	}
}

// ResumenPedidos es lo que devuelve un ciclo de pedidos, para el log.
type ResumenPedidos struct {
	Enviadas  int
	Fallidas  int
	Seguidas  int
	Cambiadas int
	Problemas []string
}

// EnviarPendientes hace el pedido de cada orden de FAV que sigue en BORRADOR.
// Una que falla no corta el ciclo.
func (s *ServicioPedidos) EnviarPendientes(ctx context.Context) (ResumenPedidos, error) {
	var res ResumenPedidos
	ordenes, err := s.repo.OrdenesParaEnviar(ctx)
	if err != nil {
		return res, err
	}
	for _, o := range ordenes {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		final, err := s.enviar(ctx, o)
		switch {
		case err != nil:
			res.Fallidas++
			res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", o.ID, err))
		case final.Estado == EstadoSolicitada:
			res.Enviadas++
		}
	}
	return res, nil
}

// Seguimiento es la consulta de respaldo: le pregunta a FAV por cada pedido
// que todavía puede cambiar, por si un aviso se perdió (ADR-009).
func (s *ServicioPedidos) Seguimiento(ctx context.Context) (ResumenPedidos, error) {
	var res ResumenPedidos
	ordenes, err := s.repo.OrdenesEnSeguimiento(ctx)
	if err != nil {
		return res, err
	}
	for _, o := range ordenes {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Seguidas++
		p, err := s.fav.ConsultarPedido(ctx, *o.PedidoID)
		if err != nil {
			res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", o.ID, err))
			continue
		}
		cambio, err := s.aplicarEstado(ctx, o, p.Estado)
		if err != nil {
			res.Problemas = append(res.Problemas, fmt.Sprintf("%s: %v", o.ID, err))
			continue
		}
		if cambio {
			res.Cambiadas++
		}
	}
	return res, nil
}

// RecibirAviso registra el aviso ya verificado y, si es nuevo, lo procesa en
// segundo plano: el contrato pide responder 2xx en menos de 5 s, y procesarlo
// incluye consultar a FAV. Devuelve false si el aviso ya se había recibido.
//
// Si el procesamiento falla después de responder, la consulta de respaldo lo
// corrige en su próxima vuelta: el estado vigente está siempre en FAV.
func (s *ServicioPedidos) RecibirAviso(ctx context.Context, a fav.Aviso, alTerminar func(error)) (bool, error) {
	nuevo, err := s.repo.RegistrarAviso(ctx, a.EventoID, a.Pedido.PedidoID, a.Pedido.Estado)
	if err != nil || !nuevo {
		return nuevo, err
	}
	// El procesamiento sigue aunque se corte la request del webhook.
	ctxFondo := context.WithoutCancel(ctx)
	s.enSegundoPlano(func() {
		err := s.ProcesarAviso(ctxFondo, a)
		if alTerminar != nil {
			alTerminar(err)
		}
	})
	return true, nil
}

// ProcesarAviso confirma el estado con GET /v1/pedidos/{pedidoId} antes de
// mover la orden: el contrato dice que la fuente es la consulta, no el aviso.
func (s *ServicioPedidos) ProcesarAviso(ctx context.Context, a fav.Aviso) error {
	o, err := s.repo.OrdenPorPedidoID(ctx, a.Pedido.PedidoID)
	if errors.Is(err, repository.ErrNoEncontrado) {
		return fmt.Errorf("%w: ninguna orden tiene el pedido %s", ErrOrdenNoEncontrada, a.Pedido.PedidoID)
	}
	if err != nil {
		return err
	}
	p, err := s.fav.ConsultarPedido(ctx, a.Pedido.PedidoID)
	if err != nil {
		return fmt.Errorf("confirmar el aviso con FAV: %w", err)
	}
	_, err = s.aplicarEstado(ctx, o, p.Estado)
	return err
}

// destinoPorEstadoFAV es la tabla de ADR-009: a qué estado pasa nuestra orden
// según el estado del pedido en FAV y dónde está hoy. Lo que no figura no
// mueve la orden: un aviso con un estado anterior al que ya tenemos se ignora.
func destinoPorEstadoFAV(actual, estadoFAV string) string {
	switch estadoFAV {
	case PedidoPendienteDePago:
		return ""
	case PedidoPagado:
		if actual == EstadoSolicitada {
			return EstadoConfirmada
		}
	case PedidoVencido:
		if actual == EstadoSolicitada {
			return EstadoVencida
		}
	case PedidoAVerificar:
		if actual == EstadoSolicitada || actual == EstadoVencida {
			return EstadoEnRevision
		}
	default:
		// Un estado que no conocemos: "en revisión", sin acción automática,
		// como pide la política de compatibilidad del contrato.
		if actual == EstadoSolicitada {
			return EstadoEnRevision
		}
	}
	return ""
}

// aplicarEstado mueve la orden según el estado vigente en FAV. Al confirmar,
// suma el stock entrante en la misma transacción. Devuelve si la orden cambió.
func (s *ServicioPedidos) aplicarEstado(ctx context.Context, o repository.Orden, estadoFAV string) (bool, error) {
	hacia := destinoPorEstadoFAV(o.Estado, estadoFAV)
	if hacia == "" {
		if o.PedidoEstado == nil || *o.PedidoEstado != estadoFAV {
			return false, s.repo.ActualizarEstadoDelPedido(ctx, o.ID, estadoFAV)
		}
		return false, nil
	}
	if err := validarTransicion(o.ID, o.Estado, hacia); err != nil {
		return false, err
	}
	err := s.repo.EnTransaccion(ctx, func(tx repository.Tx) error {
		avanzada, err := s.repo.AvanzarOrden(ctx, tx, o.ID, o.Estado, hacia, estadoFAV)
		if err != nil {
			return err
		}
		if hacia == EstadoConfirmada {
			return s.repo.SumarStock(ctx, tx, avanzada.SKU, avanzada.Cantidad)
		}
		return nil
	})
	if errors.Is(err, repository.ErrNoEncontrado) {
		// Otro proceso (el webhook o la consulta de respaldo) ya la movió.
		return false, nil
	}
	return err == nil, err
}
