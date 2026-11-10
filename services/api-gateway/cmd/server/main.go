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
	"net/url"
	"os"
	"os/signal"
	"strconv"
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

	// Límite de tráfico (ADR-010). 50 req/s sostenidas con ráfagas de 100 por
	// identidad queda por debajo del punto de colapso medido en ADR-013 y muy
	// por encima de lo que necesita el grupo de paneles solares.
	limitePorSegundoDefecto = 50
	limiteRafagaDefecto     = 100
	// El login es lo que se ataca por fuerza bruta: 5 intentos de golpe y uno
	// cada 5 s después, por IP.
	loginPorSegundoDefecto = 0.2
	loginRafagaDefecto     = 5

	puertoMetricasDefecto = "9091"
	minLargoSecretoJWT    = 32
)

type config struct {
	port      string
	upstreams []string
	apiKeys   []string
	timeout   time.Duration
	intervalo time.Duration

	usuarios     map[string]string
	secretoJWT   string
	duracionJWT  time.Duration
	claveInterna string
	stockURL     *url.URL

	limitePorSegundo float64
	limiteRafaga     int
	loginPorSegundo  float64
	loginRafaga      int

	puertoMetricas string
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

	// Login del frontend: opcional. Sin USUARIOS o sin JWT_SECRETO, la ruta de
	// login no existe y el gateway funciona como en la Entrega 2.
	if c.usuarios, err = usuariosDeEnv("USUARIOS"); err != nil {
		return c, err
	}
	c.secretoJWT = strings.TrimSpace(os.Getenv("JWT_SECRETO"))
	if c.secretoJWT != "" && len(c.secretoJWT) < minLargoSecretoJWT {
		return c, fmt.Errorf("JWT_SECRETO: tiene que tener al menos %d caracteres", minLargoSecretoJWT)
	}
	if c.duracionJWT, err = duracionDeEnv("DURACION_JWT", time.Hour); err != nil {
		return c, err
	}
	c.claveInterna = strings.TrimSpace(os.Getenv("CLAVE_INTERNA"))

	if crudo := strings.TrimSpace(os.Getenv("STOCK_URL")); crudo != "" {
		u, err := url.Parse(crudo)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return c, fmt.Errorf("STOCK_URL: se espera una URL como http://stock-service:8080, llegó %q", crudo)
		}
		c.stockURL = u
	}

	if c.limitePorSegundo, err = numeroDeEnv("LIMITE_POR_SEGUNDO", limitePorSegundoDefecto); err != nil {
		return c, err
	}
	if c.loginPorSegundo, err = numeroDeEnv("LOGIN_POR_SEGUNDO", loginPorSegundoDefecto); err != nil {
		return c, err
	}
	var rafaga float64
	if rafaga, err = numeroDeEnv("LIMITE_RAFAGA", limiteRafagaDefecto); err != nil {
		return c, err
	}
	c.limiteRafaga = int(rafaga)
	if rafaga, err = numeroDeEnv("LOGIN_RAFAGA", loginRafagaDefecto); err != nil {
		return c, err
	}
	c.loginRafaga = int(rafaga)

	c.puertoMetricas = strings.TrimSpace(os.Getenv("PUERTO_METRICAS"))
	if c.puertoMetricas == "" {
		c.puertoMetricas = puertoMetricasDefecto
	}
	return c, nil
}

// usuariosDeEnv lee "usuario:contraseña,otro:otra". Son los usuarios de demo
// del frontend; ver la deuda técnica en ARCHITECTURE.md.
func usuariosDeEnv(nombre string) (map[string]string, error) {
	usuarios := map[string]string{}
	for _, par := range listaDeEnv(nombre) {
		u, c, ok := strings.Cut(par, ":")
		u, c = strings.TrimSpace(u), strings.TrimSpace(c)
		if !ok || u == "" || c == "" {
			return nil, fmt.Errorf("%s: cada entrada tiene que ser usuario:contraseña", nombre)
		}
		usuarios[u] = c
	}
	return usuarios, nil
}

// numeroDeEnv acepta 0 para apagar el límite.
func numeroDeEnv(nombre string, porDefecto float64) (float64, error) {
	crudo := strings.TrimSpace(os.Getenv(nombre))
	if crudo == "" {
		return porDefecto, nil
	}
	n, err := strconv.ParseFloat(crudo, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: se espera un número mayor o igual a cero, llegó %q", nombre, crudo)
	}
	return n, nil
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

	metricas := gateway.NuevasMetricas(bal.Sanas)

	srv := &http.Server{
		Addr: ":" + cfg.port,
		Handler: gateway.NuevoHandler(gateway.Config{
			Balanceador:      bal,
			APIKeys:          cfg.apiKeys,
			TimeoutUpstream:  cfg.timeout,
			Logger:           log,
			Usuarios:         cfg.usuarios,
			SecretoJWT:       []byte(cfg.secretoJWT),
			DuracionJWT:      cfg.duracionJWT,
			ClaveInterna:     cfg.claveInterna,
			StockURL:         cfg.stockURL,
			LimitePorSegundo: cfg.limitePorSegundo,
			LimiteRafaga:     cfg.limiteRafaga,
			LoginPorSegundo:  cfg.loginPorSegundo,
			LoginRafaga:      cfg.loginRafaga,
			Metricas:         metricas,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Tiene que superar al timeout del upstream: si no, el gateway corta la
		// respuesta antes de poder devolver su propio 504.
		WriteTimeout: cfg.timeout + 10*time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	// Las métricas van en otro puerto, que el compose no publica: Prometheus
	// las lee por la red interna y no quedan expuestas en la URL pública.
	srvMetricas := &http.Server{
		Addr:              ":" + cfg.puertoMetricas,
		Handler:           metricas.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srvMetricas.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("el servidor de métricas se detuvo", "error", err.Error())
		}
	}()
	defer srvMetricas.Close()

	errCh := make(chan error, 1)
	go func() {
		log.Info("gateway escuchando",
			"addr", srv.Addr,
			"upstreams", cfg.upstreams,
			"api_keys", len(cfg.apiKeys),
			"timeout_upstream", cfg.timeout.String(),
			"intervalo_salud", cfg.intervalo.String(),
			"login", len(cfg.usuarios) > 0 && cfg.secretoJWT != "",
			"stock_url", fmt.Sprint(cfg.stockURL),
			"limite_por_segundo", cfg.limitePorSegundo,
			"puerto_metricas", cfg.puertoMetricas,
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
