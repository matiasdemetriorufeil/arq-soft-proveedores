// Command reindexar reconstruye el índice de búsqueda desde PostgreSQL (ADR-006):
// carga un índice temporal y lo intercambia con el vigente, así que la búsqueda
// sigue respondiendo mientras tanto. Se corre a mano si se sospecha que el índice
// está desfasado:
//
//	docker compose exec proveedores-service-1 /reindexar
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/observabilidad"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/meilisearch"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/indexacion"
)

func main() {
	log := observabilidad.NuevoLogger("reindexar")
	if err := run(log); err != nil {
		log.Error("no se pudo reconstruir el índice", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	meiliURL := strings.TrimSpace(os.Getenv("MEILI_URL"))
	if databaseURL == "" || meiliURL == "" {
		return errors.New("DATABASE_URL y MEILI_URL son obligatorias")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NuevoPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	indice := meilisearch.NuevoCliente(meiliURL, os.Getenv("MEILI_MASTER_KEY"), os.Getenv("MEILI_INDICE"))
	inicio := time.Now()
	if err := indexacion.Nuevo(postgres.NuevoRepo(pool), indice, log, 0).Reconstruir(ctx); err != nil {
		return err
	}
	log.Info("reconstrucción terminada", "duracion_ms", time.Since(inicio).Milliseconds())
	return nil
}
