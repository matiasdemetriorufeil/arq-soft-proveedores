package mensajeria

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TimeoutConfirmacion es cuánto se espera la confirmación del broker. Si no
// llega, el evento se considera no publicado y queda pendiente en el outbox
// del que lo publica.
const TimeoutConfirmacion = 5 * time.Second

// ErrSinConfirmar indica que el broker no confirmó el mensaje: puede haberlo
// recibido o no, así que quien publica lo reintenta (el consumidor deduplica
// por evento_id).
var ErrSinConfirmar = errors.New("el broker no confirmó el mensaje")

// Publicador publica sobres en ExchangeEventos con confirmaciones del broker
// (publisher confirms). Si la conexión se cae, se reconecta en la próxima
// publicación: no hay goroutines de fondo que administrar.
type Publicador struct {
	url string

	mu   sync.Mutex
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NuevoPublicador no se conecta todavía: la primera conexión la abre Publicar
// o Conectar. Así el servicio arranca aunque RabbitMQ no esté.
func NuevoPublicador(url string) *Publicador {
	return &Publicador{url: url}
}

// Conectar abre la conexión y declara la topología. Es idempotente.
func (p *Publicador) Conectar() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conectarLocked()
}

func (p *Publicador) conectarLocked() error {
	if p.ch != nil && !p.ch.IsClosed() {
		return nil
	}
	p.cerrarLocked()
	conn, err := amqp.DialConfig(p.url, amqp.Config{
		Dial:      amqp.DefaultDial(5 * time.Second),
		Heartbeat: 10 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("conectar a RabbitMQ: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("abrir canal: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		conn.Close()
		return fmt.Errorf("activar confirmaciones: %w", err)
	}
	if err := DeclararTopologia(ch); err != nil {
		conn.Close()
		return err
	}
	p.conn, p.ch = conn, ch
	return nil
}

// Conectado dice si hay un canal abierto. Lo usa /estado para informar.
func (p *Publicador) Conectado() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch != nil && !p.ch.IsClosed()
}

// Publicar envía el sobre con la clave dada y espera la confirmación del
// broker. Devuelve nil solo si el broker confirmó (ack).
func (p *Publicador) Publicar(ctx context.Context, clave string, s Sobre) error {
	cuerpo, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("serializar sobre: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.conectarLocked(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, TimeoutConfirmacion)
	defer cancel()
	conf, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, ExchangeEventos, clave,
		true,  // mandatory: si no hay cola enlazada, el broker no lo descarta en silencio
		false, // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    s.EventoID,
			Type:         s.Tipo,
			Timestamp:    s.OcurridoEn,
			Body:         cuerpo,
		})
	if err != nil {
		p.cerrarLocked()
		return fmt.Errorf("publicar %s: %w", s.Tipo, err)
	}
	ok, err := conf.WaitContext(ctx)
	if err != nil {
		p.cerrarLocked()
		return fmt.Errorf("%w: %v", ErrSinConfirmar, err)
	}
	if !ok {
		return fmt.Errorf("%w: nack de %s", ErrSinConfirmar, s.EventoID)
	}
	return nil
}

// Cerrar libera la conexión.
func (p *Publicador) Cerrar() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cerrarLocked()
}

func (p *Publicador) cerrarLocked() {
	if p.conn != nil {
		p.conn.Close()
	}
	p.conn, p.ch = nil, nil
}
