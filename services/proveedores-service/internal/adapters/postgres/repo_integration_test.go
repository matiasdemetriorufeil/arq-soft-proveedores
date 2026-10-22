//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/migrations"
)

var logSilencioso = slog.New(slog.NewTextHandler(io.Discard, nil))

type conteos struct{ proveedores, productos, ofertas int }

func contar(t *testing.T, pool *pgxpool.Pool) conteos {
	t.Helper()
	ctx := context.Background()
	var c conteos
	for tabla, destino := range map[string]*int{
		"proveedores": &c.proveedores,
		"productos":   &c.productos,
		"ofertas":     &c.ofertas,
	} {
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+tabla).Scan(destino); err != nil {
			t.Fatalf("contar %s: %v", tabla, err)
		}
	}
	return c
}

// migrarCompartido deja el esquema y el seed cargados en la base compartida.
// Migrar es idempotente, así que cada test puede llamarlo sin depender del orden.
func migrarCompartido(t *testing.T) *postgres.Repo {
	t.Helper()
	if err := postgres.Migrar(context.Background(), poolCompartido, migrations.FS, logSilencioso); err != nil {
		t.Fatalf("Migrar: %v", err)
	}
	return postgres.NuevoRepo(poolCompartido)
}

func TestMigrar_BaseVacia(t *testing.T) {
	migrarCompartido(t)

	c := contar(t, poolCompartido)
	if c.proveedores != 10 {
		t.Errorf("proveedores = %d, se esperaban 10", c.proveedores)
	}
	if c.productos != 30 {
		t.Errorf("productos = %d, se esperaban 30", c.productos)
	}
	if c.ofertas <= 0 {
		t.Errorf("ofertas = %d, se esperaban > 0", c.ofertas)
	}
	t.Logf("seed cargado: %+v", c)
}

func TestMigrar_Idempotente(t *testing.T) {
	migrarCompartido(t)
	antes := contar(t, poolCompartido)

	migrarCompartido(t)
	despues := contar(t, poolCompartido)

	if antes != despues {
		t.Errorf("los conteos cambiaron tras migrar dos veces: antes %+v, después %+v", antes, despues)
	}
}

func TestOfertasPorProducto_SKUConOfertas(t *testing.T) {
	repo := migrarCompartido(t)

	got, err := repo.OfertasPorProducto(context.Background(), "PAN-450W-MONO")
	if err != nil {
		t.Fatalf("OfertasPorProducto: %v", err)
	}

	// Valores de migrations/002_seed.sql.
	want := []domain.Oferta{
		{ProveedorID: 2, ProveedorNombre: "Bornes y Cables SA", ProductoSKU: "PAN-450W-MONO",
			PrecioUnitario: 211018.43, DescuentoPct: 12, CantidadMinimaDescuento: 100, Flete: 54600, StockDisponible: 407},
		{ProveedorID: 6, ProveedorNombre: "Fotovoltaica Pampeana SRL", ProductoSKU: "PAN-450W-MONO",
			PrecioUnitario: 203628.95, DescuentoPct: 15, CantidadMinimaDescuento: 10, Flete: 0, StockDisponible: 66},
		{ProveedorID: 9, ProveedorNombre: "Ingenieria Renovable SA", ProductoSKU: "PAN-450W-MONO",
			PrecioUnitario: 172997.20, DescuentoPct: 0, CantidadMinimaDescuento: 0, Flete: 36400, StockDisponible: 0},
	}

	// La consulta no fija un orden; se compara ordenando por proveedor.
	sort.Slice(got, func(i, j int) bool { return got[i].ProveedorID < got[j].ProveedorID })
	if len(got) != len(want) {
		t.Fatalf("se obtuvieron %d ofertas, se esperaban %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("oferta %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

func TestOfertasPorProducto_ProductoSinOfertas(t *testing.T) {
	repo := migrarCompartido(t)

	got, err := repo.OfertasPorProducto(context.Background(), "MON-WIFI")
	if err != nil {
		t.Fatalf("OfertasPorProducto: %v", err)
	}
	if got == nil {
		t.Fatal("se esperaba una lista vacía no nil")
	}
	if len(got) != 0 {
		t.Errorf("se esperaban 0 ofertas, se obtuvieron %d: %+v", len(got), got)
	}
}

func TestOfertasPorProducto_SKUInexistente(t *testing.T) {
	repo := migrarCompartido(t)

	got, err := repo.OfertasPorProducto(context.Background(), "NOPE-123")
	if !errors.Is(err, domain.ErrProductoNoEncontrado) {
		t.Fatalf("err = %v, se esperaba domain.ErrProductoNoEncontrado", err)
	}
	if got != nil {
		t.Errorf("se esperaba nil junto con el error, se obtuvo %+v", got)
	}
}

func TestPing_BaseArriba(t *testing.T) {
	repo := postgres.NuevoRepo(poolCompartido)
	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// Usa un contenedor propio para poder detenerlo sin afectar al compartido.
func TestBaseCaida(t *testing.T) {
	ctx := context.Background()

	ctr, url, err := levantarPostgres(ctx)
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Errorf("terminar contenedor: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("levantar Postgres: %v", err)
	}

	pool, err := postgres.NuevoPool(ctx, url)
	if err != nil {
		t.Fatalf("NuevoPool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := postgres.Migrar(ctx, pool, migrations.FS, logSilencioso); err != nil {
		t.Fatalf("Migrar: %v", err)
	}
	repo := postgres.NuevoRepo(pool)
	if err := repo.Ping(ctx); err != nil {
		t.Fatalf("Ping antes de detener: %v", err)
	}

	if err := ctr.Stop(ctx, nil); err != nil {
		t.Fatalf("detener contenedor: %v", err)
	}

	const limite = 5 * time.Second

	t.Run("OfertasPorProducto", func(t *testing.T) {
		inicio := time.Now()
		_, err := repo.OfertasPorProducto(ctx, "PAN-450W-MONO")
		dur := time.Since(inicio)
		if err == nil {
			t.Fatal("se esperaba un error con la base caída")
		}
		if errors.Is(err, domain.ErrProductoNoEncontrado) {
			t.Fatalf("la base caída no debe confundirse con un SKU inexistente: %v", err)
		}
		if dur >= limite {
			t.Errorf("tardó %v, se esperaba menos de %v", dur, limite)
		}
		t.Logf("error en %v: %v", dur, err)
	})

	t.Run("Ping", func(t *testing.T) {
		inicio := time.Now()
		err := repo.Ping(ctx)
		dur := time.Since(inicio)
		if err == nil {
			t.Fatal("se esperaba un error con la base caída")
		}
		if errors.Is(err, domain.ErrProductoNoEncontrado) {
			t.Fatalf("error inesperado: %v", err)
		}
		if dur >= limite {
			t.Errorf("tardó %v, se esperaba menos de %v", dur, limite)
		}
		t.Logf("error en %v: %v", dur, err)
	})
}
