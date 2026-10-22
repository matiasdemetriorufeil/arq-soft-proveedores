// Package gateway es la entrada única al sistema: valida la API key, resuelve
// el X-Request-ID, abre el span padre del flujo y reenvía la capacidad de
// proveedores a alguna de las réplicas de proveedores-service.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Rutas que expone el gateway. La de la capacidad es la misma que la del
// servicio, para que el cliente no tenga que saber que hay un gateway en medio.
const (
	rutaCapacidad = "/api/v1/proveedores/recomendados"
	rutaSalud     = "/healthz"
	rutaListo     = "/readyz"
)

// timeoutUpstreamPorDefecto se aplica si Config llega sin timeout, para que un
// cero no se traduzca en un contexto ya vencido.
const timeoutUpstreamPorDefecto = 3 * time.Second

// Config agrupa las dependencias del handler del gateway.
type Config struct {
	Balanceador     *Balanceador
	APIKeys         []string
	TimeoutUpstream time.Duration
	Logger          *slog.Logger
}

// NuevoHandler arma el router con sus middlewares.
func NuevoHandler(cfg Config) http.Handler {
	p := &proxy{bal: cfg.Balanceador, timeout: cfg.TimeoutUpstream}
	if p.timeout <= 0 {
		p.timeout = timeoutUpstreamPorDefecto
	}
	p.reverse = &httputil.ReverseProxy{
		Rewrite: p.reescribir,
		// otelhttp instrumenta la llamada de salida: crea el span hijo y mete
		// el traceparent en los headers con el propagador global.
		Transport:      otelhttp.NewTransport(http.DefaultTransport),
		ErrorHandler:   p.anotarFallo,
		ModifyResponse: limpiarRequestID,
		ErrorLog:       slog.NewLogLogger(cfg.Logger.Handler(), slog.LevelError),
	}

	mux := http.NewServeMux()
	mux.Handle("GET "+rutaCapacidad, conAPIKey(cfg.APIKeys, http.HandlerFunc(p.reenviar)))
	mux.HandleFunc("GET "+rutaSalud, p.healthz)
	mux.HandleFunc("GET "+rutaListo, p.readyz)
	mux.HandleFunc("/", noEncontrado)

	return conObservabilidad(cfg.Logger, mux)
}

type proxy struct {
	bal     *Balanceador
	reverse *httputil.ReverseProxy
	timeout time.Duration
}

// intento guarda el resultado de un pase por el ReverseProxy. Viaja por el
// contexto porque es lo que comparten el Rewrite, el ErrorHandler y reenviar.
type intento struct {
	instancia *Instancia
	err       error
}

func intentoDe(ctx context.Context) *intento {
	i, _ := ctx.Value(ctxIntento).(*intento)
	return i
}

// reenviar elige una réplica y le pasa la request. Si la conexión falla, marca
// la réplica como caída y reintenta una sola vez en otra: el GET de la
// capacidad no tiene efectos, así que repetirlo es seguro.
func (p *proxy) reenviar(w http.ResponseWriter, r *http.Request) {
	inst, ok := p.bal.Siguiente()
	if !ok {
		p.sinReplicas(w, r)
		return
	}
	if p.intentar(w, r, inst) {
		return
	}

	ctx := r.Context()
	p.bal.MarcarCaida(ctx, inst)
	otra, ok := p.bal.Otra(inst)
	if !ok {
		p.sinReplicas(w, r)
		return
	}

	LoggerDe(ctx).WarnContext(ctx, "la réplica no respondió: se reintenta en otra",
		"upstream_fallido", inst.Destino(), "upstream", otra.Destino())
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("upstream_fallido", inst.Destino()))

	if p.intentar(w, r, otra) {
		return
	}
	p.bal.MarcarCaida(ctx, otra)
	p.sinReplicas(w, r)
}

