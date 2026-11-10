//go:build integration

package rabbitmq_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/rabbitmq"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/migrations"
)

// Las mismas imágenes que el docker-compose.yml.
const (
	imagenRabbit   = "rabbitmq:4.1-management-alpine"
	imagenPostgres = "postgres:16-alpine"
	proveedorSeed  = int64(3)
)

var (
	urlBroker string
	pool      *pgxpool.Pool
	repo      *postgres.Repo
	silencio  = slog.New(slog.NewTextHandler(io.Discard, nil))
	// Cada test usa secuencias más altas que el anterior: la última aplicada queda
	// guardada en la base compartida.
	secuencia atomic.Int64
)

func TestMain(m *testing.M) { os.Exit(correr(m)) }

func correr(m *testing.M) int {
	ctx := context.Background()
	rabbit, err := tcrabbit.Run(ctx, imagenRabbit)
	if rabbit != nil {
		defer func() { _ = testcontainers.TerminateContainer(rabbit) }()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo levantar %s: %v\n", imagenRabbit, err)
		return 1
	}
	if urlBroker, err = rabbit.AmqpURL(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	pg, err := tcpostgres.Run(ctx, imagenPostgres, tcpostgres.WithDatabase("proveedores"),
		tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("test"), tcpostgres.BasicWaitStrategies())
	if pg != nil {
		defer func() { _ = testcontainers.TerminateContainer(pg) }()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo levantar %s: %v\n", imagenPostgres, err)
		return 1
	}
	url, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if pool, err = postgres.NuevoPool(ctx, url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	if err := postgres.Migrar(ctx, pool, migrations.FS, silencio); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	repo = postgres.NuevoRepo(pool)
	return m.Run()
}

// --- ayudas ----------------------------------------------------------------------

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
	if err := mensajeria.DeclararTopologia(ch); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueuePurge(mensajeria.ColaListaPrecios, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueuePurge(mensajeria.ColaListaPreciosFallidos, false); err != nil {
		t.Fatal(err)
	}
	return ch
}

// arrancar corre un consumidor hasta que termine el test o se llame a la función
// que devuelve.
func arrancar(t *testing.T, aplicador ports.AplicadorListaPrecios) (parar func()) {
	t.Helper()
	c := rabbitmq.NuevoConsumidor(rabbitmq.Config{
		URL: urlBroker, Aplicador: aplicador, Logger: silencio, EsperaReintento: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	listo := make(chan struct{})
	go func() { c.Correr(ctx); close(listo) }()
	var una sync.Once
	parar = func() {
		una.Do(func() {
			cancel()
			select {
			case <-listo:
			case <-time.After(10 * time.Second):
				t.Error("el consumidor no terminó")
			}
		})
	}
	t.Cleanup(parar)
	return parar
}

func oferta(sku string, precio float64) mensajeria.OfertaEvento {
	return mensajeria.OfertaEvento{ProductoSKU: sku, PrecioUnitario: precio, StockDisponible: 5}
}

func producto(sku string) mensajeria.ProductoEvento {
	return mensajeria.ProductoEvento{SKU: sku, Nombre: "Producto " + sku, Categoria: "prueba", Unidad: "unidad"}
}

func publicar(t *testing.T, datos mensajeria.ListaPreciosActualizada) mensajeria.Sobre {
	t.Helper()
	s, err := mensajeria.NuevoSobre(context.Background(), mensajeria.TipoListaPreciosActualizada, 1, time.Now(), datos)
	if err != nil {
		t.Fatal(err)
	}
	publicarSobre(t, s)
	return s
}

func publicarSobre(t *testing.T, s mensajeria.Sobre) {
	t.Helper()
	p := mensajeria.NuevoPublicador(urlBroker)
	defer p.Cerrar()
	if err := p.Publicar(context.Background(), mensajeria.ClaveListaPrecios, s); err != nil {
		t.Fatal(err)
	}
}

func datosDe(productos []mensajeria.ProductoEvento, ofertas []mensajeria.OfertaEvento, bajas []string, completa bool) mensajeria.ListaPreciosActualizada {
	return mensajeria.ListaPreciosActualizada{
		Proveedor: mensajeria.ProveedorEvento{ID: proveedorSeed, Codigo: "seed"},
		Secuencia: secuencia.Add(1),
		Motivo:    mensajeria.MotivoSincronizacion,
		Completa:  completa,
		Productos: productos,
		Ofertas:   ofertas,
		Bajas:     bajas,
	}
}

func esperar(t *testing.T, que string, condicion func() bool) {
	t.Helper()
	hasta := time.Now().Add(20 * time.Second)
	for time.Now().Before(hasta) {
		if condicion() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("no pasó: %s", que)
}

func precioDe(t *testing.T, sku string) (float64, bool) {
	t.Helper()
	ofertas, err := repo.OfertasPorProducto(context.Background(), sku)
	if errors.Is(err, domain.ErrProductoNoEncontrado) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range ofertas {
		if o.ProveedorID == proveedorSeed {
			return o.PrecioUnitario, true
		}
	}
	return 0, false
}

func recibir(t *testing.T, ch *amqp.Channel, cola string) amqp.Delivery {
	t.Helper()
	hasta := time.Now().Add(20 * time.Second)
	for time.Now().Before(hasta) {
		d, ok, err := ch.Get(cola, false)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return d
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no llegó ningún mensaje a %s", cola)
	return amqp.Delivery{}
}

// aplicadorContador cuenta los eventos que llegan al repositorio.
type aplicadorContador struct {
	ports.AplicadorListaPrecios
	llamadas atomic.Int64
	err      error
}

func (a *aplicadorContador) AplicarListaPrecios(ctx context.Context, c ports.CambioListaPrecios) (ports.ResultadoAplicacion, error) {
	a.llamadas.Add(1)
	if a.err != nil {
		return ports.ResultadoAplicacion{}, a.err
	}
	return a.AplicadorListaPrecios.AplicarListaPrecios(ctx, c)
}

// --- la matriz de eventos ----------------------------------------------------------

func TestEventoNuevoLlegaALaBaseYDuplicadoSeAplicaUnaVez(t *testing.T) {
	canal(t)
	contador := &aplicadorContador{AplicadorListaPrecios: repo}
	arrancar(t, contador)

	sku := "FAV-int-nuevo"
	datos := datosDe([]mensajeria.ProductoEvento{producto(sku)}, []mensajeria.OfertaEvento{oferta(sku, 100)}, []string{}, false)
	sobre := publicar(t, datos)
	esperar(t, "que aparezca la oferta", func() bool { p, ok := precioDe(t, sku); return ok && p == 100 })

	// El mismo evento otra vez, con el mismo id: se confirma y no cambia nada.
	publicarSobre(t, sobre)
	esperar(t, "que se procese el duplicado", func() bool { return contador.llamadas.Load() >= 2 })

	var aplicados int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM eventos_procesados WHERE evento_id = $1`, sobre.EventoID).Scan(&aplicados); err != nil {
		t.Fatal(err)
	}
	if aplicados != 1 {
		t.Errorf("eventos registrados = %d", aplicados)
	}
}

func TestBasuraVaALaDLQSinReintentos(t *testing.T) {
	ch := canal(t)
	contador := &aplicadorContador{AplicadorListaPrecios: repo}
	arrancar(t, contador)

	if err := ch.Publish(mensajeria.ExchangeEventos, mensajeria.ClaveListaPrecios, false, false, amqp.Publishing{
		ContentType: "application/json", MessageId: "basura", Body: []byte("esto no es JSON"),
	}); err != nil {
		t.Fatal(err)
	}
	d := recibir(t, ch, mensajeria.ColaListaPreciosFallidos)
	if d.MessageId != "basura" {
		t.Errorf("a la DLQ llegó %q", d.MessageId)
	}
	// El broker cuenta como 1 la única entrega que tuvo.
	if n, _ := d.Headers["x-delivery-count"].(int64); n > 1 {
		t.Errorf("x-delivery-count = %d: tenía que ir sin reintentos", n)
	}
	d.Ack(false)
	if contador.llamadas.Load() != 0 {
		t.Error("la basura no tenía que llegar a la base")
	}
}

func TestBaseCaidaReintentaYALaTerceraEntregaVaALaDLQ(t *testing.T) {
	ch := canal(t)
	caida := &aplicadorContador{AplicadorListaPrecios: repo, err: errors.New("la base no responde")}
	arrancar(t, caida)

	sobre := publicar(t, datosDe(nil, []mensajeria.OfertaEvento{oferta("PAN-550W-MONO", 1)}, []string{}, false))

	d := recibir(t, ch, mensajeria.ColaListaPreciosFallidos)
	defer d.Ack(false)
	if d.MessageId != sobre.EventoID {
		t.Errorf("a la DLQ llegó %q, quiero %q", d.MessageId, sobre.EventoID)
	}
	if n := caida.llamadas.Load(); n != int64(mensajeria.IntentosMaximos) {
		t.Errorf("entregas = %d, quiero %d", n, mensajeria.IntentosMaximos)
	}
}

func TestSecuenciaViejaNoCambiaNada(t *testing.T) {
	canal(t)
	contador := &aplicadorContador{AplicadorListaPrecios: repo}
	arrancar(t, contador)

	sku := "FAV-int-secuencia"
	nueva := datosDe([]mensajeria.ProductoEvento{producto(sku)}, []mensajeria.OfertaEvento{oferta(sku, 200)}, []string{}, false)
	vieja := nueva
	vieja.Secuencia = nueva.Secuencia - 1
	vieja.Ofertas = []mensajeria.OfertaEvento{oferta(sku, 1)}

	publicar(t, nueva)
	esperar(t, "que se aplique la nueva", func() bool { p, ok := precioDe(t, sku); return ok && p == 200 })
	publicar(t, vieja)
	esperar(t, "que se procese la vieja", func() bool { return contador.llamadas.Load() == 2 })
	if p, _ := precioDe(t, sku); p != 200 {
		t.Errorf("precio = %v: la secuencia vieja pisó a la nueva", p)
	}
}

// --- dos réplicas ------------------------------------------------------------------

// llegadas guarda en qué orden vio cada réplica las secuencias.
type llegadas struct {
	ports.AplicadorListaPrecios
	mu     sync.Mutex
	orden  []int64
	demora time.Duration
}

func (l *llegadas) AplicarListaPrecios(ctx context.Context, c ports.CambioListaPrecios) (ports.ResultadoAplicacion, error) {
	l.mu.Lock()
	l.orden = append(l.orden, c.Secuencia)
	l.mu.Unlock()
	time.Sleep(l.demora)
	return l.AplicadorListaPrecios.AplicarListaPrecios(ctx, c)
}

func (l *llegadas) vistas() []int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int64(nil), l.orden...)
}

func TestDosReplicasAplicanEnOrdenYLaOtraTomaSiCaeLaActiva(t *testing.T) {
	canal(t)
	primera := &llegadas{AplicadorListaPrecios: repo, demora: 20 * time.Millisecond}
	segunda := &llegadas{AplicadorListaPrecios: repo, demora: 20 * time.Millisecond}
	pararPrimera := arrancar(t, primera)
	time.Sleep(500 * time.Millisecond) // que la primera quede como activa
	arrancar(t, segunda)
	time.Sleep(500 * time.Millisecond)

	sku := "FAV-int-orden"
	const eventos = 10
	var ultimaSecuencia int64
	for i := range eventos {
		d := datosDe([]mensajeria.ProductoEvento{producto(sku)}, []mensajeria.OfertaEvento{oferta(sku, float64(i+1))}, []string{}, false)
		ultimaSecuencia = d.Secuencia
		publicar(t, d)
	}
	esperar(t, "que se apliquen todos", func() bool { p, ok := precioDe(t, sku); return ok && p == eventos })

	vistas := primera.vistas()
	if len(vistas) != eventos || len(segunda.vistas()) != 0 {
		t.Errorf("primera vio %d, segunda %d: tiene que consumir una sola", len(vistas), len(segunda.vistas()))
	}
	for i := 1; i < len(vistas); i++ {
		if vistas[i] <= vistas[i-1] {
			t.Errorf("fuera de orden: %v", vistas)
		}
	}

	// Cae la activa: el siguiente evento lo aplica la otra.
	pararPrimera()
	d := datosDe(nil, []mensajeria.OfertaEvento{oferta(sku, 999)}, []string{}, false)
	if d.Secuencia <= ultimaSecuencia {
		t.Fatalf("secuencia %d no avanzó", d.Secuencia)
	}
	publicar(t, d)
	esperar(t, "que la otra réplica tome el consumo", func() bool { p, ok := precioDe(t, sku); return ok && p == 999 })
	if got := segunda.vistas(); len(got) != 1 || got[0] != d.Secuencia {
		t.Errorf("la segunda vio %v (%s)", got, strconv.FormatInt(d.Secuencia, 10))
	}
}

func TestSeReconectaSiElBrokerCierraLaConexion(t *testing.T) {
	canal(t)
	arrancar(t, repo)
	time.Sleep(500 * time.Millisecond)

	// Declarar la cola con otros argumentos cierra solo ese canal; lo que se prueba
	// es que el consumidor vuelve después de un corte, así que se le cierra la suya
	// desde el lado del broker: borrar la cola cancela al consumidor.
	ch := canal(t)
	if _, err := ch.QueueDelete(mensajeria.ColaListaPrecios, false, false, false); err != nil {
		t.Fatal(err)
	}

	sku := "FAV-int-reconexion"
	d := datosDe([]mensajeria.ProductoEvento{producto(sku)}, []mensajeria.OfertaEvento{oferta(sku, 50)}, []string{}, false)
	esperar(t, "que el consumidor vuelva a declarar la cola", func() bool {
		cola, err := ch.QueueDeclarePassive(mensajeria.ColaListaPrecios, true, false, false, false, nil)
		if err != nil {
			ch = nuevoCanalSinPurgar(t)
			return false
		}
		return cola.Consumers > 0
	})
	publicar(t, d)
	esperar(t, "que se aplique después de reconectar", func() bool { p, ok := precioDe(t, sku); return ok && p == 50 })
}

// Una cola declarada antes de x-single-active-consumer (un volumen viejo de
// RabbitMQ) no se puede redeclarar: el broker contesta PRECONDITION_FAILED. El
// servicio tiene que sobrevivir y avisar; el remedio está en eventos.md.
func TestColaViejaSinConsumidorUnicoEsUnErrorQueNoTiraAlServicio(t *testing.T) {
	ch := canal(t)
	if _, err := ch.QueueDelete(mensajeria.ColaListaPrecios, false, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueueDeclare(mensajeria.ColaListaPrecios, true, false, false, false, amqp.Table{
		"x-queue-type":              "quorum",
		"x-delivery-limit":          int32(mensajeria.IntentosMaximos),
		"x-dead-letter-exchange":    mensajeria.ExchangeFallidos,
		"x-dead-letter-routing-key": mensajeria.ColaListaPreciosFallidos,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		limpio := nuevoCanalSinPurgar(t)
		if _, err := limpio.QueueDelete(mensajeria.ColaListaPrecios, false, false, false); err != nil {
			t.Error(err)
		}
	})

	otro := nuevoCanalSinPurgar(t)
	err := mensajeria.DeclararTopologia(otro)
	var amqpErr *amqp.Error
	if !errors.As(err, &amqpErr) || amqpErr.Code != amqp.PreconditionFailed {
		t.Fatalf("err = %v, quiero PRECONDITION_FAILED", err)
	}

	parar := arrancar(t, repo)
	time.Sleep(300 * time.Millisecond)
	parar() // que vuelva al cancelar es lo que prueba que siguió reintentando
}

func nuevoCanalSinPurgar(t *testing.T) *amqp.Channel {
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
	return ch
}
