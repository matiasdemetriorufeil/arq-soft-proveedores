package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests de lo que suma la Entrega 3 al gateway: login con JWT, límite de
// tráfico, ruteo a stock-service, métricas y frontend.

const (
	usuarioDemo   = "santino"
	contrasenaOK  = "contrasena-de-prueba"
	secretoPrueba = "secreto-de-prueba-de-32-caracteres!!"
	claveInterna  = "clave-interna-de-prueba"
)

type opcionesE3 struct {
	stock            *httptest.Server
	limitePorSegundo float64
	limiteRafaga     int
	loginPorSegundo  float64
	loginRafaga      int
	sinLogin         bool
	metricas         *metricas
}

func armarGatewayE3(t *testing.T, upstream *replica, o opcionesE3) http.Handler {
	t.Helper()
	bal, err := NuevoBalanceador(ConfigBalanceador{
		Upstreams: []string{upstream.url()},
		Intervalo: time.Hour,
		Timeout:   500 * time.Millisecond,
		Logger:    loggerSilencioso(),
	})
	if err != nil {
		t.Fatal(err)
	}
	bal.Sondear(t.Context())
	cfg := Config{
		Balanceador:      bal,
		APIKeys:          []string{apiKeyValida, claveInterna},
		TimeoutUpstream:  time.Second,
		Logger:           loggerSilencioso(),
		ClaveInterna:     claveInterna,
		LimitePorSegundo: o.limitePorSegundo,
		LimiteRafaga:     o.limiteRafaga,
		LoginPorSegundo:  o.loginPorSegundo,
		LoginRafaga:      o.loginRafaga,
		Metricas:         o.metricas,
	}
	if !o.sinLogin {
		cfg.Usuarios = map[string]string{usuarioDemo: contrasenaOK}
		cfg.SecretoJWT = []byte(secretoPrueba)
	}
	if o.stock != nil {
		u, _ := url.Parse(o.stock.URL)
		cfg.StockURL = u
	}
	return NuevoHandler(cfg)
}

func hacer(h http.Handler, metodo, ruta, cuerpo string, headers map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if cuerpo != "" {
		body = strings.NewReader(cuerpo)
	}
	req := httptest.NewRequest(metodo, ruta, body)
	req.RemoteAddr = "10.0.0.1:5555"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func loguear(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := hacer(h, http.MethodPost, rutaLogin,
		`{"usuario":"`+usuarioDemo+`","contrasena":"`+contrasenaOK+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status = %d; cuerpo %s", rec.Code, rec.Body.String())
	}
	var r respuestaLogin
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.TokenType != "Bearer" || r.AccessToken == "" || r.ExpiresIn <= 0 || r.Usuario != usuarioDemo {
		t.Fatalf("respuesta de login inesperada: %+v", r)
	}
	return r.AccessToken
}

func conBearer(token string) map[string]string {
	return map[string]string{headerAutorizacion: "Bearer " + token}
}

// --- JWT ----------------------------------------------------------------------

func TestJWTEmitidoSeVerifica(t *testing.T) {
	f := &firmadorJWT{secreto: []byte(secretoPrueba), duracion: time.Hour, ahora: time.Now}
	tok, _, err := f.emitir("ana")
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.verificar(tok)
	if err != nil {
		t.Fatalf("verificar: %v", err)
	}
	if c.Sub != "ana" || c.Iss != emisorJWT {
		t.Errorf("claims = %+v", c)
	}
}

func TestJWTRechazos(t *testing.T) {
	ahora := time.Now()
	f := &firmadorJWT{secreto: []byte(secretoPrueba), duracion: time.Hour, ahora: func() time.Time { return ahora }}
	tok, _, _ := f.emitir("ana")
	partes := strings.Split(tok, ".")

	otroSecreto := &firmadorJWT{secreto: []byte("otro-secreto-otro-secreto-otro-secreto"), duracion: time.Hour, ahora: time.Now}
	tokAjeno, _, _ := otroSecreto.emitir("ana")

	payloadAdmin := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin","iss":"` + emisorJWT + `","exp":9999999999}`))
	algNone := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))

	casos := map[string]string{
		"vacío":               "",
		"dos partes":          partes[0] + "." + partes[1],
		"payload cambiado":    partes[0] + "." + payloadAdmin + "." + partes[2],
		"alg none":            algNone + "." + payloadAdmin + ".",
		"firmado con otro":    tokAjeno,
		"firma de otro token": partes[0] + "." + partes[1] + "." + strings.Split(tokAjeno, ".")[2],
	}
	for nombre, tok := range casos {
		t.Run(nombre, func(t *testing.T) {
			if _, err := f.verificar(tok); err == nil {
				t.Fatal("se esperaba un error")
			}
		})
	}

	t.Run("vencido", func(t *testing.T) {
		f.ahora = func() time.Time { return ahora.Add(time.Hour + time.Second) }
		if _, err := f.verificar(tok); err != errTokenVencido {
			t.Fatalf("err = %v; se esperaba errTokenVencido", err)
		}
	})
}

