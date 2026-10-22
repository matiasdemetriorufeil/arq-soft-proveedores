package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	apiKeyValida = "clave-de-prueba"
	rutaDePrueba = rutaCapacidad + "?producto=PAN-450W-MONO&cantidad=10"
	// cuerpoDelServicio es una respuesta del contrato recortada: alcanza para
	// verificar que el gateway no la toca.
	cuerpoDelServicio = `[{"proveedor_id":1,"proveedor_nombre":"Solar Sur","costo_total":4185.5}]`
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

// --- réplica de mentira -------------------------------------------------------

// llamada es lo que la réplica vio de una request reenviada.
type llamada struct {
	url     string
	headers http.Header
}

// replica hace de proveedores-service: responde /healthz, cuenta las requests
// de la capacidad y guarda la última para poder mirar qué le llegó.
type replica struct {
	srv       *httptest.Server
	recibidas atomic.Int32
	ultima    atomic.Pointer[llamada]
}

type opcionesReplica struct {
	demora time.Duration
	status int
	cuerpo string
}

func nuevaReplica(t *testing.T, opts opcionesReplica) *replica {
	t.Helper()
	if opts.status == 0 {
		opts.status = http.StatusOK
	}
	if opts.cuerpo == "" {
		opts.cuerpo = cuerpoDelServicio
	}

	r := &replica{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == rutaSalud {
			w.WriteHeader(http.StatusOK)
			return
		}
		r.recibidas.Add(1)
		r.ultima.Store(&llamada{url: req.URL.String(), headers: req.Header.Clone()})

		if opts.demora > 0 {
			select {
			case <-time.After(opts.demora):
			case <-req.Context().Done():
				return
			}
		}
		// El servicio devuelve el X-Request-ID que recibe.
		w.Header().Set(headerRequestID, req.Header.Get(headerRequestID))
		w.Header().Set("Content-Type", contentTypeJSON)
		w.WriteHeader(opts.status)
		_, _ = io.WriteString(w, opts.cuerpo)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *replica) url() string  { return r.srv.URL }
func (r *replica) host() string { return strings.TrimPrefix(r.srv.URL, "http://") }

// --- logger que deja mirar el contexto ---------------------------------------

// handlerQueEspia es el logger de los tests: guarda el JSON de cada línea y
// además el span context con el que se emitió. Lo segundo es lo que el handler
// de pkg/observabilidad convierte en trace_id, así que ver un span válido acá
// es ver que el gateway dejó la request correlacionada.
type handlerQueEspia struct {
	slog.Handler
	buf    *bytes.Buffer
	spans  []trace.SpanContext
	previo *handlerQueEspia
}

func nuevoEspia() *handlerQueEspia {
	buf := &bytes.Buffer{}
	return &handlerQueEspia{Handler: slog.NewJSONHandler(buf, nil), buf: buf}
}

func (h *handlerQueEspia) Handle(ctx context.Context, r slog.Record) error {
	raiz := h
	for raiz.previo != nil {
		raiz = raiz.previo
	}
	raiz.spans = append(raiz.spans, trace.SpanContextFromContext(ctx))
	return h.Handler.Handle(ctx, r)
}

func (h *handlerQueEspia) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handlerQueEspia{Handler: h.Handler.WithAttrs(attrs), buf: h.buf, previo: h}
}

// reiniciar olvida lo capturado hasta ahora. Se usa después del armado, para
// que el sondeo inicial —que loguea sin span porque no hay ninguna request— no
// cuente como línea del test.
func (h *handlerQueEspia) reiniciar() {
	raiz := h
	for raiz.previo != nil {
		raiz = raiz.previo
	}
	raiz.buf.Reset()
	raiz.spans = nil
}

func (h *handlerQueEspia) lineas(t *testing.T) []map[string]any {
	t.Helper()
	var lineas []map[string]any
	for _, cruda := range strings.Split(strings.TrimSpace(h.buf.String()), "\n") {
		if cruda == "" {
			continue
		}
		var linea map[string]any
		if err := json.Unmarshal([]byte(cruda), &linea); err != nil {
			t.Fatalf("línea de log que no es JSON (%v): %s", err, cruda)
		}
		lineas = append(lineas, linea)
	}
	return lineas
}

