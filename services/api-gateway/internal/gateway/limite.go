package gateway

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// limitador es un token bucket por clave (ADR-010). Cada clave tiene un balde
// de `rafaga` fichas que se rellena a `porSegundo` fichas por segundo; cada
// request se lleva una. Sin fichas, la request se rechaza enseguida con 429 en
// lugar de sumarse a la cola: es lo que evita el colapso que se vio en el test
// de carga de ADR-013, donde el gateway pasaba a devolver 504.
//
// El estado vive en la memoria del proceso. Con una sola instancia del gateway
// alcanza; con varias, cada una tendría su propio balde (ver ADR-010).
type limitador struct {
	porSegundo float64
	rafaga     float64
	ahora      func() time.Time

	mu         sync.Mutex
	baldes     map[string]*balde
	ultimaPoda time.Time
}

type balde struct {
	fichas float64
	ultimo time.Time
}

// inactividadParaPodar: un balde que no se usó en este tiempo ya está lleno y
// se puede olvidar. Evita que el mapa crezca sin límite con claves de un uso.
const inactividadParaPodar = 10 * time.Minute

func nuevoLimitador(porSegundo float64, rafaga int) *limitador {
	if porSegundo <= 0 {
		return nil
	}
	if rafaga < 1 {
		rafaga = 1
	}
	return &limitador{
		porSegundo: porSegundo,
		rafaga:     float64(rafaga),
		ahora:      time.Now,
		baldes:     map[string]*balde{},
	}
}

// permitir se lleva una ficha del balde de la clave. Si no hay, devuelve
// cuánto falta para que haya una (lo que va en Retry-After).
func (l *limitador) permitir(clave string) (bool, time.Duration) {
	ahora := l.ahora()
	l.mu.Lock()
	defer l.mu.Unlock()

	if ahora.Sub(l.ultimaPoda) > inactividadParaPodar {
		for k, b := range l.baldes {
			if ahora.Sub(b.ultimo) > inactividadParaPodar {
				delete(l.baldes, k)
			}
		}
		l.ultimaPoda = ahora
	}

	b, ok := l.baldes[clave]
	if !ok {
		b = &balde{fichas: l.rafaga, ultimo: ahora}
		l.baldes[clave] = b
	}
	// Relleno perezoso: se calcula al consultar, sin un goroutine por balde.
	b.fichas = math.Min(l.rafaga, b.fichas+ahora.Sub(b.ultimo).Seconds()*l.porSegundo)
	b.ultimo = ahora

	if b.fichas >= 1 {
		b.fichas--
		return true, 0
	}
	falta := (1 - b.fichas) / l.porSegundo
	return false, time.Duration(falta * float64(time.Second))
}

// limitar aplica el límite por identidad (API key o usuario). Va después de
// autenticar: una request sin credenciales ya se cortó con 401 y no gasta el
// balde de nadie.
func (l *limitador) limitar(m *metricas, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clave := "anonimo"
		if id, ok := identidadDe(r.Context()); ok {
			clave = id.clave()
		}
		l.atender(w, r, m, clave, next)
	})
}

// limitarPorIP es para el login, que todavía no tiene identidad. Frena a quien
// intente adivinar contraseñas.
func (l *limitador) limitarPorIP(m *metricas, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.atender(w, r, m, "ip:"+ipDe(r), next)
	})
}

func (l *limitador) atender(w http.ResponseWriter, r *http.Request, m *metricas, clave string, next http.Handler) {
	ok, espera := l.permitir(clave)
	if ok {
		next.ServeHTTP(w, r)
		return
	}
	// Retry-After va en segundos enteros (RFC 9110): se redondea para arriba
	// para que el cliente que lo respete no vuelva a chocar.
	segundos := int(math.Ceil(espera.Seconds()))
	if segundos < 1 {
		segundos = 1
	}
	m.rechazoPorLimite()
	trace.SpanFromContext(r.Context()).SetAttributes(attribute.Bool("limite_excedido", true))
	LoggerDe(r.Context()).WarnContext(r.Context(), "límite de tráfico excedido",
		"clave", clave, "retry_after_s", segundos)
	w.Header().Set("Retry-After", strconv.Itoa(segundos))
	escribirProblema(w, r, http.StatusTooManyRequests, CodigoLimiteExcedido,
		"se superó el límite de requests por segundo; reintentar después de Retry-After")
}