// --- Login --------------------------------------------------------------------

func TestLoginCorrectoYConElTokenSeLlegaALaCapacidad(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{})

	tok := loguear(t, h)
	rec := hacer(h, http.MethodGet, rutaDePrueba, "", conBearer(tok))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; %s", rec.Code, rec.Body.String())
	}
	got := up.ultima.Load().headers
	if got.Get(headerAPIKey) != claveInterna {
		t.Errorf("X-API-Key hacia el servicio = %q; se esperaba la clave interna", got.Get(headerAPIKey))
	}
	if got.Get(headerAutorizacion) != "" {
		t.Error("el token no tiene que viajar a los servicios")
	}
}

func TestLoginRechazado(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{})

	casos := map[string]struct {
		cuerpo string
		status int
		code   string
	}{
		"contraseña incorrecta": {`{"usuario":"santino","contrasena":"no"}`, 401, CodigoCredencialesInvalidas},
		"usuario inexistente":   {`{"usuario":"nadie","contrasena":"` + contrasenaOK + `"}`, 401, CodigoCredencialesInvalidas},
		"sin contraseña":        {`{"usuario":"santino"}`, 400, CodigoParametroInvalido},
		"JSON roto":             {`{"usuario":`, 400, CodigoParametroInvalido},
	}
	for nombre, c := range casos {
		t.Run(nombre, func(t *testing.T) {
			rec := hacer(h, http.MethodPost, rutaLogin, c.cuerpo, nil)
			if rec.Code != c.status {
				t.Fatalf("status = %d; se esperaba %d", rec.Code, c.status)
			}
			if p := problemaDe(t, rec); p.Code != c.code {
				t.Errorf("code = %q; se esperaba %q", p.Code, c.code)
			}
		})
	}
}

func TestTokenInvalidoDevuelve401YNoLlegaAlServicio(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{})

	rec := hacer(h, http.MethodGet, rutaDePrueba, "", conBearer("no.es.untoken"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; se esperaba 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("un 401 por token tiene que traer WWW-Authenticate")
	}
	if up.recibidas.Load() != 0 {
		t.Error("el servicio no tiene que recibir la request")
	}
}

func TestAPIKeyInvalidaNoSeSalvaConUnTokenValido(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{})
	tok := loguear(t, h)

	hd := conBearer(tok)
	hd[headerAPIKey] = "clave-que-no-es"
	if rec := hacer(h, http.MethodGet, rutaDePrueba, "", hd); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; se esperaba 401", rec.Code)
	}
}