// --- armado del gateway -------------------------------------------------------

type opcionesGateway struct {
	timeout  time.Duration
	espia    *handlerQueEspia
	upstream []string
}

func armarGateway(t *testing.T, opts opcionesGateway) (http.Handler, *Balanceador) {
	t.Helper()
	if opts.timeout == 0 {
		opts.timeout = 2 * time.Second
	}
	log := loggerSilencioso()
	if opts.espia != nil {
		log = slog.New(opts.espia)
	}

	// El balanceador comparte el logger con el handler, igual que en
	// cmd/server/main.go: así una transición disparada por una request se ve en
	// las mismas líneas que la request.
	bal, err := NuevoBalanceador(ConfigBalanceador{
		Upstreams: opts.upstream,
		Intervalo: time.Hour, // los tests sondean a mano
		Timeout:   500 * time.Millisecond,
		Logger:    log,
	})
	if err != nil {
		t.Fatalf("NuevoBalanceador: %v", err)
	}
	bal.Sondear(context.Background())
	if opts.espia != nil {
		opts.espia.reiniciar()
	}

	h := NuevoHandler(Config{
		Balanceador:     bal,
		APIKeys:         []string{apiKeyValida},
		TimeoutUpstream: opts.timeout,
		Logger:          log,
	})
	return h, bal
}

func pedir(h http.Handler, ruta string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, ruta, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func conAPIKeyValida() map[string]string { return map[string]string{headerAPIKey: apiKeyValida} }

// conTrazasDePrueba instala un proveedor de trazas del SDK para poder afirmar
// sobre los spans que abre el gateway, y lo saca al terminar.
func conTrazasDePrueba(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	previo := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(previo)
	})
	return rec
}

func atributo(span sdktrace.ReadOnlySpan, clave string) (attribute.Value, bool) {
	for _, a := range span.Attributes() {
		if string(a.Key) == clave {
			return a.Value, true
		}
	}
	return attribute.Value{}, false
}

func problemaDe(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != contentTypeProblem {
		t.Errorf("Content-Type = %q; se esperaba %q", ct, contentTypeProblem)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("el error no es problem+json (%v): %s", err, rec.Body.String())
	}
	if p.Status != rec.Code {
		t.Errorf("el status del cuerpo (%d) no coincide con el de la respuesta (%d)", p.Status, rec.Code)
	}
	return p
}

// --- Feliz --------------------------------------------------------------------

func TestFelizDevuelveElCuerpoDelServicioIntacto(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	rec := pedir(h, rutaDePrueba, conAPIKeyValida())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; se esperaba 200. Cuerpo: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != cuerpoDelServicio {
		t.Errorf("el gateway tocó el cuerpo:\n got: %s\nwant: %s", got, cuerpoDelServicio)
	}
	if rec.Header().Get(headerRequestID) == "" {
		t.Error("falta el X-Request-ID en la respuesta")
	}
	// Path y query tienen que llegar tal cual al servicio.
	if l := upstream.ultima.Load(); l == nil {
		t.Fatal("el upstream no recibió la request")
	} else if l.url != rutaDePrueba {
		t.Errorf("el upstream recibió %q; se esperaba %q", l.url, rutaDePrueba)
	}
}

func TestFelizReenviaLaAPIKeyYElTraceparent(t *testing.T) {
	conTrazasDePrueba(t)
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	pedir(h, rutaDePrueba, conAPIKeyValida())

	l := upstream.ultima.Load()
	if l == nil {
		t.Fatal("el upstream no recibió la request")
	}
	if got := l.headers.Get(headerAPIKey); got != apiKeyValida {
		t.Errorf("X-API-Key reenviada = %q; se esperaba %q", got, apiKeyValida)
	}
	if got := l.headers.Get("traceparent"); got == "" {
		t.Error("falta el traceparent: sin él la traza se corta en el gateway")
	}
}

