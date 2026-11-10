package fav_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/stock-service/internal/fav"
)

// El vector de prueba del contrato de FAV: el ejemplo EventoPagado en una
// línea, sin espacios, firmado con el secreto del mock.
const (
	cuerpoVector = `{"eventoId":"3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b","tipo":"pedido.actualizado","ocurridoEn":"2026-10-17T13:20:00Z","pedido":{"pedidoId":"7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f","referenciaExterna":"OC-2026-0457","estado":"PAGADO"}}`
	firmaVector  = "t=1792243201,v1=2fbec75293b36b2159e73e8c8c1a36062411d20762993a8755dd47a211182ab4"
	secretoMock  = "secreto-de-prueba"
)

var momentoVector = time.Unix(1792243201, 0)

func firmar(secreto string, t int64, cuerpo string) string {
	mac := hmac.New(sha256.New, []byte(secreto))
	fmt.Fprintf(mac, "%d.%s", t, cuerpo)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

func TestVerificarFirmaConElVectorDelContrato(t *testing.T) {
	if err := fav.VerificarFirma(secretoMock, firmaVector, []byte(cuerpoVector), momentoVector.Add(30*time.Second)); err != nil {
		t.Fatalf("el vector de prueba del contrato no verifica: %v", err)
	}
	a, err := fav.LeerAviso([]byte(cuerpoVector))
	if err != nil {
		t.Fatal(err)
	}
	if a.EventoID != "3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b" || a.Pedido.Estado != "PAGADO" || a.Pedido.ReferenciaExterna != "OC-2026-0457" {
		t.Errorf("aviso = %+v", a)
	}
}

func TestVerificarFirmaRechaza(t *testing.T) {
	casos := []struct {
		nombre, secreto, header, cuerpo string
		ahora                           time.Time
	}{
		{"otro secreto", "otro", firmaVector, cuerpoVector, momentoVector},
		{"cuerpo alterado", secretoMock, firmaVector, cuerpoVector[:len(cuerpoVector)-3] + `X"}}`, momentoVector},
		{"cuerpo re-serializado con espacios", secretoMock, firmaVector, `{"eventoId": "3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b"}`, momentoVector},
		{"con más de 5 minutos", secretoMock, firmaVector, cuerpoVector, momentoVector.Add(5*time.Minute + time.Second)},
		{"firmado en el futuro", secretoMock, firmaVector, cuerpoVector, momentoVector.Add(-6 * time.Minute)},
		{"sin header", secretoMock, "", cuerpoVector, momentoVector},
		{"formato roto", secretoMock, "v1=abc", cuerpoVector, momentoVector},
		{"v1 no es hex", secretoMock, "t=1792243201,v1=zz", cuerpoVector, momentoVector},
		{"sin secreto configurado", "", firmaVector, cuerpoVector, momentoVector},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			err := fav.VerificarFirma(c.secreto, c.header, []byte(c.cuerpo), c.ahora)
			if !errors.Is(err, fav.ErrFirmaInvalida) {
				t.Errorf("err = %v; quiero ErrFirmaInvalida", err)
			}
		})
	}
}

func TestVerificarFirmaAceptaElBordeDeLaTolerancia(t *testing.T) {
	ahora := time.Now().Truncate(time.Second)
	h := firmar("s", ahora.Add(-5*time.Minute).Unix(), "{}")
	if err := fav.VerificarFirma("s", h, []byte("{}"), ahora); err != nil {
		t.Errorf("firmado hace 5 minutos justos tiene que pasar: %v", err)
	}
}

func TestLeerAvisoIncompleto(t *testing.T) {
	for _, c := range []string{`no es json`, `{"eventoId":"x"}`, `{"eventoId":"x","pedido":{"pedidoId":"p"}}`} {
		if _, err := fav.LeerAviso([]byte(c)); !errors.Is(err, fav.ErrAvisoInvalido) {
			t.Errorf("LeerAviso(%s) = %v; quiero ErrAvisoInvalido", c, err)
		}
	}
}