func TestSinUsuariosNoHayLoginNiSeAceptanTokens(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{sinLogin: true})

	rec := hacer(h, http.MethodPost, rutaLogin, `{"usuario":"a","contrasena":"b"}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login sin configurar: status = %d; se esperaba 404", rec.Code)
	}
	// La API key sigue funcionando igual que en la Entrega 2.
	if rec := hacer(h, http.MethodGet, rutaDePrueba, "", conAPIKeyValida()); rec.Code != http.StatusOK {
		t.Fatalf("con API key: status = %d", rec.Code)
	}
}

// --- Límite de tráfico ----------------------------------------------------------

func TestLimitadorRellenaConElTiempo(t *testing.T) {
	ahora := time.Unix(1_000_000, 0)
	l := nuevoLimitador(2, 3) // 2 fichas por segundo, ráfaga de 3
	l.ahora = func() time.Time { return ahora }

	for i := range 3 {
		if ok, _ := l.permitir("a"); !ok {
			t.Fatalf("la request %d de la ráfaga tenía que pasar", i+1)
		}
	}
	ok, espera := l.permitir("a")
	if ok {
		t.Fatal("la cuarta tenía que rechazarse")
	}
	if espera <= 0 || espera > 500*time.Millisecond {
		t.Errorf("espera = %v; con 2 fichas/s se esperaba hasta 500ms", espera)
	}
	if ok, _ := l.permitir("b"); !ok {
		t.Error("otra clave tiene su propio balde")
	}

	ahora = ahora.Add(500 * time.Millisecond)
	if ok, _ := l.permitir("a"); !ok {
		t.Error("después de 500ms tenía que haber una ficha")
	}
	if ok, _ := l.permitir("a"); ok {
		t.Error("y solo una")
	}

	ahora = ahora.Add(time.Hour)
	for i := range 3 {
		if ok, _ := l.permitir("a"); !ok {
			t.Fatalf("el balde no supera la ráfaga, pero se llena hasta ella: falló la %d", i+1)
		}
	}
}

func TestLimiteExcedidoDevuelve429ConRetryAfter(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	m := NuevasMetricas(nil)
	h := armarGatewayE3(t, up, opcionesE3{limitePorSegundo: 0.5, limiteRafaga: 2, metricas: m})

	for i := range 2 {
		if rec := hacer(h, http.MethodGet, rutaDePrueba, "", conAPIKeyValida()); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i+1, rec.Code)
		}
	}
	rec := hacer(h, http.MethodGet, rutaDePrueba, "", conAPIKeyValida())
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d; se esperaba 429", rec.Code)
	}
	if p := problemaDe(t, rec); p.Code != CodigoLimiteExcedido {
		t.Errorf("code = %q", p.Code)
	}
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra < 1 || ra > 2 {
		t.Errorf("Retry-After = %q; con 0,5 fichas/s se esperaba 1 o 2", rec.Header().Get("Retry-After"))
	}
	if n := up.recibidas.Load(); n != 2 {
		t.Errorf("el servicio recibió %d; lo rechazado no tiene que llegarle", n)
	}

	// Un usuario logueado tiene su propio balde, aparte del de la API key.
	tok := loguear(t, h)
	if rec := hacer(h, http.MethodGet, rutaDePrueba, "", conBearer(tok)); rec.Code != http.StatusOK {
		t.Errorf("otro balde: status = %d", rec.Code)
	}

	if !strings.Contains(textoMetricas(t, m), "gateway_limite_rechazos_total 1") {
		t.Error("el rechazo tiene que contarse en gateway_limite_rechazos_total")
	}
}

func TestSinCredencialesNoGastaElBalde(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{limitePorSegundo: 0.001, limiteRafaga: 1})

	for range 5 {
		if rec := hacer(h, http.MethodGet, rutaDePrueba, "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d; se esperaba 401", rec.Code)
		}
	}
	if rec := hacer(h, http.MethodGet, rutaDePrueba, "", conAPIKeyValida()); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; los 401 no tenían que gastar el balde de la clave", rec.Code)
	}
}

func TestLoginLimitadoPorIP(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{loginPorSegundo: 0.001, loginRafaga: 3})

	for i := range 3 {
		if rec := hacer(h, http.MethodPost, rutaLogin, `{"usuario":"santino","contrasena":"x"}`, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("intento %d: status = %d", i+1, rec.Code)
		}
	}
	rec := hacer(h, http.MethodPost, rutaLogin, `{"usuario":"santino","contrasena":"`+contrasenaOK+`"}`, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d; después de 3 intentos se esperaba 429 aunque la contraseña sea buena", rec.Code)
	}
}

// --- stock-service ------------------------------------------------------------

type llamadaStock struct {
	metodo, path, cuerpo string
	headers              http.Header
}

func nuevoStockFalso(t *testing.T) (*httptest.Server, *atomic.Pointer[llamadaStock]) {
	t.Helper()
	var ultima atomic.Pointer[llamadaStock]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ultima.Store(&llamadaStock{r.Method, r.URL.Path, string(b), r.Header.Clone()})
		w.Header().Set("Content-Type", contentTypeJSON)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"r1","sku":"PAN-450W-MONO","cantidad":2,"estado":"ACTIVA"}`)
			return
		}
		_, _ = io.WriteString(w, `{"sku":"PAN-450W-MONO","disponible":40,"reservado":0,"minimo":10}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &ultima
}

func TestStockSeRuteaConCuerpoYHeaders(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	stock, ultima := nuevoStockFalso(t)
	h := armarGatewayE3(t, up, opcionesE3{stock: stock})
	tok := loguear(t, h)

	rec := hacer(h, http.MethodGet, "/api/v1/stock/PAN-450W-MONO", "", conBearer(tok))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"disponible":40`) {
		t.Fatalf("GET stock: %d %s", rec.Code, rec.Body.String())
	}

	hd := conBearer(tok)
	hd["Idempotency-Key"] = "clave-1"
	rec = hacer(h, http.MethodPost, "/api/v1/stock/reservas", `{"sku":"PAN-450W-MONO","cantidad":2}`, hd)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST reserva: %d %s", rec.Code, rec.Body.String())
	}
	got := ultima.Load()
	if got.metodo != http.MethodPost || got.path != "/api/v1/stock/reservas" || got.cuerpo != `{"sku":"PAN-450W-MONO","cantidad":2}` {
		t.Errorf("stock-service recibió %+v", got)
	}
	if got.headers.Get("Idempotency-Key") != "clave-1" {
		t.Error("la Idempotency-Key tiene que llegar a stock-service")
	}
	if got.headers.Get(headerAPIKey) != claveInterna || got.headers.Get(headerAutorizacion) != "" {
		t.Error("hacia stock-service va la clave interna y no el token")
	}
	if got.headers.Get(headerRequestID) != rec.Header().Get(headerRequestID) {
		t.Error("el X-Request-ID tiene que ser el mismo en el gateway y en stock-service")
	}
	if len(rec.Header().Values(headerRequestID)) != 1 {
		t.Error("el X-Request-ID no se tiene que duplicar en la respuesta")
	}
}