// --- X-Request-ID -------------------------------------------------------------

func TestRequestIDEntranteSeRespetaYVuelveIgual(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	espia := nuevoEspia()
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}, espia: espia})

	rec := pedir(h, rutaDePrueba, map[string]string{
		headerAPIKey:    apiKeyValida,
		headerRequestID: "abc123",
	})

	if got := rec.Header().Get(headerRequestID); got != "abc123" {
		t.Errorf("X-Request-ID devuelto = %q; se esperaba abc123", got)
	}
	// El servicio devuelve el mismo header y ReverseProxy suma los del upstream
	// a los que ya están: tiene que volver uno solo, no dos.
	if vals := rec.Header().Values(headerRequestID); len(vals) != 1 {
		t.Errorf("el X-Request-ID volvió %d veces (%v); tiene que volver una", len(vals), vals)
	}
	if l := upstream.ultima.Load(); l == nil || l.headers.Get(headerRequestID) != "abc123" {
		t.Error("el request id entrante tiene que viajar al upstream")
	}
	for _, linea := range espia.lineas(t) {
		if linea["request_id"] != "abc123" {
			t.Errorf("línea de log sin el request id entrante: %v", linea)
		}
	}
}

func TestSinRequestIDSeGeneraDe32Hex(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	for nombre, headers := range map[string]map[string]string{
		"ausente":      {headerAPIKey: apiKeyValida},
		"vacío":        {headerAPIKey: apiKeyValida, headerRequestID: ""},
		"con espacios": {headerAPIKey: apiKeyValida, headerRequestID: "con espacios"},
		"larguísimo":   {headerAPIKey: apiKeyValida, headerRequestID: strings.Repeat("a", maxLongitudRequestID+1)},
	} {
		t.Run(nombre, func(t *testing.T) {
			rec := pedir(h, rutaDePrueba, headers)
			id := rec.Header().Get(headerRequestID)
			if !hex32.MatchString(id) {
				t.Errorf("X-Request-ID = %q; se esperaban 32 hex", id)
			}
		})
	}
}

// --- API key ------------------------------------------------------------------

func TestSinAPIKeyValidaNoSeLlamaAlUpstream(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	for nombre, headers := range map[string]map[string]string{
		"ausente":    {},
		"vacía":      {headerAPIKey: ""},
		"incorrecta": {headerAPIKey: "clave-que-no-es"},
		"prefijo":    {headerAPIKey: apiKeyValida[:5]},
	} {
		t.Run(nombre, func(t *testing.T) {
			rec := pedir(h, rutaDePrueba, headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d; se esperaba 401", rec.Code)
			}
			p := problemaDe(t, rec)
			if p.Code != CodigoNoAutorizado {
				t.Errorf("code = %q; se esperaba %q", p.Code, CodigoNoAutorizado)
			}
			if p.RequestID == "" {
				t.Error("el problem+json tiene que traer el request_id")
			}
			if rec.Header().Get(headerRequestID) == "" {
				t.Error("el X-Request-ID se devuelve también en los errores")
			}
		})
	}
	if n := upstream.recibidas.Load(); n != 0 {
		t.Errorf("el upstream recibió %d requests sin API key válida; se esperaban 0", n)
	}
}

// --- Reparto ------------------------------------------------------------------

func TestRepartoEntreDosReplicasSanas(t *testing.T) {
	rec := conTrazasDePrueba(t)
	uno := nuevaReplica(t, opcionesReplica{})
	dos := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{uno.url(), dos.url()}})

	for range 4 {
		if r := pedir(h, rutaDePrueba, conAPIKeyValida()); r.Code != http.StatusOK {
			t.Fatalf("status = %d; se esperaba 200", r.Code)
		}
	}

	if uno.recibidas.Load() != 2 || dos.recibidas.Load() != 2 {
		t.Errorf("reparto = %d y %d; se esperaba 2 y 2", uno.recibidas.Load(), dos.recibidas.Load())
	}

	// El atributo `upstream` del span es lo que muestra el reparto en Jaeger.
	porUpstream := map[string]int{}
	for _, span := range rec.Ended() {
		if v, ok := atributo(span, "upstream"); ok {
			porUpstream[v.AsString()]++
		}
	}
	if porUpstream[uno.host()] != 2 || porUpstream[dos.host()] != 2 {
		t.Errorf("atributo upstream de los spans = %v; se esperaba 2 y 2", porUpstream)
	}
}

