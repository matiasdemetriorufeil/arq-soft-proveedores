// Package redis es la caché de ofertas (ADR-007): un decorador de
// ports.OfertasRepo. Ni el dominio ni el handler saben que existe, y por eso se
// puede apagar con una variable de entorno y medir con y sin ella.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/domain"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

const (
	// PrefijoClave incluye la versión del formato: cambiarlo evita leer JSON viejo.
	PrefijoClave = "ofertas:v1:"
	// VigenciaOfertas es el TTL de las ofertas de un SKU.
	VigenciaOfertas = 60 * time.Second
	// VigenciaNoExiste es el TTL de la marca de SKU inexistente.
	VigenciaNoExiste = 10 * time.Second
	// TimeoutRedis es lo máximo que una lectura o escritura de la caché puede
	// demorar a una request; pasado eso, se sigue con PostgreSQL.
	TimeoutRedis = 50 * time.Millisecond
	// marcaNoExiste no es JSON de una lista, así que no se confunde con ofertas.
	marcaNoExiste = "no-existe"
	// largoMaximoSKU: un SKU más largo que esto no es un SKU; va a la base sin
	// pasar por la caché, para que no se puedan llenar la memoria de Redis con
	// marcas enormes.
	largoMaximoSKU = 256
)

// ErrNoEsta indica que la clave no está en la caché.
var ErrNoEsta = errors.New("clave ausente")

// Almacen es lo que la caché usa de Redis; permite probarla sin Redis.
type Almacen interface {
	Get(ctx context.Context, clave string) (string, error)
	Set(ctx context.Context, clave, valor string, vigencia time.Duration) error
	Del(ctx context.Context, claves ...string) error
}

// Cache decora un OfertasRepo. Falla abierta: si Redis no responde, se lee del
// repositorio y la request no se entera.
type Cache struct {
	almacen Almacen
	origen  ports.OfertasRepo
	log     *slog.Logger
	grupo   singleflight.Group
}

var (
	_ ports.OfertasRepo        = (*Cache)(nil)
	_ ports.InvalidadorOfertas = (*Cache)(nil)
)

// NuevaCache envuelve origen.
func NuevaCache(almacen Almacen, origen ports.OfertasRepo, log *slog.Logger) *Cache {
	return &Cache{almacen: almacen, origen: origen, log: log}
}

// OfertasPorProducto cumple el mismo contrato que el repositorio que envuelve.
func (c *Cache) OfertasPorProducto(ctx context.Context, sku string) ([]domain.Oferta, error) {
	if len(sku) > largoMaximoSKU {
		return c.origen.OfertasPorProducto(ctx, sku)
	}
	clave := PrefijoClave + sku

	lectura, cancel := context.WithTimeout(ctx, TimeoutRedis)
	valor, err := c.almacen.Get(lectura, clave)
	cancel()
	escribir := true
	switch {
	case err == nil:
		if valor == marcaNoExiste {
			return nil, domain.ErrProductoNoEncontrado
		}
		var ofertas []domain.Oferta
		if err := json.Unmarshal([]byte(valor), &ofertas); err == nil {
			return ofertas, nil
		}
		c.log.WarnContext(ctx, "valor ilegible en la caché: se lee de la base", "clave", clave)
	case errors.Is(err, ErrNoEsta):
	default:
		c.log.WarnContext(ctx, "la caché no respondió: se lee de la base", "error", err.Error())
		// Si no contestó recién, escribirle sumaría otros 50 ms a la request.
		escribir = false
	}
	return c.llenar(ctx, sku, clave, escribir)
}

// llenar lee del repositorio una sola vez por réplica, aunque haya muchas
// requests pidiendo el mismo SKU.
func (c *Cache) llenar(ctx context.Context, sku, clave string, escribir bool) ([]domain.Oferta, error) {
	resultado := c.grupo.DoChan(sku, func() (any, error) {
		// Sin la cancelación de esta request: la lectura la comparten varias, y
		// que se vaya la primera no puede hacer fallar a las demás. El repositorio
		// tiene su propio timeout.
		origen := context.WithoutCancel(ctx)
		ofertas, err := c.origen.OfertasPorProducto(origen, sku)
		switch {
		case errors.Is(err, domain.ErrProductoNoEncontrado):
			if escribir {
				c.guardar(origen, clave, marcaNoExiste, VigenciaNoExiste)
			}
			return nil, err
		case err != nil:
			return nil, err // una falla de la base nunca se guarda
		}
		if !escribir {
			return ofertas, nil
		}
		if crudo, err := json.Marshal(ofertas); err == nil {
			c.guardar(origen, clave, string(crudo), VigenciaOfertas)
		}
		return ofertas, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resultado:
		if r.Err != nil {
			return nil, r.Err
		}
		// Copia: la lista la reciben todas las requests que esperaban.
		return slices.Clone(r.Val.([]domain.Oferta)), nil
	}
}

func (c *Cache) guardar(ctx context.Context, clave, valor string, vigencia time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutRedis)
	defer cancel()
	if err := c.almacen.Set(ctx, clave, valor, vigencia); err != nil {
		c.log.WarnContext(ctx, "no se pudo escribir en la caché", "clave", clave, "error", err.Error())
	}
}

// InvalidarOfertas borra las claves de esos SKU. También suelta las lecturas en
// curso de esta réplica, para que la siguiente request no se sume a una que
// empezó antes del cambio.
func (c *Cache) InvalidarOfertas(ctx context.Context, skus []string) error {
	if len(skus) == 0 {
		return nil
	}
	claves := make([]string, len(skus))
	for i, sku := range skus {
		claves[i] = PrefijoClave + sku
		c.grupo.Forget(sku)
	}
	return c.almacen.Del(ctx, claves...)
}

// AlmacenRedis es el Almacen sobre un servidor real.
type AlmacenRedis struct {
	cliente *goredis.Client
}

// NuevoAlmacen no se conecta todavía: la primera operación abre la conexión. Así
// el servicio arranca aunque Redis no esté. Sin reintentos y con los timeouts de
// conexión, lectura y pool en TimeoutRedis, ninguna operación puede pasarse.
func NuevoAlmacen(url string) (*AlmacenRedis, error) {
	opciones, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL inválida: %w", err)
	}
	opciones.DialTimeout = TimeoutRedis
	opciones.ReadTimeout = TimeoutRedis
	opciones.WriteTimeout = TimeoutRedis
	opciones.PoolTimeout = TimeoutRedis
	opciones.MaxRetries = -1
	opciones.ContextTimeoutEnabled = true
	opciones.DisableIdentity = true
	return &AlmacenRedis{cliente: goredis.NewClient(opciones)}, nil
}

// Get devuelve ErrNoEsta si la clave no existe.
func (a *AlmacenRedis) Get(ctx context.Context, clave string) (string, error) {
	valor, err := a.cliente.Get(ctx, clave).Result()
	if errors.Is(err, goredis.Nil) {
		return "", ErrNoEsta
	}
	return valor, err
}

// Set guarda con vigencia.
func (a *AlmacenRedis) Set(ctx context.Context, clave, valor string, vigencia time.Duration) error {
	return a.cliente.Set(ctx, clave, valor, vigencia).Err()
}

// Del borra las claves.
func (a *AlmacenRedis) Del(ctx context.Context, claves ...string) error {
	return a.cliente.Del(ctx, claves...).Err()
}

// Cerrar libera las conexiones.
func (a *AlmacenRedis) Cerrar() error {
	return a.cliente.Close()
}