func TestStockExigeAutenticacionYNoPublicaElWebhook(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	stock, ultima := nuevoStockFalso(t)
	h := armarGatewayE3(t, up, opcionesE3{stock: stock})

	if rec := hacer(h, http.MethodGet, "/api/v1/stock/PAN-450W-MONO", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin credenciales: status = %d; se esperaba 401", rec.Code)
	}
	if rec := hacer(h, http.MethodPost, "/webhooks/fav", `{}`, conAPIKeyValida()); rec.Code != http.StatusNotFound {
		t.Errorf("webhook: status = %d; FAV lo llama directo, el gateway no lo publica", rec.Code)
	}
	if ultima.Load() != nil {
		t.Error("stock-service no tenía que recibir nada")
	}
}

func TestStockCaidoDevuelve502(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	stock, _ := nuevoStockFalso(t)
	stock.Close()
	h := armarGatewayE3(t, up, opcionesE3{stock: stock})

	rec := hacer(h, http.MethodGet, "/api/v1/stock/PAN-450W-MONO", "", conAPIKeyValida())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d; se esperaba 502", rec.Code)
	}
	if p := problemaDe(t, rec); p.Code != CodigoNoDisponible {
		t.Errorf("code = %q", p.Code)
	}
}

// --- Métricas y frontend --------------------------------------------------------

func textoMetricas(t *testing.T, m *metricas) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestMetricasUsanElPatronYNoElPath(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	stock, _ := nuevoStockFalso(t)
	m := NuevasMetricas(func() int { return 2 })
	h := armarGatewayE3(t, up, opcionesE3{stock: stock, metricas: m})

	hacer(h, http.MethodGet, rutaDePrueba, "", conAPIKeyValida())
	hacer(h, http.MethodGet, "/api/v1/stock/PAN-450W-MONO", "", conAPIKeyValida())
	hacer(h, http.MethodGet, "/api/v1/stock/INV-3KW-ONG", "", conAPIKeyValida())
	hacer(h, http.MethodGet, rutaDePrueba, "", nil)

	txt := textoMetricas(t, m)
	for _, esperado := range []string{
		`gateway_http_requests_total{metodo="GET",ruta="GET /api/v1/proveedores/recomendados",status="200"} 1`,
		`gateway_http_requests_total{metodo="GET",ruta="GET /api/v1/proveedores/recomendados",status="401"} 1`,
		`gateway_http_requests_total{metodo="GET",ruta="/api/v1/stock/",status="200"} 2`,
		`gateway_http_request_duration_seconds_bucket{ruta="GET /api/v1/proveedores/recomendados",le="0.3"}`,
		`gateway_replicas_sanas 2`,
	} {
		if !strings.Contains(txt, esperado) {
			t.Errorf("falta en /metrics: %s", esperado)
		}
	}
	if strings.Contains(txt, "INV-3KW-ONG") {
		t.Error("un SKU no puede ser una etiqueta: cada uno sería una serie nueva")
	}
}

func TestFrontendSeSirveDesdeElGateway(t *testing.T) {
	up := nuevaReplica(t, opcionesReplica{})
	h := armarGatewayE3(t, up, opcionesE3{})

	rec := hacer(h, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/web/app.js") {
		t.Fatalf("GET /: %d", rec.Code)
	}
	for _, ruta := range []string{"/web/app.js", "/web/estilos.css"} {
		if rec := hacer(h, http.MethodGet, ruta, "", nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", ruta, rec.Code)
		}
	}
	if rec := hacer(h, http.MethodGet, "/web/no-existe.js", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("archivo inexistente: %d", rec.Code)
	}
}
