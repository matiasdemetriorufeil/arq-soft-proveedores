//go:build integration

package redis_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/redis"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
)

// La misma imagen que el docker-compose.yml, con contraseña como allá.
const (
	imagenRedis = "redis:7.4-alpine"
	clave       = "clave-de-prueba"
)

var urlRedis string

func TestMain(m *testing.M) { os.Exit(correr(m)) }

func correr(m *testing.M) int {
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        imagenRedis,
			Cmd:          []string{"redis-server", "--requirepass", clave, "--save", "", "--appendonly", "no"},
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForLog("Ready to accept connections"),
		},
		Started: true,
	})
	if ctr != nil {
		defer func() { _ = testcontainers.TerminateContainer(ctr) }()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "no se pudo levantar %s: %v\n", imagenRedis, err)
		return 1
	}
	destino, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	urlRedis = "redis://:" + clave + "@" + destino + "/0"
	return m.Run()
}

type origen struct {
	llamadas atomic.Int64
	precio   atomic.Int64
	demora   time.Duration
}

func (o *origen) OfertasPorProducto(_ context.Context, sku string) ([]domain.Oferta, error) {
	o.llamadas.Add(1)
	time.Sleep(o.demora)
	if sku == "NO-EXISTE" {
		return nil, domain.ErrProductoNoEncontrado
	}
	return []domain.Oferta{{ProveedorID: 1, ProductoSKU: sku, PrecioUnitario: float64(o.precio.Load())}}, nil
}

func armar(t *testing.T) (*redis.Cache, *origen, *goredis.Client) {
	t.Helper()
	opciones, err := goredis.ParseURL(urlRedis)
	if err != nil {
		t.Fatal(err)
	}
	directo := goredis.NewClient(opciones)
	t.Cleanup(func() { directo.Close() })
	if err := directo.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	almacen, err := redis.NuevoAlmacen(urlRedis)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { almacen.Cerrar() })
	o := &origen{}
	o.precio.Store(100)
	return redis.NuevaCache(almacen, o, slog.New(slog.NewTextHandler(io.Discard, nil))), o, directo
}

func TestAciertoContraRedisRealConClaveYTTLDeADR007(t *testing.T) {
	c, o, directo := armar(t)
	ctx := context.Background()
	for range 3 {
		ofertas, err := c.OfertasPorProducto(ctx, "PAN-1")
		if err != nil || ofertas[0].PrecioUnitario != 100 {
			t.Fatalf("ofertas = %+v, err = %v", ofertas, err)
		}
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
	ttl, err := directo.TTL(ctx, "ofertas:v1:PAN-1").Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 50*time.Second || ttl > 60*time.Second {
		t.Errorf("TTL = %v, quiero 60 s", ttl)
	}
}

func TestMarcaDeNoExisteConTTLCorto(t *testing.T) {
	c, o, directo := armar(t)
	ctx := context.Background()
	for range 2 {
		if _, err := c.OfertasPorProducto(ctx, "NO-EXISTE"); !errors.Is(err, domain.ErrProductoNoEncontrado) {
			t.Fatalf("err = %v", err)
		}
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
	ttl, err := directo.TTL(ctx, "ofertas:v1:NO-EXISTE").Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > 10*time.Second {
		t.Errorf("TTL = %v, quiero 10 s", ttl)
	}
}

func TestInvalidarContraRedisReal(t *testing.T) {
	c, o, _ := armar(t)
	ctx := context.Background()
	if _, err := c.OfertasPorProducto(ctx, "PAN-1"); err != nil {
		t.Fatal(err)
	}
	o.precio.Store(250)
	if err := c.InvalidarOfertas(ctx, []string{"PAN-1", "OTRO"}); err != nil {
		t.Fatal(err)
	}
	ofertas, err := c.OfertasPorProducto(ctx, "PAN-1")
	if err != nil || ofertas[0].PrecioUnitario != 250 {
		t.Fatalf("ofertas = %+v, err = %v", ofertas, err)
	}
}

func TestLecturasConcurrentesDeUnSKUVencidoHacenUnaConsulta(t *testing.T) {
	c, o, _ := armar(t)
	o.demora = 100 * time.Millisecond
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if _, err := c.OfertasPorProducto(context.Background(), "PAN-1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
}

func TestRedisInalcanzableRespondeDesdeLaBase(t *testing.T) {
	almacen, err := redis.NuevoAlmacen("redis://:x@127.0.0.1:1/0")
	if err != nil {
		t.Fatal(err)
	}
	defer almacen.Cerrar()
	o := &origen{}
	c := redis.NuevaCache(almacen, o, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := c.OfertasPorProducto(context.Background(), "PAN-1"); err != nil {
		t.Fatalf("err = %v: tenía que responder desde la base", err)
	}
}
