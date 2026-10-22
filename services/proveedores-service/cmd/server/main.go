// Command server arranca proveedores-service: aplica las migraciones y expone
// GET /api/v1/proveedores/recomendados, /healthz y /readyz.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	httpadapter "github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/http"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/adapters/postgres"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/migrations"
)

const (
	nombreServicio     = "proveedores-service"
	intentosMigracion  = 15
	esperaEntreIntento = 2 * time.Second
	timeoutApagado     = 10 * time.Second
	timeoutMigracion   = 10 * time.Second
)

type config struct {
	databaseURL string
	apiKeys     []string
	port        string
}

func cargarConfig() (config, error) {
	c := config{
		databaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		port:        strings.TrimSpace(os.Getenv("PORT")),
	}
	if c.port == "" {
		c.port = "8080"
	}
	for _, k := range strings.Split(os.Getenv("API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			c.apiKeys = append(c.apiKeys, k)
		}
	}
	if c.databaseURL == "" {
		return c, errors.New("DATABASE_URL es obligatoria")
	}
	if len(c.apiKeys) == 0 {
		return c, errors.New("API_KEYS es obligatoria (lista de claves separadas por coma)")
	}
	return c, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", nombreServicio)
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("el servicio terminó con error", "error", err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := cargarConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NuevoPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := migrarConReintentos(ctx, log, func(ctx context.Context) error {
		return postgres.Migrar(ctx, pool, migrations.FS, log)
	}); err != nil {
		return err
	}

	repo := postgres.NuevoRepo(pool)
	srv := &http.Server{
		Addr: ":" + cfg.port,
		Handler: httpadapter.NuevoHandler(httpadapter.Config{
			Repo:    repo,
			Salud:   repo,
			APIKeys: cfg.apiKeys,
			Logger:  log,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("servidor escuchando", "addr", srv.Addr, "api_keys", len(cfg.apiKeys))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("apagando: se dejan de aceptar conexiones y se terminan las requests en curso")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeoutApagado)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("apagado ordenado: %w", err)
	}
	log.Info("servidor detenido")
	return nil
}

// migrarConReintentos tolera que Postgres todavía esté arrancando (por
// ejemplo, al levantar todo con docker compose).
func migrarConReintentos(ctx context.Context, log *slog.Logger, migrar func(context.Context) error) error {
	var err error
	for intento := 1; intento <= intentosMigracion; intento++ {
		// Cada intento tiene su propio timeout: una base que acepta la conexión
		// pero no responde (o un lock trabado) no puede bloquear los reintentos.
		intentoCtx, cancel := context.WithTimeout(ctx, timeoutMigracion)
		err = migrar(intentoCtx)
		cancel()
		if err == nil {
			return nil
		}
		log.Warn("no se pudieron aplicar las migraciones; reintentando",
			"intento", intento, "max", intentosMigracion, "error", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(esperaEntreIntento):
		}
	}
	return fmt.Errorf("migraciones: %w", err)
}
