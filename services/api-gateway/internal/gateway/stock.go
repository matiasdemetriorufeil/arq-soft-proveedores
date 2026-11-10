package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// prefijoStock es todo lo que el gateway publica de stock-service: consultar
// stock, reservar y ver o mover las órdenes de reposición. El webhook de FAV
// (/webhooks/fav) queda afuera a propósito: FAV lo llama directo, nunca por el
// gateway (ADR-009).
const prefijoStock = "/api/v1/stock/"

// proxyStock reenvía a la única instancia de stock-service. No hay balanceo ni
// reintento: la reserva es un POST, y aunque es idempotente por
// Idempotency-Key, reintentarla es decisión del cliente, que es quien tiene
// la clave.
type proxyStock struct {
	destino *url.URL
	reverse *httputil.ReverseProxy
	timeout time.Duration
}

type ctxErrStock struct{}

func nuevoProxyStock(destino *url.URL, timeout time.Duration, log *slog.Logger) *proxyStock {
	p := &proxyStock{destino: destino, timeout: timeout}
	p.reverse = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(destino)
			// SetURL une el path del destino con el de la request; el destino
			// no tiene path, así que queda el mismo /api/v1/stock/...
			pr.Out.Host = destino.Host
			pr.SetXForwarded()
			pr.Out.Header.Set(headerRequestID, requestIDDe(pr.In.Context()))
		},
		Transport:      otelhttp.NewTransport(http.DefaultTransport),
		ModifyResponse: limpiarRequestID,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if e, ok := r.Context().Value(ctxErrStock{}).(*error); ok {
				*e = err
			}
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	return p
}

func (p *proxyStock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), p.timeout)
	defer cancel()
	var errUpstream error
	ctx = context.WithValue(ctx, ctxErrStock{}, &errUpstream)
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("upstream", p.destino.Host))

	rw := &respuestaConEstado{ResponseWriter: w}
	p.reverse.ServeHTTP(rw, r.WithContext(ctx))
	if errUpstream == nil || rw.status != 0 || r.Context().Err() != nil {
		return
	}

	LoggerDe(ctx).ErrorContext(ctx, "falla al reenviar a stock-service",
		"upstream", p.destino.Host, "error", errUpstream.Error())
	trace.SpanFromContext(ctx).RecordError(errUpstream)
	if errors.Is(errUpstream, context.DeadlineExceeded) {
		escribirProblema(w, r, http.StatusGatewayTimeout, CodigoNoDisponible,
			"stock-service no respondió dentro del tiempo límite; reintentar más tarde")
		return
	}
	escribirProblema(w, r, http.StatusBadGateway, CodigoNoDisponible,
		"no se pudo contactar a stock-service; reintentar más tarde")
}
