package mensajeria

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestNuevoSobreLlevaIDTipoYTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "publicar")
	defer span.End()

	ocurrido := time.Date(2026, 11, 4, 12, 0, 0, 0, time.FixedZone("ART", -3*3600))
	s, err := NuevoSobre(ctx, TipoListaPreciosActualizada, 1, ocurrido, map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(s.EventoID) {
		t.Errorf("evento_id no es un UUID v4: %q", s.EventoID)
	}
	if s.OcurridoEn.Location() != time.UTC {
		t.Errorf("ocurrido_en tiene que ir en UTC, vino %v", s.OcurridoEn.Location())
	}
	if string(s.Datos) != `{"a":1}` {
		t.Errorf("datos = %s", s.Datos)
	}

	// El consumidor tiene que poder continuar la misma traza.
	ctxConsumidor := s.ContextoDeTraza(context.Background())
	got := trace.SpanContextFromContext(ctxConsumidor)
	if got.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("trace_id del consumidor = %s, quiero %s", got.TraceID(), span.SpanContext().TraceID())
	}
}

func TestNuevoSobreSinSpanNoInventaTraceparent(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	s, err := NuevoSobre(context.Background(), "X", 1, time.Now(), struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Traceparent != "" {
		t.Errorf("traceparent = %q, quiero vacío", s.Traceparent)
	}
}

func TestIDsNoSeRepiten(t *testing.T) {
	vistos := map[string]bool{}
	for range 1000 {
		id := NuevoID()
		if vistos[id] {
			t.Fatalf("id repetido: %s", id)
		}
		vistos[id] = true
	}
}

func TestLeerSobre(t *testing.T) {
	valido := `{"evento_id":"e1","tipo":"T","version":1,"ocurrido_en":"2026-11-04T15:00:00Z","datos":{"x":1}}`
	if _, err := LeerSobre([]byte(valido)); err != nil {
		t.Fatalf("sobre válido rechazado: %v", err)
	}

	casos := map[string]string{
		"no es json":    `{`,
		"sin evento_id": `{"tipo":"T","version":1,"datos":{}}`,
		"sin tipo":      `{"evento_id":"e1","version":1,"datos":{}}`,
		"version 0":     `{"evento_id":"e1","tipo":"T","version":0,"datos":{}}`,
		"sin datos":     `{"evento_id":"e1","tipo":"T","version":1}`,
		"datos en null": `{"evento_id":"e1","tipo":"T","version":1,"datos":null}`,
	}
	for nombre, cuerpo := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := LeerSobre([]byte(cuerpo))
			if !errors.Is(err, ErrSobreInvalido) {
				t.Errorf("error = %v, quiero ErrSobreInvalido", err)
			}
		})
	}
}

func TestListaPreciosSerializaConLosNombresDelContrato(t *testing.T) {
	d := ListaPreciosActualizada{
		Proveedor:     ProveedorEvento{ID: 11, Codigo: "fav", Nombre: "FAV"},
		ImportacionID: "imp-1",
		Secuencia:     7,
		Motivo:        MotivoSincronizacion,
		Productos:     []ProductoEvento{{SKU: "FAV-v1", Nombre: "Remera", Categoria: "ropa-remeras", Unidad: "unidad"}},
		Ofertas:       []OfertaEvento{{ProductoSKU: "FAV-v1", PrecioUnitario: 15000, StockDisponible: 10}},
		Bajas:         []string{"FAV-v2"},
	}
	b, _ := json.Marshal(d)
	var m map[string]any
	json.Unmarshal(b, &m)
	for _, campo := range []string{"proveedor", "importacion_id", "secuencia", "motivo", "completa", "productos", "ofertas", "bajas"} {
		if _, ok := m[campo]; !ok {
			t.Errorf("falta el campo %q en %s", campo, b)
		}
	}
	oferta := m["ofertas"].([]any)[0].(map[string]any)
	for _, campo := range []string{"producto_sku", "precio_unitario", "descuento_pct", "cantidad_minima_descuento", "flete", "stock_disponible"} {
		if _, ok := oferta[campo]; !ok {
			t.Errorf("falta el campo %q en la oferta", campo)
		}
	}
}

type declaracion struct {
	tipo, nombre, extra string
	args                amqp.Table
}

type declaradorFalso struct{ hechas []declaracion }

func (d *declaradorFalso) ExchangeDeclare(name, kind string, durable, _, _, _ bool, args amqp.Table) error {
	if !durable {
		return errors.New("exchange no durable")
	}
	d.hechas = append(d.hechas, declaracion{"exchange", name, kind, args})
	return nil
}

func (d *declaradorFalso) QueueDeclare(name string, durable, _, _, _ bool, args amqp.Table) (amqp.Queue, error) {
	if !durable {
		return amqp.Queue{}, errors.New("cola no durable")
	}
	d.hechas = append(d.hechas, declaracion{"cola", name, "", args})
	return amqp.Queue{Name: name}, nil
}

func (d *declaradorFalso) QueueBind(name, key, exchange string, _ bool, _ amqp.Table) error {
	d.hechas = append(d.hechas, declaracion{"bind", name, exchange + "/" + key, nil})
	return nil
}

func TestTopologiaMandaLosFallidosALaDLQ(t *testing.T) {
	d := &declaradorFalso{}
	if err := DeclararTopologia(d); err != nil {
		t.Fatal(err)
	}

	var principal *declaracion
	binds := map[string]string{}
	for i, h := range d.hechas {
		if h.tipo == "cola" && h.nombre == ColaListaPrecios {
			principal = &d.hechas[i]
		}
		if h.tipo == "bind" {
			binds[h.nombre] = h.extra
		}
	}
	if principal == nil {
		t.Fatal("no se declaró la cola principal")
	}
	if principal.args["x-dead-letter-exchange"] != ExchangeFallidos {
		t.Errorf("la cola principal no tiene DLX: %v", principal.args)
	}
	if principal.args["x-delivery-limit"] != int32(IntentosMaximos) {
		t.Errorf("x-delivery-limit = %v, quiero %d", principal.args["x-delivery-limit"], IntentosMaximos)
	}
	// La clave de dead-letter tiene que coincidir con el bind de la DLQ, si no
	// los fallidos se pierden.
	if got, want := binds[ColaListaPreciosFallidos], ExchangeFallidos+"/"+principal.args["x-dead-letter-routing-key"].(string); got != want {
		t.Errorf("bind de la DLQ = %q, quiero %q", got, want)
	}
	if got := binds[ColaListaPrecios]; got != ExchangeEventos+"/"+ClaveListaPrecios {
		t.Errorf("bind de la cola principal = %q", got)
	}
}