// --- Réplicas caídas ----------------------------------------------------------

// Una réplica que se cae entre dos sondeos: la request que le toca falla la
// conexión, se reintenta en la otra y la caída queda marcada.
func TestUnaReplicaCaidaSeReintentaEnLaOtraYQuedaMarcada(t *testing.T) {
	conTrazasDePrueba(t)
	espia := nuevoEspia()
	uno := nuevaReplica(t, opcionesReplica{})
	dos := nuevaReplica(t, opcionesReplica{})
	h, bal := armarGateway(t, opcionesGateway{upstream: []string{uno.url(), dos.url()}, espia: espia})

	uno.srv.Close() // se cae sin que el sondeo se haya enterado
	caida, viva := bal.Instancias()[0], bal.Instancias()[1]

	rec := pedir(h, rutaDePrueba, conAPIKeyValida())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; se esperaba 200 por la réplica que quedó viva. Cuerpo: %s",
			rec.Code, rec.Body.String())
	}
	if caida.Sana() {
		t.Error("la réplica que falló la conexión tendría que quedar marcada")
	}
	if !viva.Sana() {
		t.Error("la réplica que atendió tiene que seguir sana")
	}

	// Marcada quiere decir que deja de recibir: todo lo que sigue va a la viva.
	antes := dos.recibidas.Load()
	for range 3 {
		if r := pedir(h, rutaDePrueba, conAPIKeyValida()); r.Code != http.StatusOK {
			t.Fatalf("status = %d; se esperaba 200", r.Code)
		}
	}
	if dos.recibidas.Load()-antes != 3 {
		t.Errorf("la réplica viva recibió %d de 3 requests posteriores", dos.recibidas.Load()-antes)
	}

	// La línea que explica el failover es la que después se busca por trace_id:
	// tiene que salir con el span de la request, no con un contexto vacío.
	var vistaLaTransicion bool
	for i, linea := range espia.lineas(t) {
		if linea["msg"] != "upstream caído: deja de recibir requests" {
			continue
		}
		vistaLaTransicion = true
		if !espia.spans[i].IsValid() {
			t.Error("la línea del failover salió sin span: no tendría trace_id")
		}
		if linea["request_id"] == nil || linea["request_id"] == "" {
			t.Errorf("la línea del failover salió sin request_id: %v", linea)
		}
	}
	if !vistaLaTransicion {
		t.Error("marcar la réplica como caída tiene que dejar una línea de log")
	}
}

func TestLasDosCaidasDevuelve502(t *testing.T) {
	espia := nuevoEspia()
	uno := nuevaReplica(t, opcionesReplica{})
	dos := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{uno.url(), dos.url()}, espia: espia})

	uno.srv.Close()
	dos.srv.Close()

	rec := pedir(h, rutaDePrueba, map[string]string{
		headerAPIKey:    apiKeyValida,
		headerRequestID: "req-sin-upstreams",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; se esperaba 502. Cuerpo: %s", rec.Code, rec.Body.String())
	}
	p := problemaDe(t, rec)
	if p.Code != CodigoNoDisponible {
		t.Errorf("code = %q; se esperaba %q", p.Code, CodigoNoDisponible)
	}

	var hayError bool
	for _, linea := range espia.lineas(t) {
		if linea["level"] == "ERROR" {
			hayError = true
			if linea["request_id"] != "req-sin-upstreams" {
				t.Errorf("la línea de error no trae el request_id: %v", linea)
			}
		}
	}
	if !hayError {
		t.Error("con las dos réplicas caídas tiene que quedar una línea de log en ERROR")
	}
}

