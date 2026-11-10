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
// entienda (falta el eventoId o el pedido).
var ErrAvisoInvalido = errors.New("aviso de FAV inválido")

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
	return a, nil
}
