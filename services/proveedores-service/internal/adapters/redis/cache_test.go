package redis

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
)

type almacenFalso struct {
	mu       sync.Mutex
	datos    map[string]string
	vigencia map[string]time.Duration
	errGet   error
	errSet   error
	colgar   bool
	gets     int
}

func nuevoAlmacenFalso() *almacenFalso {
	return &almacenFalso{datos: map[string]string{}, vigencia: map[string]time.Duration{}}
}

func (a *almacenFalso) Get(ctx context.Context, clave string) (string, error) {
	a.mu.Lock()
	a.gets++
	colgar := a.colgar
	a.mu.Unlock()
	if colgar {
		<-ctx.Done()
		return "", ctx.Err()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.errGet != nil {
		return "", a.errGet
	}
	v, ok := a.datos[clave]
	if !ok {
		return "", ErrNoEsta
	}
	return v, nil
}

func (a *almacenFalso) Set(_ context.Context, clave, valor string, vigencia time.Duration) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.errSet != nil {
		return a.errSet
	}
	a.datos[clave] = valor
	a.vigencia[clave] = vigencia
	return nil
}

func (a *almacenFalso) Del(_ context.Context, claves ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range claves {
		delete(a.datos, c)
	}
	return nil
}

// origenFalso hace de PostgreSQL.
type origenFalso struct {
	llamadas atomic.Int64
	ofertas  map[string][]domain.Oferta
	err      error
	demora   time.Duration
}

func (o *origenFalso) OfertasPorProducto(ctx context.Context, sku string) ([]domain.Oferta, error) {
	o.llamadas.Add(1)
	time.Sleep(o.demora)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if o.err != nil {
		return nil, o.err
	}
	of, ok := o.ofertas[sku]
	if !ok {
		return nil, domain.ErrProductoNoEncontrado
	}
	return of, nil
}

func silencio() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func nuevoOrigen() *origenFalso {
	return &origenFalso{ofertas: map[string][]domain.Oferta{
		"PAN-1":    {{ProveedorID: 1, ProveedorNombre: "Andes", ProductoSKU: "PAN-1", PrecioUnitario: 100.25, StockDisponible: 3}},
		"SIN-OFER": {},
	}}
}

