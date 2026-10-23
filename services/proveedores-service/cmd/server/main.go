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

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/observabilidad"
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

// rutasDeSalud no se trazan: el healthcheck del contenedor pega a /readyz y el
// gateway a /healthz cada pocos segundos, así que trazarlas llenaría Jaeger de
// trazas que no cuentan ningún flujo. Están acá, donde se arma el servidor, para
// no tocar el adaptador HTTP que las expone.
var rutasDeSalud = map[string]bool{"/healthz": true, "/readyz": true}

func main() {
	// El logger compartido agrega trace_id y span_id a toda línea que se emita
	// con el contexto de la request: es la correlación entre los logs y Jaeger.
	log := observabilidad.NuevoLogger(nombreServicio)
	// El default también lo usa el paquete de observabilidad para avisar de los
	// fallos del exportador de trazas.
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

	// Antes del pool: así el tracer de las consultas ya encuentra puesto el
	// proveedor global de trazas. Sin colector OTLP configurado esto no falla,
	// solo deja las trazas en no-op.
	apagarTrazas, err := observabilidad.IniciarTrazas(ctx, nombreServicio)
	if err != nil {
		return err
	}
	defer func() {
		// El contexto de apagado es nuevo a propósito: el de la señal ya está
		// cancelado y los spans pendientes igual tienen que poder salir.
		apagarCtx, cancel := context.WithTimeout(context.Background(), timeoutApagado)
		defer cancel()
		if err := apagarTrazas(apagarCtx); err != nil {
			log.Warn("no se pudieron vaciar las trazas pendientes", "error", err.Error())
		}
	}()

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

	// otelhttp va por fuera del handler que arma el adaptador: continúa la traza
	// que viene en el `traceparent` del gateway y abre el span del servidor antes
	// de que corra el middleware de log, que por eso emite su línea con trace_id.
	handler := otelhttp.NewHandler(
		httpadapter.NuevoHandler(httpadapter.Config{
			Repo:    repo,
			Salud:   repo,
			APIKeys: cfg.apiKeys,
			Logger:  log,
		}),
		// El nombre del span lo arma el formateador de abajo, no esta etiqueta.
		"",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return !rutasDeSalud[r.URL.Path]
		}),
	)

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           handler,
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
