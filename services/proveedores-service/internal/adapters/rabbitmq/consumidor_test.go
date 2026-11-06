package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

// respuesta registra qué le contestó el consumidor al broker.
type respuesta struct {
	acks, nacks int
	requeue     bool
}

func (r *respuesta) Ack(uint64, bool) error { r.acks++; return nil }
func (r *respuesta) Nack(_ uint64, _, requeue bool) error {
	r.nacks++
	r.requeue = requeue
	return nil
}
func (r *respuesta) Reject(uint64, bool) error { return errors.New("no se usa") }

type aplicadorFalso struct {
	res      ports.ResultadoAplicacion
	err      error
	recibido []ports.CambioListaPrecios
}

func (a *aplicadorFalso) AplicarListaPrecios(_ context.Context, c ports.CambioListaPrecios) (ports.ResultadoAplicacion, error) {
	a.recibido = append(a.recibido, c)
	return a.res, a.err
}

type invalidadorFalso struct {
	skus []string
	err  error
}

func (i *invalidadorFalso) InvalidarOfertas(_ context.Context, skus []string) error {
	i.skus = append(i.skus, skus...)
	return i.err
}

type avisoFalso struct{ avisos int }

func (a *avisoFalso) AvisarPendientes() { a.avisos++ }

type banco struct {
	aplicador   *aplicadorFalso
	invalidador *invalidadorFalso
	aviso       *avisoFalso
	consumidor  *Consumidor
}

func nuevoBanco() *banco {
	b := &banco{
		aplicador:   &aplicadorFalso{res: ports.ResultadoAplicacion{Aplicado: true, SKUs: []string{"FAV-1"}}},
		invalidador: &invalidadorFalso{},
		aviso:       &avisoFalso{},
	}
	b.consumidor = NuevoConsumidor(Config{
		Aplicador:       b.aplicador,
		Invalidador:     b.invalidador,
		Aviso:           b.aviso,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		EsperaReintento: time.Millisecond,
	})
	return b
}

func datosValidos() mensajeria.ListaPreciosActualizada {
	return mensajeria.ListaPreciosActualizada{
		Proveedor: mensajeria.ProveedorEvento{ID: 11, Codigo: "fav", Nombre: "FAV"},
		Secuencia: 7,
		Completa:  true,
		Productos: []mensajeria.ProductoEvento{{SKU: "FAV-1", Nombre: "Buzo", Categoria: "ropa-buzos"}},
		Ofertas:   []mensajeria.OfertaEvento{{ProductoSKU: "FAV-1", PrecioUnitario: 100, StockDisponible: 10}},
		Bajas:     []string{"FAV-2"},
	}
}

func sobreCrudo(t *testing.T, tipo string, version int, datos any) []byte {
	t.Helper()
	s, err := mensajeria.NuevoSobre(context.Background(), tipo, version, time.Now(), datos)
	if err != nil {
		t.Fatal(err)
	}
	crudo, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return crudo
}

func entrega(cuerpo []byte, r *respuesta, headers amqp.Table) amqp.Delivery {
	return amqp.Delivery{Acknowledger: r, Body: cuerpo, Headers: headers}
}

func TestEventoValidoSeAplicaInvalidaAvisaYConfirma(t *testing.T) {
	b := nuevoBanco()
	r := &respuesta{}
	b.consumidor.procesar(context.Background(), entrega(
		sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, nil))

	if r.acks != 1 || r.nacks != 0 {
		t.Fatalf("respuesta = %+v, quiero un Ack", r)
	}
	c := b.aplicador.recibido[0]
	if c.EventoID == "" || c.ProveedorID != 11 || c.Secuencia != 7 || !c.Completa {
		t.Errorf("cambio = %+v", c)
	}
	if c.Productos[0].Unidad != "unidad" {
		t.Errorf("unidad vacía tenía que tomar el default de la columna: %+v", c.Productos[0])
	}
	if c.Ofertas[0].PrecioUnitario != 100 || c.Bajas[0] != "FAV-2" {
		t.Errorf("cambio = %+v", c)
	}
	if len(b.invalidador.skus) != 1 || b.aviso.avisos != 1 {
		t.Errorf("invalidados = %v, avisos = %d", b.invalidador.skus, b.aviso.avisos)
	}
}

func TestEventoDescartadoConfirmaSinInvalidar(t *testing.T) {
	b := nuevoBanco()
	b.aplicador.res = ports.ResultadoAplicacion{Motivo: "duplicado"}
	r := &respuesta{}
	b.consumidor.procesar(context.Background(), entrega(
		sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, nil))

	if r.acks != 1 {
		t.Fatalf("respuesta = %+v, un duplicado se confirma", r)
	}
	if len(b.invalidador.skus) != 0 || b.aviso.avisos != 0 {
		t.Errorf("un descartado no invalida ni avisa: %v, %d", b.invalidador.skus, b.aviso.avisos)
	}
}

