// Command server arranca api-gateway, la entrada única al sistema: valida la
// API key, abre el span padre del flujo y reparte la capacidad de proveedores
// entre las réplicas de proveedores-service.
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

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/observabilidad"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/api-gateway/internal/gateway"
)

const (
	nombreServicio = "api-gateway"
	timeoutApagado = 10 * time.Second

	puertoPorDefecto          = "8080"
	timeoutUpstreamPorDefecto = 3 * time.Second
	intervaloSaludPorDefecto  = 5 * time.Second
)

type config struct {
	port      string
	upstreams []string
	apiKeys   []string
	timeout   time.Duration
	intervalo time.Duration
}

// cargarConfig lee toda la configuración del entorno: no hay archivo de
// configuración ni valores en el código, así que la misma imagen sirve para
// cualquier ambiente.
func cargarConfig() (config, error) {
	c := config{
		port:      strings.TrimSpace(os.Getenv("PORT")),
		upstreams: listaDeEnv("UPSTREAMS_PROVEEDORES"),
		apiKeys:   listaDeEnv("API_KEYS"),
	}
	if c.port == "" {
		c.port = puertoPorDefecto
	}

	var err error
	if c.timeout, err = duracionDeEnv("TIMEOUT_UPSTREAM", timeoutUpstreamPorDefecto); err != nil {
		return c, err
	}
	if c.intervalo, err = duracionDeEnv("INTERVALO_SALUD", intervaloSaludPorDefecto); err != nil {
		return c, err
	}
	if len(c.upstreams) == 0 {
		return c, errors.New("UPSTREAMS_PROVEEDORES es obligatoria (URLs como http://host:puerto, separadas por coma)")
	}
	if len(c.apiKeys) == 0 {
		return c, errors.New("API_KEYS es obligatoria (lista de claves separadas por coma)")
	}
	return c, nil
}

func listaDeEnv(nombre string) []string {
	var vals []string
	for _, v := range strings.Split(os.Getenv(nombre), ",") {
		if v = strings.TrimSpace(v); v != "" {
			vals = append(vals, v)
		}
	}
	return vals
}

func duracionDeEnv(nombre string, porDefecto time.Duration) (time.Duration, error) {
	crudo := strings.TrimSpace(os.Getenv(nombre))
	if crudo == "" {
		return porDefecto, nil
	}
	d, err := time.ParseDuration(crudo)
	if err != nil {
		return 0, fmt.Errorf("%s: se espera una duración como 3s o 500ms: %w", nombre, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: tiene que ser mayor a cero", nombre)
	}
	return d, nil
}

func main() {
	log := observabilidad.NuevoLogger(nombreServicio)
	// El default también lo usa el paquete de observabilidad para avisar de los
	// fallos del exportador de trazas.
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("el gateway terminó con error", "error", err.Error())
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
		// El contexto de apagado es nuevo a propósito: el de la señal ya está
		// cancelado y los spans pendientes igual tienen que poder salir.
		apagarCtx, cancel := context.WithTimeout(context.Background(), timeoutApagado)
		defer cancel()
		if err := apagarTrazas(apagarCtx); err != nil {
			log.Warn("no se pudieron vaciar las trazas pendientes", "error", err.Error())
		}
	}()

	bal, err := gateway.NuevoBalanceador(gateway.ConfigBalanceador{
		Upstreams: cfg.upstreams,
		Intervalo: cfg.intervalo,
		Timeout:   min(cfg.timeout, cfg.intervalo),
		Logger:    log,
	})
	if err != nil {
		return err
	}
	// Primera vuelta sincrónica: el gateway no empieza a escuchar sin saber
	// cuáles réplicas están atendiendo.
	bal.IniciarSondeo(ctx)

	srv := &http.Server{
		Addr: ":" + cfg.port,
		Handler: gateway.NuevoHandler(gateway.Config{
			Balanceador:     bal,
			APIKeys:         cfg.apiKeys,
			TimeoutUpstream: cfg.timeout,
			Logger:          log,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Tiene que superar al timeout del upstream: si no, el gateway corta la
		// respuesta antes de poder devolver su propio 504.
		WriteTimeout: cfg.timeout + 10*time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway escuchando",
			"addr", srv.Addr,
			"upstreams", cfg.upstreams,
			"api_keys", len(cfg.apiKeys),
			"timeout_upstream", cfg.timeout.String(),
			"intervalo_salud", cfg.intervalo.String(),
		)
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
	log.Info("gateway detenido")
	return nil
}
