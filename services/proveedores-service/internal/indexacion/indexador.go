// Package indexacion mantiene el índice de búsqueda al día con PostgreSQL: drena
// los SKU pendientes y lo reconstruye si no está. No conoce ni a RabbitMQ ni a
// Meilisearch: habla por los puertos.
package indexacion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

const (
	// IntervaloPorDefecto es cada cuánto se mira si hay algo pendiente, aunque
	// nadie avise. El aviso del consumidor es lo normal; esto cubre la vuelta del
	// motor después de una caída y a la réplica que no consume.
	IntervaloPorDefecto = 2 * time.Second
	limiteDrenado       = 500
	timeoutVuelta       = 5 * time.Minute
	esperaOcupado       = 500 * time.Millisecond
	intentosOcupado     = 120
)

// ErrOcupado indica que otra réplica estuvo escribiendo el índice todo el tiempo
// que se esperó.
var ErrOcupado = errors.New("otra réplica está escribiendo el índice")

// Indexador es el ciclo de fondo. AvisarPendientes se puede llamar desde otra
// goroutine.
type Indexador struct {
	almacen   ports.AlmacenIndexacion
	indice    ports.IndiceProductos
	log       *slog.Logger
	intervalo time.Duration
	aviso     chan struct{}
	fallando  bool
}

var _ ports.AvisoIndexacion = (*Indexador)(nil)

// Nuevo arma el indexador. Con intervalo 0 usa IntervaloPorDefecto.
func Nuevo(almacen ports.AlmacenIndexacion, indice ports.IndiceProductos, log *slog.Logger, intervalo time.Duration) *Indexador {
	if intervalo <= 0 {
		intervalo = IntervaloPorDefecto
	}
	return &Indexador{almacen: almacen, indice: indice, log: log, intervalo: intervalo, aviso: make(chan struct{}, 1)}
}

// AvisarPendientes despierta al ciclo. Si ya hay un aviso sin atender, ese alcanza.
func (x *Indexador) AvisarPendientes() {
	select {
	case x.aviso <- struct{}{}:
	default:
	}
}

// Correr repite la vuelta hasta que se cancele ctx. Un motor caído solo se
// loguea: los SKU quedan en la tabla hasta que vuelva.
func (x *Indexador) Correr(ctx context.Context) {
	tic := time.NewTicker(x.intervalo)
	defer tic.Stop()
	x.registrar(ctx, x.Vuelta(ctx))
	for {
		select {
		case <-ctx.Done():
			return
		case <-x.aviso:
		case <-tic.C:
		}
		x.registrar(ctx, x.Vuelta(ctx))
	}
}

// registrar loguea solo los cambios de estado: con el motor caído no vale una
// línea cada pocos segundos.
func (x *Indexador) registrar(ctx context.Context, err error) {
	switch {
	case err != nil && ctx.Err() != nil:
	case err != nil && !x.fallando:
		x.fallando = true
		x.log.WarnContext(ctx, "no se pudo actualizar el índice: los SKU quedan pendientes", "error", err.Error())
	case err == nil && x.fallando:
		x.fallando = false
		x.log.InfoContext(ctx, "el índice volvió a actualizarse")
	}
}

// Vuelta se asegura de que el índice esté cargado y drena lo pendiente.
func (x *Indexador) Vuelta(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, timeoutVuelta)
	defer cancel()

	cargado, err := x.indice.IndiceCargado(ctx)
	if err != nil {
		return fmt.Errorf("mirar el índice: %w", err)
	}
	if cargado {
		// Esta consulta sola evita tomar el lock en cada vuelta sin trabajo.
		hay, err := x.almacen.SKUsPendientes(ctx, 1)
		if err != nil {
			return err
		}
		if len(hay) == 0 {
			return nil
		}
	}
	// Si otra réplica tiene la exclusión, ella se ocupa; lo que quede, en la
	// próxima vuelta.
	_, err = x.almacen.ConExclusionIndexacion(ctx, func(ctx context.Context) error {
		if !cargado {
			// Se vuelve a mirar: otra réplica pudo terminarla recién.
			listo, err := x.indice.IndiceCargado(ctx)
			if err != nil {
				return err
			}
			if !listo {
				x.log.InfoContext(ctx, "el índice no existe: se reconstruye desde PostgreSQL")
				if err := x.reconstruir(ctx); err != nil {
					return err
				}
			}
		}
		return x.drenar(ctx)
	})
	return err
}

func (x *Indexador) drenar(ctx context.Context) error {
	for {
		// El orden importa: primero los pendientes y después los documentos. Si un
		// SKU cambia en el medio, su versión ya no coincide y sigue pendiente.
		pendientes, err := x.almacen.SKUsPendientes(ctx, limiteDrenado)
		if err != nil {
			return err
		}
		if len(pendientes) == 0 {
			return nil
		}
		skus := make([]string, len(pendientes))
		for i, p := range pendientes {
			skus[i] = p.SKU
		}
		docs, err := x.almacen.DocumentosDeProductos(ctx, skus)
		if err != nil {
			return err
		}
		if err := x.indice.Indexar(ctx, docs); err != nil {
			return err
		}
		if err := x.almacen.QuitarPendientes(ctx, pendientes); err != nil {
			return err
		}
		if len(pendientes) < limiteDrenado {
			return nil
		}
	}
}

func (x *Indexador) reconstruir(ctx context.Context) error {
	docs, err := x.almacen.TodosLosDocumentos(ctx)
	if err != nil {
		return err
	}
	if err := x.indice.Reconstruir(ctx, docs); err != nil {
		return err
	}
	x.log.InfoContext(ctx, "índice reconstruido", "documentos", len(docs))
	return nil
}

// Reconstruir rehace el índice desde cero aunque ya exista, y deja drenado lo que
// haya cambiado mientras tanto. Si otra réplica está indexando, espera su turno.
func (x *Indexador) Reconstruir(ctx context.Context) error {
	for range intentosOcupado {
		corrio, err := x.almacen.ConExclusionIndexacion(ctx, func(ctx context.Context) error {
			if err := x.reconstruir(ctx); err != nil {
				return err
			}
			return x.drenar(ctx)
		})
		if err != nil {
			return err
		}
		if corrio {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(esperaOcupado):
		}
	}
	return ErrOcupado
}
