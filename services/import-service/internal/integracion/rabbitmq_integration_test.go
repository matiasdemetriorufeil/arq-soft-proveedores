//go:build integration

// Package integracion prueba pkg/mensajeria contra un RabbitMQ real: que el
// publicador recibe la confirmación del broker y que la topología manda a la
// cola de fallidos lo que se rechaza o se reintenta de más. Vive en
// import-service porque es el servicio que publica y ya tiene testcontainers.
package integracion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
)

// La misma imagen que el docker-compose.yml.
const imagenRabbit = "rabbitmq:4.1-management-alpine"

var urlBroker string

func TestMain(m *testing.M) { os.Exit(correr(m)) }

func correr(m *testing.M) int {
	ctx := context.Background()
	ctr, err := tcrabbit.Run(ctx, imagenRabbit)
	if ctr != nil {
		defer func() {
			if err := testcontainers.TerminateContainer(ctr); err != nil {
				fmt.Fprintf(os.Stderr, "no se pudo terminar el contenedor de RabbitMQ: %v\n", err)
			}
		}()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudo levantar %s (¿Docker está corriendo?): %v\n", imagenRabbit, err)
		return 1
	}
	if urlBroker, err = ctr.AmqpURL(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}

func canal(t *testing.T) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(urlBroker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	// De a uno, como el consumidor: recibir no se queda con más de un mensaje.
	if err := ch.Qos(1, 0, false); err != nil {
		t.Fatal(err)
	}
	// Cada test arranca con las colas vacías.
	if err := mensajeria.DeclararTopologia(ch); err != nil {
		t.Fatal(err)
	}
	ch.QueuePurge(mensajeria.ColaListaPrecios, false)
	ch.QueuePurge(mensajeria.ColaListaPreciosFallidos, false)
	return ch
}

func publicarUno(t *testing.T) mensajeria.Sobre {
	t.Helper()
	sobre, err := mensajeria.NuevoSobre(context.Background(), mensajeria.TipoListaPreciosActualizada, 1, time.Now(),
		mensajeria.ListaPreciosActualizada{ImportacionID: "imp-test", Secuencia: 1})
	if err != nil {
		t.Fatal(err)
	}
	p := mensajeria.NuevoPublicador(urlBroker)
	defer p.Cerrar()
	if err := p.Publicar(context.Background(), mensajeria.ClaveListaPrecios, sobre); err != nil {
		t.Fatalf("el broker no confirmó: %v", err)
	}
	return sobre
}

// recibir se suscribe en vez de usar basic.get porque la cola principal tiene
// x-single-active-consumer, y una cola quorum así rechaza basic.get. Se da de baja
// enseguida: el mensaje queda sin confirmar hasta que el test le conteste.
func recibir(t *testing.T, ch *amqp.Channel, cola string) amqp.Delivery {
	t.Helper()
	const etiqueta = "test-recibir"
	entregas, err := ch.Consume(cola, etiqueta, false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ch.Cancel(etiqueta, false); err != nil {
			t.Fatal(err)
		}
	}()
	select {
	case d := <-entregas:
		return d
	case <-time.After(10 * time.Second):
		t.Fatalf("no llegó ningún mensaje a %s", cola)
		return amqp.Delivery{}
	}
}

func TestPublicarConConfirmacionLlegaALaColaDeProveedores(t *testing.T) {
	ch := canal(t)
	sobre := publicarUno(t)

	d := recibir(t, ch, mensajeria.ColaListaPrecios)
	leido, err := mensajeria.LeerSobre(d.Body)
	if err != nil {
		t.Fatal(err)
	}
	if leido.EventoID != sobre.EventoID || d.MessageId != sobre.EventoID || d.DeliveryMode != amqp.Persistent {
		t.Errorf("mensaje = %+v", d)
	}
	d.Ack(false)
}

func TestUnMensajeRechazadoVaALaColaDeFallidos(t *testing.T) {
	ch := canal(t)
	sobre := publicarUno(t)

	// Lo que hará el consumidor con un mensaje no procesable: nack sin
	// reencolar.
	recibir(t, ch, mensajeria.ColaListaPrecios).Nack(false, false)

	d := recibir(t, ch, mensajeria.ColaListaPreciosFallidos)
	if d.MessageId != sobre.EventoID {
		t.Errorf("a la DLQ llegó %q, quiero %q", d.MessageId, sobre.EventoID)
	}
	d.Ack(false)
}

func TestPasadoElLimiteDeEntregasVaALaColaDeFallidos(t *testing.T) {
	ch := canal(t)
	sobre := publicarUno(t)

	// Un error transitorio que no se resuelve: el consumidor reencola y la
	// cola quorum cuenta las entregas.
	for range mensajeria.IntentosMaximos + 1 {
		recibir(t, ch, mensajeria.ColaListaPrecios).Nack(false, true)
	}
	d := recibir(t, ch, mensajeria.ColaListaPreciosFallidos)
	if d.MessageId != sobre.EventoID {
		t.Errorf("a la DLQ llegó %q", d.MessageId)
	}
	d.Ack(false)
}

func TestSinBrokerPublicarDevuelveErrorYNoCuelga(t *testing.T) {
	p := mensajeria.NuevoPublicador("amqp://guest:guest@127.0.0.1:1/")
	inicio := time.Now()
	sobre, _ := mensajeria.NuevoSobre(context.Background(), "T", 1, time.Now(), map[string]int{})
	if err := p.Publicar(context.Background(), mensajeria.ClaveListaPrecios, sobre); err == nil {
		t.Fatal("publicar sin broker tenía que fallar")
	}
	if time.Since(inicio) > 6*time.Second {
		t.Errorf("tardó %v en fallar", time.Since(inicio))
	}
}

func TestUnMensajeSinColaNoCuentaComoPublicado(t *testing.T) {
	canal(t)
	sobre, _ := mensajeria.NuevoSobre(context.Background(), "T", 1, time.Now(), map[string]int{})
	p := mensajeria.NuevoPublicador(urlBroker)
	defer p.Cerrar()
	err := p.Publicar(context.Background(), "clave.sin.ninguna.cola", sobre)
	if !errors.Is(err, mensajeria.ErrSinRuta) {
		t.Fatalf("err = %v, quiero ErrSinRuta: el evento se perdería marcado como publicado", err)
	}
	// Después de una devolución el publicador sigue sirviendo.
	if err := p.Publicar(context.Background(), mensajeria.ClaveListaPrecios, sobre); err != nil {
		t.Fatalf("después de la devolución: %v", err)
	}
}
