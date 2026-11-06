// Package sincronizacion orquesta la importación del catálogo de FAV: lo baja,
// lo traduce, lo compara con lo último que se avisó, guarda la importación y
// publica el evento ListaPreciosActualizada. Depende de puertos, no de MongoDB
// ni de RabbitMQ: los adaptadores están en internal/adapters.
package sincronizacion

import (
	"context"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
)

// Estados de una importación.
const (
	// EstadoConCambios: el catálogo cambió y se generó un evento.
	EstadoConCambios = "con_cambios"
	// EstadoSinCambios: FAV respondió y no hubo nada que avisar.
	EstadoSinCambios = "sin_cambios"
	// EstadoError: FAV no respondió o respondió fuera de contrato.
	EstadoError = "error"
	// EstadoVencido: pasaron más de 24 h sin sincronizar y se retiraron las
	// ofertas de FAV del ranking.
	EstadoVencido = "vencido"
)

// Importacion es un documento de la colección importaciones. Cada intento deja
// uno, salga bien o mal: así se mide la tasa de éxito (ADR-009).
type Importacion struct {
	ID            string
	Proveedor     string
	IniciadaEn    time.Time
	TerminadaEn   time.Time
	Estado        string
	Error         string
	Intentos      int
	CorrelationID string
	// Crudo es la respuesta de FAV tal cual llegó. Solo se guarda cuando hubo
	// cambios: es lo que permite reconstruir por qué cambió un precio.
	Crudo     []byte
	Variantes int
	Descartes []dominio.Descarte
	Altas     int
	Modific   int
	Bajas     int

	// Secuencia y Vigentes solo están en las importaciones que cambian lo que
	// tiene el consumidor (con_cambios y vencido). Vigentes es el catálogo
	// traducido que queda vigente después de aplicar el evento.
	Secuencia int64
	Vigentes  []dominio.Item

	// Evento es el outbox: se guarda en el mismo documento que la
	// importación, así las dos cosas se escriben juntas o no se escribe
	// ninguna (una escritura de un documento es atómica en MongoDB).
	Evento *EventoOutbox
}

// EventoOutbox es el evento a publicar y si ya se publicó.
type EventoOutbox struct {
	Sobre       mensajeria.Sobre
	Publicado   bool
	PublicadoEn time.Time
}

// Almacen es lo que la sincronización necesita de la base.
type Almacen interface {
	// UltimaVigente devuelve la última importación con Vigentes (la de mayor
	// Secuencia). ok es false si nunca hubo una.
	UltimaVigente(ctx context.Context) (imp Importacion, ok bool, err error)
	// UltimaSincronizacionOK devuelve cuándo terminó la última importación en
	// la que FAV respondió bien.
	UltimaSincronizacionOK(ctx context.Context) (t time.Time, ok bool, err error)
	Guardar(ctx context.Context, imp Importacion) error
	// Pendientes devuelve los eventos sin publicar, por Secuencia ascendente.
	Pendientes(ctx context.Context, limite int) ([]Importacion, error)
	ContarPendientes(ctx context.Context) (int64, error)
	MarcarPublicado(ctx context.Context, id string, en time.Time) error
	Ping(ctx context.Context) error
}

// Publicador es lo que se necesita del broker.
type Publicador interface {
	Publicar(ctx context.Context, clave string, s mensajeria.Sobre) error
	Conectado() bool
}

// FuenteCatalogo es el cliente de FAV.
type FuenteCatalogo interface {
	ListarProductos(ctx context.Context, correlationID string) (fav.Resultado, error)
}
