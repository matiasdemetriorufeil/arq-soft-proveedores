package mensajeria

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Nombres de la topología. Están documentados en docs/contracts/eventos.md:
// si cambian acá, cambian allá.
const (
	// ExchangeEventos recibe todos los eventos de dominio. Es topic para que
	// un consumidor nuevo se suscriba por clave sin tocar al que publica.
	ExchangeEventos = "proveedores.eventos"
	// ExchangeFallidos recibe los mensajes que una cola descarta.
	ExchangeFallidos = "proveedores.eventos.dlx"

	ClaveListaPrecios = "lista_precios.actualizada"

	// ColaListaPrecios es la cola de proveedores-service para este evento.
	ColaListaPrecios = "proveedores-service.lista-precios"
	// ColaListaPreciosFallidos guarda lo que no se pudo procesar.
	ColaListaPreciosFallidos = "proveedores-service.lista-precios.dlq"

	// IntentosMaximos es cuántas veces se entrega un mensaje antes de que la
	// cola lo mande a la de fallidos (ADR-005: hasta 3 intentos).
	IntentosMaximos = 3
)

// argsColaListaPrecios son los argumentos de la cola principal. Es una cola
// quorum porque así RabbitMQ cuenta las entregas (x-delivery-limit) y, pasado
// el límite, la manda sola al exchange de fallidos: el consumidor no tiene que
// llevar la cuenta de los reintentos.
func argsColaListaPrecios() amqp.Table {
	return amqp.Table{
		"x-queue-type":              "quorum",
		"x-delivery-limit":          int32(IntentosMaximos),
		"x-dead-letter-exchange":    ExchangeFallidos,
		"x-dead-letter-routing-key": ColaListaPreciosFallidos,
	}
}

// Declarador es la parte de *amqp.Channel que usa DeclararTopologia; permite
// probarla sin broker.
type Declarador interface {
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
}

// DeclararTopologia crea (o confirma, si ya existen) los exchanges y colas del
// evento ListaPreciosActualizada. La llaman los dos lados: el que publica,
// para que ningún evento se pierda si el consumidor todavía no arrancó, y el
// que consume. Declarar algo que ya existe con los mismos argumentos no hace
// nada.
func DeclararTopologia(ch Declarador) error {
	if err := ch.ExchangeDeclare(ExchangeEventos, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declarar exchange %s: %w", ExchangeEventos, err)
	}
	if err := ch.ExchangeDeclare(ExchangeFallidos, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declarar exchange %s: %w", ExchangeFallidos, err)
	}
	if _, err := ch.QueueDeclare(ColaListaPreciosFallidos, true, false, false, false,
		amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("declarar cola %s: %w", ColaListaPreciosFallidos, err)
	}
	if err := ch.QueueBind(ColaListaPreciosFallidos, ColaListaPreciosFallidos, ExchangeFallidos, false, nil); err != nil {
		return fmt.Errorf("enlazar cola %s: %w", ColaListaPreciosFallidos, err)
	}
	if _, err := ch.QueueDeclare(ColaListaPrecios, true, false, false, false, argsColaListaPrecios()); err != nil {
		return fmt.Errorf("declarar cola %s: %w", ColaListaPrecios, err)
	}
	if err := ch.QueueBind(ColaListaPrecios, ClaveListaPrecios, ExchangeEventos, false, nil); err != nil {
		return fmt.Errorf("enlazar cola %s: %w", ColaListaPrecios, err)
	}
	return nil
}
