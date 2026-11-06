// Package mongo guarda las importaciones en MongoDB (ADR-003). Cada intento
// de sincronización es un documento de la colección importaciones, con la
// respuesta cruda de FAV, el catálogo traducido vigente y el evento a publicar
// (outbox) en el mismo documento.
package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/sincronizacion"
)

// TimeoutOperacion acota cada operación contra la base.
const TimeoutOperacion = 3 * time.Second

const coleccion = "importaciones"

// Almacen implementa sincronizacion.Almacen sobre MongoDB.
type Almacen struct {
	cliente   *mongo.Client
	col       *mongo.Collection
	proveedor string
}

var _ sincronizacion.Almacen = (*Almacen)(nil)

// Conectar abre el cliente. No falla si la base todavía no está: el driver
// reconecta solo, y /readyz informa mientras tanto.
func Conectar(uri string) (*mongo.Client, error) {
	return mongo.Connect(options.Client().
		ApplyURI(uri).
		SetAppName("import-service").
		SetServerSelectionTimeout(TimeoutOperacion).
		SetConnectTimeout(TimeoutOperacion))
}

// Nuevo arma el almacén sobre la base dada, para un proveedor.
func Nuevo(cliente *mongo.Client, base, proveedor string) *Almacen {
	return &Almacen{cliente: cliente, col: cliente.Database(base).Collection(coleccion), proveedor: proveedor}
}

// CrearIndices es idempotente: crear un índice que ya existe no hace nada.
func (a *Almacen) CrearIndices(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*TimeoutOperacion)
	defer cancel()
	_, err := a.col.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// La última importación vigente, y que no haya dos con la misma
			// secuencia: es lo que ordena los eventos del proveedor.
			Keys: bson.D{{Key: "proveedor", Value: 1}, {Key: "secuencia", Value: -1}},
			Options: options.Index().SetName("secuencia_unica").SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "tiene_vigentes", Value: true}}),
		},
		{
			Keys:    bson.D{{Key: "proveedor", Value: 1}, {Key: "estado", Value: 1}, {Key: "terminada_en", Value: -1}},
			Options: options.Index().SetName("por_estado"),
		},
		{
			Keys: bson.D{{Key: "evento.publicado", Value: 1}, {Key: "secuencia", Value: 1}},
			Options: options.Index().SetName("outbox_pendientes").
				SetPartialFilterExpression(bson.D{{Key: "evento.publicado", Value: false}}),
		},
	})
	return err
}

type docEvento struct {
	EventoID    string     `bson:"evento_id"`
	Tipo        string     `bson:"tipo"`
	Sobre       string     `bson:"sobre"`
	Publicado   bool       `bson:"publicado"`
	PublicadoEn *time.Time `bson:"publicado_en,omitempty"`
}

type docImportacion struct {
	ID            string             `bson:"_id"`
	Proveedor     string             `bson:"proveedor"`
	IniciadaEn    time.Time          `bson:"iniciada_en"`
	TerminadaEn   time.Time          `bson:"terminada_en"`
	Estado        string             `bson:"estado"`
	Error         string             `bson:"error,omitempty"`
	Intentos      int                `bson:"intentos"`
	CorrelationID string             `bson:"correlation_id,omitempty"`
	Crudo         any                `bson:"crudo,omitempty"`
	Variantes     int                `bson:"variantes"`
	Descartes     []dominio.Descarte `bson:"descartes,omitempty"`
	Altas         int                `bson:"altas"`
	Modificadas   int                `bson:"modificadas"`
	Bajas         int                `bson:"bajas"`
	TieneVigentes bool               `bson:"tiene_vigentes"`
	Secuencia     int64              `bson:"secuencia,omitempty"`
	Vigentes      []dominio.Item     `bson:"vigentes,omitempty"`
	Evento        *docEvento         `bson:"evento,omitempty"`
}

