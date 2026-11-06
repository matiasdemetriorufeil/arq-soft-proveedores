// Command server arranca stock-service: aplica las migraciones y expone
// POST /api/v1/stock/reservas, GET /api/v1/stock/{sku}, /healthz y /readyz.
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
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/handler"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/proveedores"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/repository"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/service"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/migrations"
)

const (
	nombreServicio     = "stock-service"
	intentosMigracion  = 15
	esperaEntreIntento = 2 * time.Second
	timeoutApagado     = 10 * time.Second
	timeoutMigracion   = 10 * time.Second
)

const (
	// intervaloReposicionPorDefecto es cada cuánto corre el ciclo que detecta
	// los faltantes (RN-14). Una hora es lo que dice ADR-009.
	intervaloReposicionPorDefecto = time.Hour
	// intervaloReposicionMinimo acota el valor configurado: para la demo se
	// baja a segundos, pero no a cero (sería un ciclo ocupado).
	intervaloReposicionMinimo = 10 * time.Second
)

type config struct {
	databaseURL string
	apiKeys     []string
	port        string

	// Reposición automática (HU-14). Sin proveedoresURL el ciclo queda
	// apagado: no puede impedir que la reserva, que es la operación
	// publicada, funcione.
	proveedoresURL      string
	proveedoresAPIKey   string
	intervaloReposicion time.Duration
}

func cargarConfig() (config, error) {
	c := config{
		databaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		port:              strings.TrimSpace(os.Getenv("PORT")),
		proveedoresURL:    strings.TrimSpace(os.Getenv("PROVEEDORES_URL")),
		proveedoresAPIKey: strings.TrimSpace(os.Getenv("PROVEEDORES_API_KEY")),
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

	c.intervaloReposicion = intervaloReposicionPorDefecto
	if v := strings.TrimSpace(os.Getenv("INTERVALO_REPOSICION")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("INTERVALO_REPOSICION inválido (%q): %w", v, err)
		}
		if d < intervaloReposicionMinimo {
			return c, fmt.Errorf("INTERVALO_REPOSICION no puede ser menor a %s", intervaloReposicionMinimo)
		}
		c.intervaloReposicion = d
	}
	// Si no hay clave propia para proveedores-service se usa la primera de las
	// nuestras: en el compose las dos puntas comparten API_KEYS.
	if c.proveedoresAPIKey == "" {
		c.proveedoresAPIKey = c.apiKeys[0]
	}
	return c, nil
}

// rutasDeSalud no se trazan: el healthcheck del contenedor pega a /readyz cada
// pocos segundos, así que trazarlas llenaría Jaeger de trazas que no cuentan
// ningún flujo. Están acá, donde se arma el servidor, para no tocar el
// adaptador HTTP que las expone.
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

	pool, err := repository.NuevoPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := migrarConReintentos(ctx, log, func(ctx context.Context) error {
		return repository.Migrar(ctx, pool, migrations.FS, log)
	}); err != nil {
		return err
	}

	// Las tres capas se componen acá y en ningún otro lugar:
	// handler → service → repository.
	repo := repository.NuevoRepo(pool)
	svc := service.NuevoServicio(repo)

	// El cliente de proveedores-service se le inyecta al servicio como una
	// interfaz: la capa de negocio no conoce el transporte. El transporte es el
	// de otelhttp para que la llamada salga con traceparent y aparezca en la
	// misma traza del ciclo en Jaeger.
	var cliente service.ClienteProveedores
	if cfg.proveedoresURL != "" {
		cliente = proveedores.NuevoCliente(cfg.proveedoresURL, cfg.proveedoresAPIKey,
			otelhttp.NewTransport(proveedores.TransportePropio()))
	} else {
		log.Warn("reposición automática apagada: falta PROVEEDORES_URL; " +
			"las consultas y transiciones de órdenes siguen disponibles")
	}
	svcOrdenes := service.NuevoServicioOrdenes(repo, cliente)

	if cliente != nil {
		// El ciclo termina con el contexto de la señal: al apagar, deja de
		// correr y no bloquea el apagado ordenado del servidor.
		go correrCicloDeReposicion(ctx, log, svcOrdenes, cfg.intervaloReposicion)
	}

	// otelhttp va por fuera del handler que arma el adaptador: continúa la traza
	// que viene en el `traceparent` del gateway y abre el span del servidor antes
	// de que corra el middleware de log, que por eso emite su línea con trace_id.
	h := otelhttp.NewHandler(
		handler.NuevoHandler(handler.Config{
			Servicio: svc,
			Ordenes:  svcOrdenes,
			Salud:    repo,
			APIKeys:  cfg.apiKeys,
			Logger:   log,
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
		Handler:           h,
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

// correrCicloDeReposicion corre el ciclo una vez al arrancar y después cada
// `intervalo`, hasta que el contexto de la señal se cancela (HU-14). Una vuelta
// que falla no detiene las siguientes: el faltante sigue estando y el ciclo
// siguiente lo vuelve a intentar.
func correrCicloDeReposicion(ctx context.Context, log *slog.Logger,
	svc *service.ServicioOrdenes, intervalo time.Duration) {

	log.Info("reposición automática encendida", "intervalo", intervalo.String())
	t := time.NewTicker(intervalo)
	defer t.Stop()

	for {
		unaVueltaDeReposicion(ctx, log, svc)
		select {
		case <-ctx.Done():
			log.Info("reposición automática detenida")
			return
		case <-t.C:
		}
	}
}

func unaVueltaDeReposicion(ctx context.Context, log *slog.Logger, svc *service.ServicioOrdenes) {
	inicio := time.Now()
	res, err := svc.CicloDeReposicion(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return // apagado: no es una falla del ciclo
		}
		log.Error("el ciclo de reposición falló", "error", err.Error())
		return
	}
	nivel := slog.LevelInfo
	if res.Fallidas > 0 {
		nivel = slog.LevelWarn
	}
	log.LogAttrs(ctx, nivel, "ciclo de reposición",
		slog.Int("bajo_minimo", res.BajoMinimo),
		slog.Int("creadas", res.Creadas),
		slog.Int("salteadas", res.Salteadas),
		slog.Int("sin_proveedor", res.SinProveedor),
		slog.Int("fallidas", res.Fallidas),
		slog.Any("problemas", res.Problemas),
		slog.Float64("duration_ms", float64(time.Since(inicio).Microseconds())/1000),
	)
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
