package fav

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ToleranciaFirma es la antigüedad máxima de un aviso firmado (contrato,
// header X-FAV-Firma, propuesta de FAV). También se rechaza una firma con
// fecha más de 5 minutos en el futuro: un reloj así de corrido es sospechoso.
const ToleranciaFirma = 5 * time.Minute

// ErrFirmaInvalida: el aviso no viene de FAV, se alteró o es viejo. El
// receptor responde 401 y FAV no lo reintenta.
var ErrFirmaInvalida = errors.New("firma del aviso inválida")

// ErrAvisoInvalido: la firma verificó pero el cuerpo no es un aviso que se
// entienda (falta el eventoId o el pedido, o el eventoId no es un UUID).
var ErrAvisoInvalido = errors.New("aviso de FAV inválido")

// esUUID valida la forma canónica 8-4-4-4-12 en hexadecimal. El contrato de
// FAV declara `eventoId` como `format: uuid` y la columna donde lo guardamos
// es `uuid`, así que un valor con otra forma no se puede registrar. Se valida
// acá para responder 400 —un error del emisor, que no se arregla
// reintentando— en lugar de dejar que falle el INSERT y contestar 503, que le
// pide a FAV que reintente para siempre un aviso que nunca va a entrar.
// Se comprueba la forma en vez de usar una biblioteca porque alcanza: es más
// estricto que PostgreSQL, que además acepta llaves y la variante sin guiones.
func esUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			esHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !esHex {
				return false
			}
		}
	}
	return true
}

// Aviso es el cuerpo del webhook pedidoActualizado. Se ignoran los campos
// desconocidos, como pide el contrato.
type Aviso struct {
	EventoID   string    `json:"eventoId"`
	Tipo       string    `json:"tipo"`
	OcurridoEn time.Time `json:"ocurridoEn"`
	Pedido     struct {
		PedidoID          string `json:"pedidoId"`
		ReferenciaExterna string `json:"referenciaExterna"`
		Estado            string `json:"estado"`
	} `json:"pedido"`
}

// VerificarFirma comprueba el header `X-FAV-Firma: t=<unix>,v1=<hex>`: el
// HMAC-SHA256 de "<t>.<cuerpo crudo>" con el secreto de avisos, comparado en
// tiempo constante, y que `t` no tenga más de ToleranciaFirma.
func VerificarFirma(secreto, header string, cuerpo []byte, ahora time.Time) error {
	if secreto == "" {
		return fmt.Errorf("%w: no hay secreto de avisos configurado", ErrFirmaInvalida)
	}
	var tTexto, v1 string
	for _, parte := range strings.Split(header, ",") {
		clave, valor, ok := strings.Cut(strings.TrimSpace(parte), "=")
		if !ok {
			continue
		}
		switch clave {
		case "t":
			tTexto = valor
		case "v1":
			v1 = valor
		}
	}
	t, err := strconv.ParseInt(tTexto, 10, 64)
	if err != nil || v1 == "" {
		return fmt.Errorf("%w: el header no tiene el formato t=<unix>,v1=<hex>", ErrFirmaInvalida)
	}
	recibida, err := hex.DecodeString(v1)
	if err != nil {
		return fmt.Errorf("%w: v1 no es hexadecimal", ErrFirmaInvalida)
	}
	mac := hmac.New(sha256.New, []byte(secreto))
	mac.Write([]byte(tTexto + "."))
	mac.Write(cuerpo)
	if !hmac.Equal(mac.Sum(nil), recibida) {
		return fmt.Errorf("%w: no coincide con el secreto de avisos", ErrFirmaInvalida)
	}
	if d := ahora.Sub(time.Unix(t, 0)); d > ToleranciaFirma || d < -ToleranciaFirma {
		return fmt.Errorf("%w: firmado hace %s, el máximo es %s", ErrFirmaInvalida, d.Round(time.Second), ToleranciaFirma)
	}
	return nil
}

// LeerAviso interpreta el cuerpo ya verificado.
func LeerAviso(cuerpo []byte) (Aviso, error) {
	var a Aviso
	if err := json.Unmarshal(cuerpo, &a); err != nil {
		return Aviso{}, fmt.Errorf("%w: %v", ErrAvisoInvalido, err)
	}
	if a.EventoID == "" || a.Pedido.PedidoID == "" || a.Pedido.Estado == "" {
		return Aviso{}, fmt.Errorf("%w: faltan eventoId, pedido.pedidoId o pedido.estado", ErrAvisoInvalido)
	}
	if !esUUID(a.EventoID) {
		return Aviso{}, fmt.Errorf("%w: eventoId tiene que ser un UUID", ErrAvisoInvalido)
	}
	return a, nil
}
