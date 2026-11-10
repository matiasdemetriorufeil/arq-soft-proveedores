package gateway

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	rutaLogin           = "/api/v1/auth/login"
	headerAutorizacion  = "Authorization"
	prefijoBearer       = "Bearer "
	maxCuerpoLogin      = 4 << 10
	duracionJWTDefecto  = time.Hour
	contrasenaDeRelleno = "relleno-para-tiempo-constante"
)

// identidad es quién hace la request, ya autenticado. La clave del límite de
// tráfico sale de acá: cada API key y cada usuario tienen su propio balde.
type identidad struct {
	// tipo es "api_key" (otro sistema, como el grupo de paneles solares) o
	// "usuario" (una persona que entró por el frontend).
	tipo string
	// id nunca es la API key en claro: va a logs, a métricas y al límite de
	// tráfico. Para las claves es un prefijo de su SHA-256.
	id string
}

func (i identidad) clave() string { return i.tipo + ":" + i.id }

func identidadDe(ctx context.Context) (identidad, bool) {
	i, ok := ctx.Value(ctxIdentidad).(identidad)
	return i, ok
}

// autenticador resuelve las dos formas de entrar al gateway:
//
//   - X-API-Key: la de siempre, para los sistemas que consumen la capacidad
//     (el contrato con el grupo de paneles solares no cambia).
//   - Authorization: Bearer <JWT>: para el frontend, después del login.
//
// Los servicios de atrás siguen exigiendo X-API-Key. Cuando la request entra
// con JWT, el gateway saca el Authorization y pone la clave interna: el
// usuario del frontend nunca ve una API key.
type autenticador struct {
	claves       [][]byte
	usuarios     map[string][]byte
	jwt          *firmadorJWT
	claveInterna string
}

func nuevoAutenticador(cfg Config) *autenticador {
	a := &autenticador{usuarios: map[string][]byte{}, claveInterna: cfg.ClaveInterna}
	for _, k := range cfg.APIKeys {
		a.claves = append(a.claves, []byte(k))
	}
	if a.claveInterna == "" && len(cfg.APIKeys) > 0 {
		a.claveInterna = cfg.APIKeys[0]
	}
	for u, c := range cfg.Usuarios {
		a.usuarios[u] = []byte(c)
	}
	if len(cfg.SecretoJWT) > 0 {
		dur := cfg.DuracionJWT
		if dur <= 0 {
			dur = duracionJWTDefecto
		}
		a.jwt = &firmadorJWT{secreto: cfg.SecretoJWT, duracion: dur, ahora: time.Now}
	}
	return a
}

// loginHabilitado: sin secreto o sin usuarios el login no tiene sentido, y la
// ruta responde 404 como cualquier otra que el gateway no expone.
func (a *autenticador) loginHabilitado() bool { return a.jwt != nil && len(a.usuarios) > 0 }

func (a *autenticador) claveValida(recibida string) bool {
	if recibida == "" {
		return false
	}
	ok := false
	for _, k := range a.claves {
		// Sin cortar al encontrarla: el tiempo no depende de cuál acertó.
		if subtle.ConstantTimeCompare([]byte(recibida), k) == 1 {
			ok = true
		}
	}
	return ok
}

// exigir protege una ruta: si la request no trae una API key válida o un JWT
// vigente, responde 401 y no llega al upstream.
func (a *autenticador) exigir(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.resolver(w, r)
		if !ok {
			return
		}
		ctx := context.WithValue(r.Context(), ctxIdentidad, id)
		trace.SpanFromContext(ctx).SetAttributes(
			attribute.String("auth.tipo", id.tipo),
			attribute.String("auth.id", id.id),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *autenticador) resolver(w http.ResponseWriter, r *http.Request) (identidad, bool) {
	// Una API key presente manda, y si es inválida es 401 aunque venga
	// también un token: no se adivina cuál de las dos quiso usar el cliente.
	if clave := r.Header.Get(headerAPIKey); clave != "" {
		if !a.claveValida(clave) {
			escribirProblema(w, r, http.StatusUnauthorized, CodigoNoAutorizado,
				"falta el header X-API-Key o no es válido")
			return identidad{}, false
		}
		suma := sha256.Sum256([]byte(clave))
		return identidad{tipo: "api_key", id: hex.EncodeToString(suma[:4])}, true
	}

	auth := r.Header.Get(headerAutorizacion)
	if a.jwt != nil && strings.HasPrefix(auth, prefijoBearer) {
		c, err := a.jwt.verificar(strings.TrimSpace(strings.TrimPrefix(auth, prefijoBearer)))
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			escribirProblema(w, r, http.StatusUnauthorized, CodigoNoAutorizado,
				"el token no es válido o venció; volver a iniciar sesión")
			return identidad{}, false
		}
		// Hacia adentro solo viaja la clave interna: el token se queda en el
		// gateway y los servicios no tienen que saber de JWT.
		r.Header.Del(headerAutorizacion)
		r.Header.Set(headerAPIKey, a.claveInterna)
		return identidad{tipo: "usuario", id: c.Sub}, true
	}

	escribirProblema(w, r, http.StatusUnauthorized, CodigoNoAutorizado,
		"falta el header X-API-Key o no es válido")
	return identidad{}, false
}

type cuerpoLogin struct {
	Usuario    string `json:"usuario"`
	Contrasena string `json:"contrasena"`
}

type respuestaLogin struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Usuario     string `json:"usuario"`
}

// login cambia usuario y contraseña por un JWT. Las credenciales incorrectas
// y el usuario inexistente dan el mismo 401 y tardan lo mismo: no se puede
// averiguar qué usuarios existen.
func (a *autenticador) login(w http.ResponseWriter, r *http.Request) {
	var c cuerpoLogin
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCuerpoLogin)).Decode(&c); err != nil ||
		c.Usuario == "" || c.Contrasena == "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido,
			"el cuerpo tiene que ser un JSON con 'usuario' y 'contrasena'")
		return
	}

	guardada, existe := a.usuarios[c.Usuario]
	if !existe {
		guardada = []byte(contrasenaDeRelleno)
	}
	// Se comparan los hashes para que la comparación sea de largo fijo:
	// ConstantTimeCompare corta enseguida si los largos difieren.
	h1 := sha256.Sum256([]byte(c.Contrasena))
	h2 := sha256.Sum256(guardada)
	if subtle.ConstantTimeCompare(h1[:], h2[:]) != 1 || !existe {
		LoggerDe(r.Context()).WarnContext(r.Context(), "login rechazado", "usuario", c.Usuario)
		escribirProblema(w, r, http.StatusUnauthorized, CodigoCredencialesInvalidas,
			"usuario o contraseña incorrectos")
		return
	}

	token, vence, err := a.jwt.emitir(c.Usuario)
	if err != nil {
		escribirProblema(w, r, http.StatusInternalServerError, CodigoErrorInterno,
			"no se pudo emitir el token")
		return
	}
	LoggerDe(r.Context()).InfoContext(r.Context(), "login correcto", "usuario", c.Usuario)
	escribirJSON(w, http.StatusOK, respuestaLogin{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(time.Until(vence).Seconds()),
		Usuario:     c.Usuario,
	})
}

// ipDe es la clave del límite de tráfico del login, que todavía no tiene
// identidad. El gateway es el borde: RemoteAddr es el cliente real, así que
// no se mira X-Forwarded-For (lo puede inventar cualquiera).
func ipDe(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