// crudoComoDocumento guarda la respuesta de FAV como documento, con los campos
// que traiga aunque no los usemos: si FAV agrega uno, queda registrado sin
// cambiar el esquema. Si no se puede convertir, se guarda como texto.
func crudoComoDocumento(crudo []byte) any {
	if len(crudo) == 0 {
		return nil
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON(crudo, false, &doc); err == nil {
		return doc
	}
	return string(crudo)
}

func aDoc(imp sincronizacion.Importacion) (docImportacion, error) {
	d := docImportacion{
		ID: imp.ID, Proveedor: imp.Proveedor, IniciadaEn: imp.IniciadaEn, TerminadaEn: imp.TerminadaEn,
		Estado: imp.Estado, Error: imp.Error, Intentos: imp.Intentos, CorrelationID: imp.CorrelationID,
		Crudo: crudoComoDocumento(imp.Crudo), Variantes: imp.Variantes, Descartes: imp.Descartes,
		Altas: imp.Altas, Modificadas: imp.Modific, Bajas: imp.Bajas,
		TieneVigentes: imp.Vigentes != nil, Secuencia: imp.Secuencia, Vigentes: imp.Vigentes,
	}
	if d.TieneVigentes && d.Vigentes == nil {
		d.Vigentes = []dominio.Item{}
	}
	if imp.Evento != nil {
		sobre, err := json.Marshal(imp.Evento.Sobre)
		if err != nil {
			return docImportacion{}, err
		}
		d.Evento = &docEvento{
			EventoID: imp.Evento.Sobre.EventoID, Tipo: imp.Evento.Sobre.Tipo,
			Sobre: string(sobre), Publicado: imp.Evento.Publicado,
		}
		if imp.Evento.Publicado {
			t := imp.Evento.PublicadoEn
			d.Evento.PublicadoEn = &t
		}
	}
	return d, nil
}

func deDoc(d docImportacion) (sincronizacion.Importacion, error) {
	imp := sincronizacion.Importacion{
		ID: d.ID, Proveedor: d.Proveedor, IniciadaEn: d.IniciadaEn, TerminadaEn: d.TerminadaEn,
		Estado: d.Estado, Error: d.Error, Intentos: d.Intentos, CorrelationID: d.CorrelationID,
		Variantes: d.Variantes, Descartes: d.Descartes, Altas: d.Altas, Modific: d.Modificadas, Bajas: d.Bajas,
		Secuencia: d.Secuencia,
	}
	if d.TieneVigentes {
		imp.Vigentes = d.Vigentes
		if imp.Vigentes == nil {
			imp.Vigentes = []dominio.Item{}
		}
	}
	if d.Evento != nil {
		var s mensajeria.Sobre
		if err := json.Unmarshal([]byte(d.Evento.Sobre), &s); err != nil {
			return sincronizacion.Importacion{}, fmt.Errorf("evento %s ilegible: %w", d.Evento.EventoID, err)
		}
		imp.Evento = &sincronizacion.EventoOutbox{Sobre: s, Publicado: d.Evento.Publicado}
		if d.Evento.PublicadoEn != nil {
			imp.Evento.PublicadoEn = *d.Evento.PublicadoEn
		}
	}
	return imp, nil
}

// Guardar inserta la importación. Es una sola escritura: el evento del outbox
// entra junto con la importación o no entra.
func (a *Almacen) Guardar(ctx context.Context, imp sincronizacion.Importacion) error {
	d, err := aDoc(imp)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, TimeoutOperacion)
	defer cancel()
	_, err = a.col.InsertOne(ctx, d)
	return err
}

// sinCrudo deja afuera la respuesta cruda de FAV, que ninguna lectura necesita.
var sinCrudo = bson.D{{Key: "crudo", Value: 0}}

func (a *Almacen) una(ctx context.Context, filtro, orden, proyeccion bson.D) (sincronizacion.Importacion, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutOperacion)
	defer cancel()
	var d docImportacion
	err := a.col.FindOne(ctx, filtro, options.FindOne().SetSort(orden).SetProjection(proyeccion)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return sincronizacion.Importacion{}, false, nil
	}
	if err != nil {
		return sincronizacion.Importacion{}, false, err
	}
	imp, err := deDoc(d)
	return imp, err == nil, err
}

// UltimaVigente devuelve la importación vigente de mayor secuencia.
func (a *Almacen) UltimaVigente(ctx context.Context) (sincronizacion.Importacion, bool, error) {
	return a.una(ctx,
		bson.D{{Key: "proveedor", Value: a.proveedor}, {Key: "tiene_vigentes", Value: true}},
		bson.D{{Key: "secuencia", Value: -1}},
		sinCrudo)
}

// UltimaSincronizacionOK devuelve cuándo terminó la última importación buena.
func (a *Almacen) UltimaSincronizacionOK(ctx context.Context) (time.Time, bool, error) {
	imp, ok, err := a.una(ctx,
		bson.D{
			{Key: "proveedor", Value: a.proveedor},
			{Key: "estado", Value: bson.D{{Key: "$in", Value: bson.A{
				sincronizacion.EstadoConCambios, sincronizacion.EstadoSinCambios,
			}}}},
		},
		bson.D{{Key: "terminada_en", Value: -1}},
		// Solo hace falta la fecha: sin el catálogo vigente ni la respuesta cruda.
		bson.D{{Key: "crudo", Value: 0}, {Key: "vigentes", Value: 0}, {Key: "evento", Value: 0}})
	return imp.TerminadaEn, ok, err
}

// Pendientes devuelve los eventos sin publicar por secuencia ascendente.
func (a *Almacen) Pendientes(ctx context.Context, limite int) ([]sincronizacion.Importacion, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutOperacion)
	defer cancel()
	cur, err := a.col.Find(ctx,
		bson.D{{Key: "proveedor", Value: a.proveedor}, {Key: "evento.publicado", Value: false}},
		options.Find().SetSort(bson.D{{Key: "secuencia", Value: 1}}).SetLimit(int64(limite)).
			// Los eventos pendientes no necesitan la respuesta cruda.
			SetProjection(sinCrudo))
	if err != nil {
		return nil, err
	}
	var docs []docImportacion
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]sincronizacion.Importacion, 0, len(docs))
	for _, d := range docs {
		imp, err := deDoc(d)
		if err != nil {
			return nil, err
		}
		out = append(out, imp)
	}
	return out, nil
}

// ContarPendientes cuenta los eventos sin publicar.
func (a *Almacen) ContarPendientes(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutOperacion)
	defer cancel()
	return a.col.CountDocuments(ctx,
		bson.D{{Key: "proveedor", Value: a.proveedor}, {Key: "evento.publicado", Value: false}})
}

// MarcarPublicado registra que el broker confirmó el evento.
func (a *Almacen) MarcarPublicado(ctx context.Context, id string, en time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutOperacion)
	defer cancel()
	res, err := a.col.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "evento.publicado", Value: true},
			{Key: "evento.publicado_en", Value: en},
		}}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("importación %s no encontrada", id)
	}
	return nil
}

// Ping confirma que la base responde.
func (a *Almacen) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return a.cliente.Ping(ctx, nil)
}