// Cuando el sondeo ya las dio por caídas, el gateway responde 502 sin intentar
// la conexión: no hay a quién mandarle la request.
func TestSinReplicasSanasNoSeIntentaLaConexion(t *testing.T) {
	uno := nuevaReplica(t, opcionesReplica{})
	h, bal := armarGateway(t, opcionesGateway{upstream: []string{uno.url()}})
	bal.MarcarCaida(context.Background(), bal.Instancias()[0])

	antes := uno.recibidas.Load()
	rec := pedir(h, rutaDePrueba, conAPIKeyValida())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; se esperaba 502", rec.Code)
	}
	if uno.recibidas.Load() != antes {
		t.Error("no tendría que haber llamado a una réplica marcada como caída")
	}
}

// --- Upstream lento -----------------------------------------------------------

func TestUpstreamLentoDevuelve504YNoMarcaLaReplica(t *testing.T) {
	lento := nuevaReplica(t, opcionesReplica{demora: 300 * time.Millisecond})
	h, bal := armarGateway(t, opcionesGateway{
		upstream: []string{lento.url()},
		timeout:  30 * time.Millisecond,
	})

	rec := pedir(h, rutaDePrueba, conAPIKeyValida())
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d; se esperaba 504. Cuerpo: %s", rec.Code, rec.Body.String())
	}
	p := problemaDe(t, rec)
	if p.Code != CodigoNoDisponible {
		t.Errorf("code = %q; se esperaba %q", p.Code, CodigoNoDisponible)
	}
	// Está lenta, no caída: sacarla del reparto por un timeout sería castigar a
	// la única réplica que hay.
	if !bal.Instancias()[0].Sana() {
		t.Error("un timeout no tiene que marcar la réplica como caída")
	}
	if n := lento.recibidas.Load(); n != 1 {
		t.Errorf("la réplica recibió %d requests; un timeout no se reintenta", n)
	}
}

// --- Ruta desconocida ---------------------------------------------------------

func TestRutaDesconocidaDevuelve404ProblemJSON(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	rec := pedir(h, "/nope", conAPIKeyValida())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; se esperaba 404", rec.Code)
	}
	p := problemaDe(t, rec)
	if p.Code != CodigoRutaNoEncontrada {
		t.Errorf("code = %q; se esperaba %q", p.Code, CodigoRutaNoEncontrada)
	}
	if p.Instance != "/nope" {
		t.Errorf("instance = %q; se esperaba /nope", p.Instance)
	}
	if upstream.recibidas.Load() != 0 {
		t.Error("una ruta que el gateway no expone no se reenvía")
	}
}

// --- Salud propia -------------------------------------------------------------

func TestHealthzRespondeSiempreYReadyzSigueALasReplicas(t *testing.T) {
	upstream := nuevaReplica(t, opcionesReplica{})
	h, bal := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	// Con la réplica sana los dos responden 200, y sin API key: son chequeos de
	// infraestructura, no la capacidad.
	if rec := pedir(h, rutaSalud, nil); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d; se esperaba 200", rec.Code)
	}
	if rec := pedir(h, rutaListo, nil); rec.Code != http.StatusOK {
		t.Errorf("readyz = %d con la réplica sana; se esperaba 200", rec.Code)
	}

	bal.MarcarCaida(context.Background(), bal.Instancias()[0])

	if rec := pedir(h, rutaSalud, nil); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d; el proceso del gateway sigue atendiendo, tiene que dar 200", rec.Code)
	}
	rec := pedir(h, rutaListo, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d sin réplicas; se esperaba 503", rec.Code)
	}
	if p := problemaDe(t, rec); p.Code != CodigoNoDisponible {
		t.Errorf("code = %q; se esperaba %q", p.Code, CodigoNoDisponible)
	}
}

// --- Correlación y traza ------------------------------------------------------

