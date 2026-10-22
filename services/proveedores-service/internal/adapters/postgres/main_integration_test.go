//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
)

const imagenPostgres = "postgres:16-alpine"

// poolCompartido apunta al contenedor que levanta TestMain y comparten todos
// los tests del paquete, salvo el de "base caída", que usa uno propio.
var poolCompartido *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(correr(m))
}

// correr existe para que los defer (cierre del pool y terminación del
// contenedor) se ejecuten antes del os.Exit, aunque los tests fallen.
func correr(m *testing.M) int {
	ctx := context.Background()

	ctr, url, err := levantarPostgres(ctx)
	if ctr != nil {
		defer func() {
			if err := testcontainers.TerminateContainer(ctr); err != nil {
				fmt.Fprintf(os.Stderr, "no se pudo terminar el contenedor de Postgres: %v\n", err)
			}
		}()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudo levantar %s con testcontainers "+
			"(¿Docker está instalado y corriendo?): %v\n", imagenPostgres, err)
		return 1
	}

	pool, err := postgres.NuevoPool(ctx, url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tests de integración: no se pudo abrir el pool: %v\n", err)
		return 1
	}
	defer pool.Close()
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
		tcpostgres.WithDatabase("proveedores"),
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
