// Package rabbitmq es el adaptador de entrada de los eventos: consume
// ListaPreciosActualizada y lo traduce a un cambio que entienda el núcleo. Los
// pasos están en docs/contracts/eventos.md.
package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

const (
	esperaReconexionMinima = time.Second
	esperaReconexionMaxima = 30 * time.Second
	// timeoutProceso es lo máximo que puede tardar un evento en aplicarse.
	timeoutProceso = 30 * time.Second
	// timeoutInvalidacion acota la invalidación después del commit. Si no llega,
	// queda el TTL de la caché como red de seguridad (ADR-007).
	timeoutInvalidacion = time.Second
	// esperaReintentoPorDefecto: entre entregas se espera, porque sin espera las
	// tres se gastan en milisegundos y un corte de pocos segundos de la base
	// mandaría el evento a la DLQ. Mientras tanto la cola no avanza (prefetch 1),
	// que es lo que mantiene el orden.
	esperaReintentoPorDefecto = 5 * time.Second
)

// Config agrupa las dependencias del consumidor. Invalidador y Aviso son
// opcionales: el servicio puede correr sin caché o sin búsqueda.
type Config struct {
	URL             string
	Aplicador       ports.AplicadorListaPrecios
	Invalidador     ports.InvalidadorOfertas
	Aviso           ports.AvisoIndexacion
	Logger          *slog.Logger
	EsperaReintento time.Duration
}

// Consumidor lee la cola y se reconecta solo.
type Consumidor struct {
	cfg    Config
	log    *slog.Logger
	tracer trace.Tracer
}

// NuevoConsumidor no se conecta todavía: la conexión la abre Correr. Así el
// servicio arranca aunque RabbitMQ no esté.
func NuevoConsumidor(cfg Config) *Consumidor {
	if cfg.EsperaReintento <= 0 {
		cfg.EsperaReintento = esperaReintentoPorDefecto
	}
	return &Consumidor{cfg: cfg, log: cfg.Logger, tracer: otel.Tracer("proveedores-service/rabbitmq")}
}

// Correr consume hasta que se cancele ctx. Una caída del broker, o una topología
// que no coincide, se loguea y se reintenta: nunca tira al servicio.
func (c *Consumidor) Correr(ctx context.Context) {
	espera := esperaReconexionMinima
	for ctx.Err() == nil {
		inicio := time.Now()
		err := c.sesion(ctx)
		if ctx.Err() != nil {
			return
		}
		c.log.Warn("consumidor sin conexión a RabbitMQ; reintentando", "error", err.Error(), "espera", espera.String())
		if time.Since(inicio) > esperaReconexionMaxima {
			espera = esperaReconexionMinima
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(espera):
		}
		espera = min(2*espera, esperaReconexionMaxima)
	}
}

// sesion dura lo que dura una conexión.
func (c *Consumidor) sesion(ctx context.Context) error {
	conn, err := amqp.DialConfig(c.cfg.URL, amqp.Config{
		Dial:      amqp.DefaultDial(5 * time.Second),
		Heartbeat: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("conectar: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("abrir canal: %w", err)
	}
	if err := mensajeria.DeclararTopologia(ch); err != nil {
		return err
	}
	// De a uno: junto con x-single-active-consumer, es lo que garantiza que no se
	// aplique un evento nuevo antes que uno anterior.
	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("configurar prefetch: %w", err)
	}
	entregas, err := ch.Consume(mensajeria.ColaListaPrecios, etiquetaConsumidor(), false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consumir: %w", err)
	}
	c.log.Info("consumidor conectado; la cola entrega solo a la réplica activa", "cola", mensajeria.ColaListaPrecios)
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-entregas:
			if !ok {
				return errors.New("el broker cerró la entrega")
			}
			c.procesar(ctx, d)
		}
	}
}

func etiquetaConsumidor() string {
	nombre, err := os.Hostname()
	if err != nil {
		nombre = "proveedores-service"
	}
	return fmt.Sprintf("%s-%d", nombre, time.Now().UnixNano())
}