func TestAciertoNoConsultaLaBaseYDevuelveLoMismo(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	c := NuevaCache(a, o, silencio())

	primera, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil {
		t.Fatal(err)
	}
	segunda, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil {
		t.Fatal(err)
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
	if len(segunda) != 1 || segunda[0] != primera[0] {
		t.Errorf("lo cacheado = %+v, lo original = %+v", segunda, primera)
	}
	if got := a.vigencia[PrefijoClave+"PAN-1"]; got != VigenciaOfertas {
		t.Errorf("TTL = %v", got)
	}
}

func TestProductoSinOfertasTambienSeGuarda(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	c := NuevaCache(a, o, silencio())
	for range 2 {
		ofertas, err := c.OfertasPorProducto(context.Background(), "SIN-OFER")
		if err != nil || len(ofertas) != 0 {
			t.Fatalf("ofertas = %v, err = %v", ofertas, err)
		}
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
}

func TestSKUInexistenteDosVecesElSegundoNoVaALaBase(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	c := NuevaCache(a, o, silencio())
	for range 2 {
		if _, err := c.OfertasPorProducto(context.Background(), "NO-EXISTE"); !errors.Is(err, domain.ErrProductoNoEncontrado) {
			t.Fatalf("err = %v", err)
		}
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
	if got := a.vigencia[PrefijoClave+"NO-EXISTE"]; got != VigenciaNoExiste {
		t.Errorf("TTL de la marca = %v", got)
	}
}

func TestRedisConErrorFallaAbierta(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	a.errGet = errors.New("redis caído")
	a.errSet = errors.New("redis caído")
	c := NuevaCache(a, o, silencio())
	ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil || len(ofertas) != 1 {
		t.Fatalf("ofertas = %v, err = %v: tenía que responder desde la base", ofertas, err)
	}
}

func TestRedisColgadoCortaA50ms(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	a.colgar = true
	c := NuevaCache(a, o, silencio())
	inicio := time.Now()
	ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil || len(ofertas) != 1 {
		t.Fatalf("ofertas = %v, err = %v", ofertas, err)
	}
	if d := time.Since(inicio); d < TimeoutRedis || d > 10*TimeoutRedis {
		t.Errorf("tardó %v, quiero poco más de %v", d, TimeoutRedis)
	}
	if len(a.datos) != 0 {
		t.Errorf("después de una lectura fallida no se escribe: %v", a.datos)
	}
}

func TestUnaFallaDeLaBaseNoSeGuarda(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	o.err = errors.New("la base no responde")
	c := NuevaCache(a, o, silencio())
	if _, err := c.OfertasPorProducto(context.Background(), "PAN-1"); err == nil || errors.Is(err, domain.ErrProductoNoEncontrado) {
		t.Fatalf("err = %v, quiero el error de la base", err)
	}
	if len(a.datos) != 0 {
		t.Errorf("se guardó %v: una falla de la base no puede quedar 60 s", a.datos)
	}
}

func TestValorIlegibleSeTrataComoFallo(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	a.datos[PrefijoClave+"PAN-1"] = "{no es json"
	c := NuevaCache(a, o, silencio())
	ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil || len(ofertas) != 1 || o.llamadas.Load() != 1 {
		t.Fatalf("ofertas = %v, err = %v, llamadas = %d", ofertas, err, o.llamadas.Load())
	}
}

func TestInvalidarHaceQueLaSiguienteLecturaTraigaLoNuevo(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	c := NuevaCache(a, o, silencio())
	if _, err := c.OfertasPorProducto(context.Background(), "PAN-1"); err != nil {
		t.Fatal(err)
	}
	o.ofertas["PAN-1"] = []domain.Oferta{{ProveedorID: 1, PrecioUnitario: 999}}
	if err := c.InvalidarOfertas(context.Background(), []string{"PAN-1"}); err != nil {
		t.Fatal(err)
	}
	ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil || ofertas[0].PrecioUnitario != 999 {
		t.Fatalf("ofertas = %+v, err = %v", ofertas, err)
	}
}

func TestInvalidarSinSKUsNoLlamaARedis(t *testing.T) {
	c := NuevaCache(nuevoAlmacenFalso(), nuevoOrigen(), silencio())
	if err := c.InvalidarOfertas(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestEstampidaUnaSolaConsulta(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	o.demora = 100 * time.Millisecond
	c := NuevaCache(a, o, silencio())

	const lecturas = 50
	var wg sync.WaitGroup
	errores := make(chan error, lecturas)
	for range lecturas {
		wg.Go(func() {
			ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
			if err == nil && len(ofertas) != 1 {
				err = errors.New("lista vacía")
			}
			errores <- err
		})
	}
	wg.Wait()
	close(errores)
	for err := range errores {
		if err != nil {
			t.Fatal(err)
		}
	}
	if o.llamadas.Load() != 1 {
		t.Errorf("consultas a la base = %d, quiero 1", o.llamadas.Load())
	}
}

func TestSiSeVaLaPrimeraRequestLasDemasReciben(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	o.demora = 100 * time.Millisecond
	c := NuevaCache(a, o, silencio())

	ctx, cancelar := context.WithCancel(context.Background())
	primera := make(chan error, 1)
	go func() {
		_, err := c.OfertasPorProducto(ctx, "PAN-1")
		primera <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelar()
	if err := <-primera; !errors.Is(err, context.Canceled) {
		t.Errorf("la que se fue recibió %v", err)
	}
	if ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1"); err != nil || len(ofertas) != 1 {
		t.Errorf("la que esperaba recibió %v, %v", ofertas, err)
	}
}

func TestSKUEnormeNoPasaPorLaCache(t *testing.T) {
	a, o := nuevoAlmacenFalso(), nuevoOrigen()
	c := NuevaCache(a, o, silencio())
	_, _ = c.OfertasPorProducto(context.Background(), strings.Repeat("x", largoMaximoSKU+1))
	if a.gets != 0 || len(a.datos) != 0 || o.llamadas.Load() != 1 {
		t.Errorf("gets = %d, datos = %d, llamadas = %d", a.gets, len(a.datos), o.llamadas.Load())
	}
}

// Un servidor que acepta y no contesta es el Redis "lento" del que habla ADR-007:
// con los reintentos por defecto de go-redis, la espera sería varias veces 50 ms.
func TestAlmacenRealContraServidorColgadoNoSePasaDelTimeout(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	a, err := NuevoAlmacen("redis://" + l.Addr().String() + "/0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Cerrar()
	c := NuevaCache(a, nuevoOrigen(), silencio())

	inicio := time.Now()
	ofertas, err := c.OfertasPorProducto(context.Background(), "PAN-1")
	if err != nil || len(ofertas) != 1 {
		t.Fatalf("ofertas = %v, err = %v", ofertas, err)
	}
	// Una sola espera: si la lectura no contestó, no se intenta escribir.
	if d := time.Since(inicio); d > 3*TimeoutRedis {
		t.Errorf("tardó %v: los timeouts no están acotando", d)
	}
}

func TestURLInvalida(t *testing.T) {
	if _, err := NuevoAlmacen("esto-no-es-una-url"); err == nil {
		t.Error("tenía que fallar")
	}
}