// intentar reenvía la request a la instancia y devuelve si quedó respondida.
// Devuelve false solo cuando falló la conexión sin haber escrito nada: ese es
// el único caso en el que se puede reintentar en otra réplica.
func (p *proxy) intentar(w http.ResponseWriter, r *http.Request, inst *Instancia) bool {
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()

	estado := &intento{instancia: inst}
	ctx = context.WithValue(ctx, ctxIntento, estado)

	// Último que escribe gana: el atributo queda apuntando a la réplica que
	// efectivamente atendió, que es lo que se quiere ver en Jaeger.
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("upstream", inst.Destino()))

	rw := &respuestaConEstado{ResponseWriter: w}
	p.reverse.ServeHTTP(rw, r.WithContext(ctx))

	// El upstream respondió (con 200 o con su propio error del contrato): el
	// cuerpo ya viajó tal cual al cliente.
	if estado.err == nil || rw.status != 0 {
		return true
	}
	// El cliente se fue: no hay a quién responderle ni para quién reintentar.
	if r.Context().Err() != nil {
		return true
	}
	// Timeout: la réplica está viva pero lenta, reintentar solo suma latencia.
	if errors.Is(estado.err, context.DeadlineExceeded) {
		p.responderFallo(w, r, http.StatusGatewayTimeout, inst, estado.err)
		return true
	}
	return false
}

// reescribir apunta la request de salida a la réplica elegida. Solo cambia el
// destino: path, query y headers —incluido el X-API-Key, que el upstream
// también exige— viajan tal cual.
func (p *proxy) reescribir(pr *httputil.ProxyRequest) {
	destino := intentoDe(pr.In.Context()).instancia.URL
	pr.Out.URL.Scheme = destino.Scheme
	pr.Out.URL.Host = destino.Host
	pr.Out.Host = destino.Host
	pr.SetXForwarded()
	// El upstream devuelve el X-Request-ID que recibe: así la misma request se
	// busca con el mismo id en los logs del gateway y en los del servicio.
	pr.Out.Header.Set(headerRequestID, requestIDDe(pr.In.Context()))
}

// limpiarRequestID saca el X-Request-ID de la respuesta del upstream. El
// gateway ya lo puso en la respuesta antes de reenviar, y ReverseProxy suma los
// headers del upstream a los que ya están: sin esto el cliente recibiría el
// header dos veces.
func limpiarRequestID(res *http.Response) error {
	res.Header.Del(headerRequestID)
	return nil
}

// anotarFallo es el ErrorHandler del ReverseProxy. No escribe la respuesta:
// anota el error del intento y deja que reenviar decida si reintenta o
// responde. ReverseProxy lo llama cuando falla el RoundTrip, siempre antes de
// escribir nada.
func (p *proxy) anotarFallo(_ http.ResponseWriter, r *http.Request, err error) {
	if estado := intentoDe(r.Context()); estado != nil {
		estado.err = err
	}
}

func (p *proxy) responderFallo(w http.ResponseWriter, r *http.Request, status int, inst *Instancia, err error) {
	ctx := r.Context()
	LoggerDe(ctx).ErrorContext(ctx, "falla al reenviar al upstream",
		"upstream", inst.Destino(), "status", status, "error", err.Error())
	trace.SpanFromContext(ctx).RecordError(err)

	detalle := "no se pudo contactar a proveedores-service; reintentar más tarde"
	if status == http.StatusGatewayTimeout {
		detalle = "proveedores-service no respondió dentro del tiempo límite; reintentar más tarde"
	}
	escribirProblema(w, r, status, CodigoNoDisponible, detalle)
}

func (p *proxy) sinReplicas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	LoggerDe(ctx).ErrorContext(ctx, "ninguna réplica de proveedores-service está disponible",
		"replicas", len(p.bal.Instancias()))
	escribirProblema(w, r, http.StatusBadGateway, CodigoNoDisponible,
		"ninguna réplica de proveedores-service responde; reintentar más tarde")
}

// healthz dice si el proceso del gateway atiende; no mira a los upstreams, para
// que un upstream caído no haga reiniciar al gateway.
func (p *proxy) healthz(w http.ResponseWriter, _ *http.Request) {
	escribirJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz dice si el gateway puede atender de verdad, o sea si le queda alguna
// réplica a la que mandar requests.
func (p *proxy) readyz(w http.ResponseWriter, r *http.Request) {
	if !p.bal.AlgunaSana() {
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"ninguna réplica de proveedores-service responde")
		return
	}
	escribirJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func noEncontrado(w http.ResponseWriter, r *http.Request) {
	escribirProblema(w, r, http.StatusNotFound, CodigoRutaNoEncontrada,
		"el gateway no expone esta ruta")
}
