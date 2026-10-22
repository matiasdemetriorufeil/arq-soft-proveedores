package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockID serializa la aplicación de migraciones cuando arrancan
// varias instancias del servicio contra la misma base.
const advisoryLockID int64 = 727_001

// Migrar aplica, en orden, todos los .sql de migraciones dentro de una única
// transacción. Los scripts son idempotentes (DDL con IF NOT EXISTS y seed con
// ON CONFLICT DO NOTHING), así que se ejecutan completos en cada arranque.
func Migrar(ctx context.Context, pool *pgxpool.Pool, migraciones fs.FS, log *slog.Logger) error {
	nombres, err := fs.Glob(migraciones, "*.sql")
	if err != nil {
		return fmt.Errorf("listar migraciones: %w", err)
	}
	sort.Strings(nombres)

	aplicadas := make([]string, 0, len(nombres))
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryLockID); err != nil {
			return fmt.Errorf("tomar lock de migración: %w", err)
		}
		for _, nombre := range nombres {
			contenido, err := fs.ReadFile(migraciones, nombre)
			if err != nil {
				return fmt.Errorf("leer %s: %w", nombre, err)
			}
			sql := strings.TrimSpace(string(contenido))
			if sql == "" {
				continue
			}
			// Sin argumentos pgx usa el protocolo simple, que admite varias sentencias.
			if _, err := tx.Exec(ctx, sql); err != nil {
				return fmt.Errorf("aplicar %s: %w", nombre, err)
			}
			log.Debug("migración ejecutada (pendiente de commit)", "archivo", nombre)
			aplicadas = append(aplicadas, nombre)
		}
		return nil
	})
	if err != nil {
		return err
	}
	log.Info("migraciones aplicadas", "archivos", aplicadas)
	return nil
}
