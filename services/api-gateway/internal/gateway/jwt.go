package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// emisorJWT es el `iss` de los tokens que firma el gateway. Se verifica al
// leerlos: un token firmado con el mismo secreto por otro sistema no entra.
const emisorJWT = "arq-soft-proveedores/api-gateway"

// encabezadoJWT es fijo: el gateway solo firma y solo acepta HS256. Aceptar el
// `alg` que diga el token abre la puerta al clásico `alg: none`.
var encabezadoJWT = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

var (
	errTokenMalFormado = errors.New("el token no tiene la forma header.payload.firma")
	errTokenFirma      = errors.New("la firma del token no es válida")
	errTokenVencido    = errors.New("el token está vencido")
	errTokenEmisor     = errors.New("el token no lo emitió este gateway")
)

// reclamos son los claims del token. Solo lo justo para autenticar al usuario
// del frontend: quién es y hasta cuándo vale.
type reclamos struct {
	Sub string `json:"sub"`
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// firmadorJWT emite y verifica tokens HS256 con un secreto compartido. Está
// hecho con la biblioteca estándar a propósito (pocas dependencias): son
// tres segmentos en base64url y un HMAC-SHA256.
type firmadorJWT struct {
	secreto  []byte
	duracion time.Duration
	ahora    func() time.Time
}

func (f *firmadorJWT) emitir(usuario string) (token string, vence time.Time, err error) {
	ahora := f.ahora()
	vence = ahora.Add(f.duracion)
	payload, err := json.Marshal(reclamos{Sub: usuario, Iss: emisorJWT, Iat: ahora.Unix(), Exp: vence.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	sinFirma := encabezadoJWT + "." + base64.RawURLEncoding.EncodeToString(payload)
	return sinFirma + "." + f.firma(sinFirma), vence, nil
}

func (f *firmadorJWT) verificar(token string) (reclamos, error) {
	partes := strings.Split(token, ".")
	if len(partes) != 3 {
		return reclamos{}, errTokenMalFormado
	}
	// El header tiene que ser exactamente el nuestro: así no hay que
	// interpretar ningún `alg`.
	if partes[0] != encabezadoJWT {
		return reclamos{}, errTokenFirma
	}
	esperada := f.firma(partes[0] + "." + partes[1])
	if !hmac.Equal([]byte(esperada), []byte(partes[2])) {
		return reclamos{}, errTokenFirma
	}

	crudo, err := base64.RawURLEncoding.DecodeString(partes[1])
	if err != nil {
		return reclamos{}, errTokenMalFormado
	}
	var c reclamos
	if err := json.Unmarshal(crudo, &c); err != nil {
		return reclamos{}, errTokenMalFormado
	}
	if c.Iss != emisorJWT {
		return reclamos{}, errTokenEmisor
	}
	if c.Sub == "" {
		return reclamos{}, errTokenMalFormado
	}
	if f.ahora().Unix() >= c.Exp {
		return reclamos{}, errTokenVencido
	}
	return c, nil
}

func (f *firmadorJWT) firma(sinFirma string) string {
	mac := hmac.New(sha256.New, f.secreto)
	mac.Write([]byte(sinFirma))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
