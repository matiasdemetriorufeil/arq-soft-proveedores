// Command server arranca import-service: sincroniza el catálogo de FAV cada
// INTERVALO_SINCRONIZACION, lo guarda en MongoDB y publica
// ListaPreciosActualizada en RabbitMQ (ADR-005 y ADR-009).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/observabilidad"
	httpadapter "github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/adapters/http"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/adapters/mongo"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/resiliencia"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/sincronizacion"
)

const (
	nombreServicio = "import-service"
	timeoutApagado = 10 * time.Second
	// Si el broker estuvo caído, los eventos del outbox se reintentan con
	// este intervalo, sin esperar a la próxima sincronización: así el retraso
	// de ADR-005 (menos de 5 s en condiciones normales) se recupera rápido.
	intervaloOutbox = 15 * time.Second
	// Breaker: tras 3 sincronizaciones fallidas seguidas, se saltean las 3
	// siguientes sin llamar a FAV (ADR-010). La espera se mide en intervalos
	// y no en un tiempo fijo: si fuera más corta que el intervalo, el breaker
	// ya estaría semiabierto en la vuelta siguiente y nunca evitaría una llamada.
	fallasParaAbrir   = 3
	intervalosAbierto = 3
)

type config struct {
	port        string
	mongoURL    string
	mongoDB     string
	rabbitURL   string
	favURL      string
	favClave    string
	proveedorID int64
	flete       float64
	intervalo   time.Duration
}

func env(clave, porDefecto string) string {
	if v := strings.TrimSpace(os.Getenv(clave)); v != "" {
		return v
	}
	return porDefecto
}

func cargarConfig() (config, error) {
	c := config{
		port:      env("PORT", "8080"),
		mongoURL:  env("MONGO_URL", ""),
		mongoDB:   env("MONGO_DB", "importaciones"),
		rabbitURL: env("RABBITMQ_URL", ""),
		favURL:    env("FAV_URL", ""),
		favClave:  env("FAV_API_KEY", ""),
	}
	var err error
	if c.proveedorID, err = strconv.ParseInt(env("FAV_PROVEEDOR_ID", "11"), 10, 64); err != nil {
		return c, fmt.Errorf("FAV_PROVEEDOR_ID: %w", err)
	}
	if c.flete, err = strconv.ParseFloat(env("FAV_FLETE", "0"), 64); err != nil || c.flete < 0 {
		return c, fmt.Errorf("FAV_FLETE tiene que ser un número mayor o igual a 0")
	}
	if c.intervalo, err = time.ParseDuration(env("INTERVALO_SINCRONIZACION", "5m")); err != nil || c.intervalo < 10*time.Second {
		return c, fmt.Errorf("INTERVALO_SINCRONIZACION tiene que ser una duración de 10s o más (por ejemplo 5m)")
	}
	for _, o := range []struct{ clave, valor string }{
		{"MONGO_URL", c.mongoURL}, {"RABBITMQ_URL", c.rabbitURL}, {"FAV_URL", c.favURL}, {"FAV_API_KEY", c.favClave},
	} {
		if o.valor == "" {
			return c, fmt.Errorf("%s es obligatoria", o.clave)
		}
	}
	return c, nil
}

func main() {
	log := observabilidad.NuevoLogger(nombreServicio)
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

	apagarTrazas, err := observabilidad.IniciarTrazas(ctx, nombreServicio)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), timeoutApagado)
		defer cancel()
		if err := apagarTrazas(c); err != nil {
			log.Warn("no se pudieron vaciar las trazas pendientes", "error", err.Error())
		}
	}()

	cliente, err := mongo.Conectar(cfg.mongoURL)
	if err != nil {
		return fmt.Errorf("configurar MongoDB: %w", err)
	}
	defer cliente.Disconnect(context.Background())
	almacen := mongo.Nuevo(cliente, cfg.mongoDB, "fav")
	if err := crearIndicesConReintentos(ctx, log, almacen); err != nil {
		return err
	}

	pub := mensajeria.NuevoPublicador(cfg.rabbitURL)
	defer pub.Cerrar()
	if err := pub.Conectar(); err != nil {
		// No es fatal: los eventos esperan en el outbox hasta que el broker
		// vuelva.
		log.Warn("RabbitMQ no está disponible al arrancar; los eventos quedan en el outbox", "error", err.Error())
	}

	sinc := sincronizacion.Nuevo(sincronizacion.Config{
		Proveedor: mensajeria.ProveedorEvento{ID: cfg.proveedorID, Codigo: "fav", Nombre: "FAV (tienda de ropa)"},
		Reglas:    dominio.ReglasFAV(cfg.flete),
		// otelhttp agrega el traceparent a la llamada: la traza de la
		// sincronización muestra cuánto tardó FAV.
		Fuente:  fav.NuevoCliente(cfg.favURL, cfg.favClave, otelhttp.NewTransport(http.DefaultTransport)),
		Breaker: resiliencia.NuevoBreaker(fallasParaAbrir, esperaBreaker(cfg.intervalo), nil),
		Almacen: almacen,
		Pub:     pub,
		Logger:  log,
	})

	go cicloSincronizacion(ctx, log, sinc, cfg.intervalo)
	go cicloOutbox(ctx, sinc)

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           httpadapter.NuevoHandler(sinc, almacen, log),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      httpadapter.TimeoutSincronizacionManual + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("servidor escuchando", "addr", srv.Addr, "fav_url", cfg.favURL, "intervalo", cfg.intervalo.String())
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
	log.Info("apagando")
	c, cancel := context.WithTimeout(context.Background(), timeoutApagado)
	defer cancel()
	return srv.Shutdown(c)
}

// esperaBreaker es cuánto queda abierto el breaker para que se salteen
// intervalosAbierto vueltas y la siguiente haga la llamada de prueba. El medio
// intervalo de margen cubre lo que tarda una vuelta (los ticks no caen justo
// cuando terminó la anterior). Con el intervalo de 5 min son 17 min 30 s.
func esperaBreaker(intervalo time.Duration) time.Duration {
	return time.Duration(intervalosAbierto)*intervalo + intervalo/2
}

// cicloSincronizacion sincroniza al arrancar y después cada intervalo.
func cicloSincronizacion(ctx context.Context, log *slog.Logger, s *sincronizacion.Sincronizador, intervalo time.Duration) {
	t := time.NewTicker(intervalo)
	defer t.Stop()
	for {
		if _, err := s.Sincronizar(ctx); err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "la sincronización no pudo guardar en MongoDB", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func cicloOutbox(ctx context.Context, s *sincronizacion.Sincronizador) {
	t := time.NewTicker(intervaloOutbox)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.PublicarPendientes(ctx)
		}
	}
}

// crearIndicesConReintentos tolera que MongoDB todavía esté arrancando.
func crearIndicesConReintentos(ctx context.Context, log *slog.Logger, a *mongo.Almacen) error {
	var err error
	for intento := 1; intento <= 15; intento++ {
		if err = a.CrearIndices(ctx); err == nil {
			return nil
		}
		log.Warn("MongoDB todavía no responde; reintentando", "intento", intento, "error", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("crear índices en MongoDB: %w", err)
}