// procesar decide qué hacer con una entrega. No devuelve error: la respuesta al
// broker (Ack o Nack) es su resultado.
func (c *Consumidor) procesar(ctx context.Context, d amqp.Delivery) {
	sobre, err := mensajeria.LeerSobre(d.Body)
	if err != nil {
		c.aFallidos(ctx, d, err)
		return
	}

	spanCtx, span := c.tracer.Start(sobre.ContextoDeTraza(ctx), "consumir "+sobre.Tipo,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("evento_id", sobre.EventoID)))
	defer span.End()

	cambio, err := leerCambio(sobre)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "evento no procesable")
		c.aFallidos(spanCtx, d, fmt.Errorf("evento %s: %w", sobre.EventoID, err))
		return
	}
	span.SetAttributes(attribute.Int64("proveedor_id", cambio.ProveedorID), attribute.Int64("secuencia", cambio.Secuencia))

	// Sin la cancelación del apagado: lo que ya empezó se termina, y recién
	// después el bucle de sesion ve el apagado.
	aplicarCtx, cancel := context.WithTimeout(context.WithoutCancel(spanCtx), timeoutProceso)
	defer cancel()
	res, err := c.cfg.Aplicador.AplicarListaPrecios(aplicarCtx, cambio)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "no se pudo aplicar")
		c.reintentar(ctx, spanCtx, d, sobre.EventoID, err)
		return
	}
	if !res.Aplicado {
		c.log.InfoContext(spanCtx, "evento descartado", "evento_id", sobre.EventoID, "motivo", res.Motivo,
			"proveedor_id", cambio.ProveedorID, "secuencia", cambio.Secuencia)
		c.confirmar(spanCtx, d)
		return
	}

	c.invalidar(aplicarCtx, res.SKUs)
	if c.cfg.Aviso != nil {
		c.cfg.Aviso.AvisarPendientes()
	}
	c.log.InfoContext(spanCtx, "evento aplicado", "evento_id", sobre.EventoID,
		"proveedor_id", cambio.ProveedorID, "secuencia", cambio.Secuencia, "skus", len(res.SKUs))
	c.confirmar(spanCtx, d)
}

// invalidar nunca hace fallar al evento: el cambio ya está confirmado en la base
// y una caché caída no puede frenar los precios.
func (c *Consumidor) invalidar(ctx context.Context, skus []string) {
	if c.cfg.Invalidador == nil || len(skus) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutInvalidacion)
	defer cancel()
	if err := c.cfg.Invalidador.InvalidarOfertas(ctx, skus); err != nil {
		c.log.WarnContext(ctx, "no se pudo invalidar la caché: queda el TTL", "skus", len(skus), "error", err.Error())
	}
}

func (c *Consumidor) confirmar(ctx context.Context, d amqp.Delivery) {
	if err := d.Ack(false); err != nil {
		// El evento vuelve a entregarse y el duplicado se descarta.
		c.log.WarnContext(ctx, "no se pudo confirmar el mensaje", "error", err.Error())
	}
}

func (c *Consumidor) aFallidos(ctx context.Context, d amqp.Delivery, causa error) {
	c.log.ErrorContext(ctx, "mensaje no procesable: va a la cola de fallidos", "message_id", d.MessageId, "error", causa.Error())
	if err := d.Nack(false, false); err != nil {
		c.log.WarnContext(ctx, "no se pudo rechazar el mensaje", "error", err.Error())
	}
}

// reintentar devuelve el mensaje a la cola. La cola quorum recién lo descarta
// después de la cuarta entrega (x-delivery-limit cuenta las devoluciones), así
// que la tercera entrega fallida la manda a la DLQ el consumidor: es lo que dice
// el contrato.
func (c *Consumidor) reintentar(ctx, spanCtx context.Context, d amqp.Delivery, eventoID string, causa error) {
	intento := entregasPrevias(d) + 1
	if intento >= mensajeria.IntentosMaximos {
		c.aFallidos(spanCtx, d, fmt.Errorf("evento %s, entrega %d: %w", eventoID, intento, causa))
		return
	}
	espera := time.Duration(intento) * c.cfg.EsperaReintento
	c.log.WarnContext(spanCtx, "no se pudo aplicar el evento; se reintenta", "evento_id", eventoID,
		"entrega", intento, "espera", espera.String(), "error", causa.Error())
	select {
	case <-ctx.Done():
		// Apagado: al cerrar la conexión, el broker lo vuelve a poner en la cola.
		return
	case <-time.After(espera):
	}
	if err := d.Nack(false, true); err != nil {
		c.log.WarnContext(spanCtx, "no se pudo devolver el mensaje", "error", err.Error())
	}
}