func TestLoQueNoSePuedeProcesarVaDirectoALaDLQ(t *testing.T) {
	sinProveedor := datosValidos()
	sinProveedor.Proveedor.ID = 0
	sinSecuencia := datosValidos()
	sinSecuencia.Secuencia = 0
	precioNegativo := datosValidos()
	precioNegativo.Ofertas[0].PrecioUnitario = -1
	descuentoDeMas := datosValidos()
	descuentoDeMas.Ofertas[0].DescuentoPct = 101
	stockDeMas := datosValidos()
	stockDeMas.Ofertas[0].StockDisponible = maxEntero + 1
	bajaVacia := datosValidos()
	bajaVacia.Bajas = []string{" "}
	sinSKU := datosValidos()
	sinSKU.Productos[0].SKU = ""

	casos := map[string][]byte{
		"no es JSON":       []byte("esto no es JSON"),
		"sobre sin datos":  []byte(`{"evento_id":"x","tipo":"ListaPreciosActualizada","version":1}`),
		"tipo desconocido": sobreCrudo(t, "OtroEvento", 1, datosValidos()),
		"versión futura":   sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 2, datosValidos()),
		"datos de otro tipo": sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1,
			map[string]any{"secuencia": "no es un número"}),
		"sin proveedor":    sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, sinProveedor),
		"sin secuencia":    sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, sinSecuencia),
		"precio negativo":  sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, precioNegativo),
		"descuento > 100":  sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, descuentoDeMas),
		"stock enorme":     sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, stockDeMas),
		"baja vacía":       sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, bajaVacia),
		"producto sin SKU": sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, sinSKU),
	}
	for nombre, cuerpo := range casos {
		t.Run(nombre, func(t *testing.T) {
			b := nuevoBanco()
			r := &respuesta{}
			b.consumidor.procesar(context.Background(), entrega(cuerpo, r, nil))
			if r.nacks != 1 || r.requeue || r.acks != 0 {
				t.Errorf("respuesta = %+v, quiero Nack sin reencolar", r)
			}
			if len(b.aplicador.recibido) != 0 {
				t.Error("no tenía que llegar a la base")
			}
		})
	}
}

func TestErrorTransitorioReencolaHastaLaTerceraEntrega(t *testing.T) {
	casos := []struct {
		nombre  string
		headers amqp.Table
		requeue bool
	}{
		{"primera entrega", nil, true},
		{"segunda entrega", amqp.Table{"x-delivery-count": int64(1)}, true},
		{"tercera entrega", amqp.Table{"x-delivery-count": int64(2)}, false},
		{"contador int32", amqp.Table{"x-delivery-count": int32(2)}, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			b := nuevoBanco()
			b.aplicador.err = errors.New("la base no responde")
			r := &respuesta{}
			b.consumidor.procesar(context.Background(), entrega(
				sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, c.headers))
			if r.nacks != 1 || r.requeue != c.requeue || r.acks != 0 {
				t.Errorf("respuesta = %+v, quiero Nack con requeue=%v", r, c.requeue)
			}
			if len(b.invalidador.skus) != 0 || b.aviso.avisos != 0 {
				t.Error("sin aplicar no hay nada que invalidar ni indexar")
			}
		})
	}
}

func TestApagadoDuranteLaEsperaNoContestaAlBroker(t *testing.T) {
	b := nuevoBanco()
	b.consumidor.cfg.EsperaReintento = time.Hour
	b.aplicador.err = errors.New("la base no responde")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &respuesta{}
	b.consumidor.procesar(ctx, entrega(sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, nil))
	if r.nacks != 0 || r.acks != 0 {
		t.Errorf("respuesta = %+v: al cerrar la conexión el broker lo reencola solo", r)
	}
}

func TestCacheCaidaNoFrenaAlEvento(t *testing.T) {
	b := nuevoBanco()
	b.invalidador.err = errors.New("redis caído")
	r := &respuesta{}
	b.consumidor.procesar(context.Background(), entrega(
		sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, nil))
	if r.acks != 1 || b.aviso.avisos != 1 {
		t.Errorf("respuesta = %+v, avisos = %d: el evento ya está aplicado", r, b.aviso.avisos)
	}
}

func TestSinCacheNiBusquedaElEventoSeAplicaIgual(t *testing.T) {
	aplicador := &aplicadorFalso{res: ports.ResultadoAplicacion{Aplicado: true, SKUs: []string{"FAV-1"}}}
	c := NuevoConsumidor(Config{Aplicador: aplicador, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	r := &respuesta{}
	c.procesar(context.Background(), entrega(sobreCrudo(t, mensajeria.TipoListaPreciosActualizada, 1, datosValidos()), r, nil))
	if r.acks != 1 {
		t.Errorf("respuesta = %+v", r)
	}
}

func TestCorrerSinBrokerNoCuelgaAlCancelar(t *testing.T) {
	c := NuevoConsumidor(Config{URL: "amqp://guest:guest@127.0.0.1:1/", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ctx, cancel := context.WithCancel(context.Background())
	listo := make(chan struct{})
	go func() { c.Correr(ctx); close(listo) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-listo:
	case <-time.After(5 * time.Second):
		t.Fatal("Correr no volvió después de cancelar")
	}
}
