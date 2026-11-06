//go:build integration

package repository_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/migrations"
)

const imagenPostgres = "postgres:16-alpine"

// varURLDePrueba permite correr estos tests contra un PostgreSQL ya levantado,
// sin Docker en la máquina donde se escriben. El test de concurrencia es la
// evidencia de CA-13.1 y tiene que poder correrse en cualquier máquina del
// grupo:
//
//	$env:STOCK_TEST_DATABASE_URL = "postgres://postgres:test@localhost:5432/stock_test?sslmode=disable"
//	go test -tags=integration -count=1 ./internal/repository/
//
// Ojo: la base que se le pase se usa de verdad (las migraciones crean las
// tablas y los tests borran sus filas). No apuntarla a una base con datos.
const varURLDePrueba = "STOCK_TEST_DATABASE_URL"

// poolCompartido apunta a la base que prepara TestMain y comparten todos los
// tests del paquete.
var poolCompartido *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(correr(m))
}

// correr existe para que los defer (cierre del pool y terminación del
// contenedor) se ejecuten antes del os.Exit, aunque los tests fallen.
func correr(m *testing.M) int {
	ctx := context.Background()

	url := strings.TrimSpace(os.Getenv(varURLDePrueba))
	if url == "" {
		ctr, urlCtr, err := levantarPostgres(ctx)
		if ctr != nil {
			defer func() {
				if err := testcontainers.TerminateContainer(ctr); err != nil {
					fmt.Fprintf(os.Stderr, "no se pudo terminar el contenedor de Postgres: %v\n", err)
				}
			}()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "tests de integración: no se pudo levantar %s con testcontainers "+
				"(¿Docker está instalado y corriendo?): %v\n"+
				"Alternativa sin Docker: exportar %s apuntando a un PostgreSQL real.\n",
				imagenPostgres, err, varURLDePrueba)
			return 1
		}
		url = urlCtr
	}

	pool, err := repository.NuevoPool(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudo abrir el pool: %v\n", err)
		return 1
	}
	defer pool.Close()

	// Las mismas migraciones embebidas que aplica el binario al arrancar: el
	// esquema que se prueba es el que se despliega.
	migrarCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := repository.Migrar(migrarCtx, pool, migrations.FS, log); err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudieron aplicar las migraciones: %v\n", err)
		return 1
	}

	poolCompartido = pool
	return m.Run()
}

// levantarPostgres arranca un Postgres efímero en un puerto aleatorio del host
// y espera a que acepte conexiones. Devuelve el contenedor aunque falle la
// espera, para que el llamador pueda terminarlo.
func levantarPostgres(ctx context.Context) (ctr *tcpostgres.PostgresContainer, url string, err error) {
	// Algunas rutas de testcontainers hacen panic si no encuentran Docker;
	// se convierten en error para que el mensaje sea claro.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("testcontainers: %v", r)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute) // incluye la descarga de la imagen
	defer cancel()

	ctr, err = tcpostgres.Run(ctx, imagenPostgres,
		tcpostgres.WithDatabase("stock"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return ctr, "", err
	}
	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return ctr, "", fmt.Errorf("obtener connection string: %w", err)
	}
	return ctr, url, nil
}

func migracionesDePrueba() fs.FS { return migrations.FS }

func loggerSilencioso() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// prepararSKU deja el SKU con el disponible indicado y sin reservas, para que
// cada test arranque de un estado conocido. Los tests comparten la base, así
// que cada uno usa su propio SKU.
func prepararSKU(t *testing.T, sku string, disponible, minimo int) {
	t.Helper()
	ctx := context.Background()

	if _, err := poolCompartido.Exec(ctx,
		`DELETE FROM reservas WHERE sku = $1`, sku); err != nil {
		t.Fatalf("limpiar reservas de %s: %v", sku, err)
	}
	if _, err := poolCompartido.Exec(ctx, `
		INSERT INTO stock_productos (sku, disponible, reservado, minimo)
		VALUES ($1, $2, 0, $3)
		ON CONFLICT (sku) DO UPDATE
		SET disponible = EXCLUDED.disponible,
		    reservado  = 0,
		    minimo     = EXCLUDED.minimo`, sku, disponible, minimo); err != nil {
		t.Fatalf("preparar stock de %s: %v", sku, err)
	}
}
