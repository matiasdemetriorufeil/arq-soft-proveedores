// Package mensajeria reúne lo que comparten quien publica y quien consume los
// eventos de dominio por RabbitMQ: el sobre común de ADR-005, la topología
// (exchanges, colas y cola de mensajes fallidos) y un publicador con
// confirmaciones del broker.
//
// Está en un paquete propio para que import-service (publica) y
// proveedores-service (consume) usen exactamente las mismas declaraciones: si
// cada uno declarara la cola con argumentos distintos, RabbitMQ rechazaría la
// segunda declaración con PRECONDITION_FAILED.
package mensajeria

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Sobre es el formato común de todos los eventos (ADR-005). Los datos propios
// de cada evento van en Datos; el resto lo necesita cualquier consumidor para
// deduplicar (EventoID), elegir cómo leer los datos (Tipo y Version) y seguir
// la traza (Traceparent).
type Sobre struct {
	EventoID    string          `json:"evento_id"`
	Tipo        string          `json:"tipo"`
	Version     int             `json:"version"`
	OcurridoEn  time.Time       `json:"ocurrido_en"`
	Traceparent string          `json:"traceparent,omitempty"`
	Datos       json.RawMessage `json:"datos"`
}

// NuevoSobre arma el sobre de un evento con un id nuevo y el traceparent del
// span que esté en ctx, así el consumidor continúa la misma traza.
func NuevoSobre(ctx context.Context, tipo string, version int, ocurridoEn time.Time, datos any) (Sobre, error) {
	crudo, err := json.Marshal(datos)
	if err != nil {
		return Sobre{}, fmt.Errorf("serializar datos de %s: %w", tipo, err)
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return Sobre{
		EventoID:    NuevoID(),
		Tipo:        tipo,
		Version:     version,
		OcurridoEn:  ocurridoEn.UTC(),
		Traceparent: carrier.Get("traceparent"),
		Datos:       crudo,
	}, nil
}

// ContextoDeTraza devuelve ctx con el span remoto del sobre, para que el
// consumidor abra su span como hijo del que publicó el evento.
func (s Sobre) ContextoDeTraza(ctx context.Context) context.Context {
	if s.Traceparent == "" {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": s.Traceparent}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

// ErrSobreInvalido marca un mensaje que no se puede procesar nunca, por más que
// se reintente: el consumidor lo manda directo a la cola de fallidos.
var ErrSobreInvalido = errors.New("sobre inválido")

// LeerSobre decodifica y valida un mensaje. Un error envuelve ErrSobreInvalido.
func LeerSobre(cuerpo []byte) (Sobre, error) {
	var s Sobre
	if err := json.Unmarshal(cuerpo, &s); err != nil {
		return Sobre{}, fmt.Errorf("%w: %v", ErrSobreInvalido, err)
	}
	switch {
	case s.EventoID == "":
		return Sobre{}, fmt.Errorf("%w: falta evento_id", ErrSobreInvalido)
	case s.Tipo == "":
		return Sobre{}, fmt.Errorf("%w: falta tipo", ErrSobreInvalido)
	case s.Version < 1:
		return Sobre{}, fmt.Errorf("%w: version tiene que ser 1 o mayor", ErrSobreInvalido)
	case len(s.Datos) == 0 || string(s.Datos) == "null":
		return Sobre{}, fmt.Errorf("%w: faltan datos", ErrSobreInvalido)
	}
	return s, nil
}

// NuevoID devuelve un UUID v4 en texto. Va sin dependencias externas: es lo
// único que se necesita de una biblioteca de UUID.
func NuevoID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand no falla en las plataformas en las que corremos; si
		// fallara, no hay forma segura de seguir.
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