// El span padre es el del gateway y lleva el request_id; el contexto con el que
// se loguea tiene ese mismo span, que es de donde el handler de
// pkg/observabilidad saca el trace_id de cada línea.
func TestElSpanPadreYLosLogsQuedanCorrelacionados(t *testing.T) {
	grabador := conTrazasDePrueba(t)
	espia := nuevoEspia()
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}, espia: espia})

	rec := pedir(h, rutaDePrueba, conAPIKeyValida())
	id := rec.Header().Get(headerRequestID)

	var padre sdktrace.ReadOnlySpan
	for _, span := range grabador.Ended() {
		if span.SpanKind() == trace.SpanKindServer {
			padre = span
		}
	}
	if padre == nil {
		t.Fatal("el gateway no abrió el span padre del flujo")
	}
	if got, ok := atributo(padre, "request_id"); !ok || got.AsString() != id {
		t.Errorf("request_id del span = %v; se esperaba el devuelto en el header (%s)", got.AsString(), id)
	}
	if padre.Name() != http.MethodGet+" "+rutaCapacidad {
		t.Errorf("nombre del span = %q", padre.Name())
	}

	if len(espia.spans) == 0 {
		t.Fatal("no se logueó ninguna línea")
	}
	for _, sc := range espia.spans {
		if !sc.IsValid() {
			t.Fatal("se logueó sin span en el contexto: la línea saldría sin trace_id")
		}
		if sc.TraceID() != padre.SpanContext().TraceID() {
			t.Error("la línea de log quedó en otra traza que el span del gateway")
		}
	}
}

// Un traceparent entrante tiene que continuar la traza, no empezar una nueva:
// es lo que permite seguir un pedido desde quien nos consume.
func TestTraceparentEntranteContinuaLaTraza(t *testing.T) {
	grabador := conTrazasDePrueba(t)
	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	const traceIDEntrante = "4bf92f3577b34da6a3ce929d0e0e4736"
	pedir(h, rutaDePrueba, map[string]string{
		headerAPIKey:  apiKeyValida,
		"traceparent": "00-" + traceIDEntrante + "-00f067aa0ba902b7-01",
	})

	if len(grabador.Ended()) == 0 {
		t.Fatal("no se grabó ningún span")
	}
	for _, span := range grabador.Ended() {
		if got := span.SpanContext().TraceID().String(); got != traceIDEntrante {
			t.Errorf("trace id = %s; se esperaba el entrante %s", got, traceIDEntrante)
		}
	}
}

// Sin colector OTLP el proveedor global es no-op: el gateway tiene que seguir
// respondiendo 200, que es la fila "Jaeger caído" de la matriz.
func TestSinTrazasElGatewayRespondeIgual(t *testing.T) {
	previo := otel.GetTracerProvider()
	otel.SetTracerProvider(noop.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(previo) })

	upstream := nuevaReplica(t, opcionesReplica{})
	h, _ := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	rec := pedir(h, rutaDePrueba, conAPIKeyValida())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d sin trazas; se esperaba 200", rec.Code)
	}
	if rec.Header().Get(headerRequestID) == "" {
		t.Error("el X-Request-ID no depende de las trazas")
	}
}

// --- Errores del propio servicio ----------------------------------------------

// Los errores del contrato son del servicio: el gateway los pasa tal cual, sin
// reinterpretarlos ni marcar la réplica como caída.
func TestLosErroresDelServicioPasanTalCual(t *testing.T) {
	cuerpo := `{"type":"x","title":"Producto no encontrado","status":404,` +
		`"detail":"no existe un producto con SKU 'NO-EXISTE'","code":"PRODUCTO_NO_ENCONTRADO"}`
	upstream := nuevaReplica(t, opcionesReplica{status: http.StatusNotFound, cuerpo: cuerpo})
	h, bal := armarGateway(t, opcionesGateway{upstream: []string{upstream.url()}})

	rec := pedir(h, rutaCapacidad+"?producto=NO-EXISTE", conAPIKeyValida())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d; se esperaba el 404 del servicio", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != cuerpo {
		t.Errorf("el gateway tocó el error del servicio:\n got: %s\nwant: %s", got, cuerpo)
	}
	if !bal.Instancias()[0].Sana() {
		t.Error("un 404 del contrato no es una réplica caída")
	}
}