// entregasPrevias lee el contador que la cola quorum agrega desde la segunda
// entrega.
func entregasPrevias(d amqp.Delivery) int64 {
	switch n := d.Headers["x-delivery-count"].(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

// leerCambio valida el tipo, la versión y los datos, y los pasa al formato del
// núcleo. Un error acá no se arregla reintentando.
func leerCambio(s mensajeria.Sobre) (ports.CambioListaPrecios, error) {
	if s.Tipo != mensajeria.TipoListaPreciosActualizada || s.Version != mensajeria.VersionListaPreciosActualizada {
		return ports.CambioListaPrecios{}, fmt.Errorf("tipo %q versión %d desconocidos", s.Tipo, s.Version)
	}
	var datos mensajeria.ListaPreciosActualizada
	if err := json.Unmarshal(s.Datos, &datos); err != nil {
		return ports.CambioListaPrecios{}, fmt.Errorf("datos: %w", err)
	}
	if err := validar(datos); err != nil {
		return ports.CambioListaPrecios{}, err
	}

	cambio := ports.CambioListaPrecios{
		EventoID:    s.EventoID,
		ProveedorID: datos.Proveedor.ID,
		Secuencia:   datos.Secuencia,
		Completa:    datos.Completa,
		Bajas:       datos.Bajas,
	}
	for _, p := range datos.Productos {
		unidad := p.Unidad
		if strings.TrimSpace(unidad) == "" {
			unidad = "unidad" // el default de la columna
		}
		cambio.Productos = append(cambio.Productos, ports.ProductoCambio{
			SKU: p.SKU, Nombre: p.Nombre, Categoria: p.Categoria, Unidad: unidad,
		})
	}
	for _, o := range datos.Ofertas {
		cambio.Ofertas = append(cambio.Ofertas, ports.OfertaCambio{
			ProductoSKU:             o.ProductoSKU,
			PrecioUnitario:          o.PrecioUnitario,
			DescuentoPct:            o.DescuentoPct,
			CantidadMinimaDescuento: o.CantidadMinimaDescuento,
			Flete:                   o.Flete,
			StockDisponible:         o.StockDisponible,
		})
	}
	return cambio, nil
}

// maxEntero es lo que entra en las columnas INTEGER de ofertas.
const maxEntero = 1<<31 - 1

// validar rechaza de entrada lo que la base nunca aceptaría, para que no gaste
// reintentos como si fuera una falla transitoria.
func validar(d mensajeria.ListaPreciosActualizada) error {
	if d.Proveedor.ID <= 0 {
		return errors.New("falta proveedor.id")
	}
	if d.Secuencia <= 0 {
		return errors.New("secuencia tiene que ser 1 o mayor")
	}
	for _, p := range d.Productos {
		if strings.TrimSpace(p.SKU) == "" || strings.TrimSpace(p.Nombre) == "" || strings.TrimSpace(p.Categoria) == "" {
			return fmt.Errorf("producto %q incompleto", p.SKU)
		}
	}
	for _, o := range d.Ofertas {
		switch {
		case strings.TrimSpace(o.ProductoSKU) == "":
			return errors.New("oferta sin producto_sku")
		case o.PrecioUnitario < 0 || o.Flete < 0 || o.DescuentoPct < 0 || o.DescuentoPct > 100:
			return fmt.Errorf("oferta de %q con importes fuera de rango", o.ProductoSKU)
		case o.StockDisponible < 0 || o.StockDisponible > maxEntero,
			o.CantidadMinimaDescuento < 0 || o.CantidadMinimaDescuento > maxEntero:
			return fmt.Errorf("oferta de %q con cantidades fuera de rango", o.ProductoSKU)
		}
	}
	for _, sku := range d.Bajas {
		if strings.TrimSpace(sku) == "" {
			return errors.New("baja sin SKU")
		}
	}
	return nil
}
