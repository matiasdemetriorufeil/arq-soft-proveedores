// Package repository es la capa de acceso a datos de stock-service: el único
// paquete que importa pgx y el único que escribe SQL.
//
// La arquitectura del servicio es en capas, con la dependencia en un solo
// sentido: internal/handler → internal/service → internal/repository (ADR-002).
// Por eso el límite transaccional lo abre la capa de servicio con
// EnTransaccion, pero el SELECT … FOR UPDATE que evita la sobreventa lo ejecuta
// este paquete: la capa de servicio nunca ve una pgx.Tx, solo el tipo opaco Tx.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TimeoutConsulta es el tiempo máximo de cada consulta —y de cada
// transacción— contra la base. Si PostgreSQL no responde, el servicio contesta
// 503 en lugar de colgarse.
const TimeoutConsulta = 2 * time.Second

// codigoViolacionUnica es el SQLSTATE de unique_violation en PostgreSQL.
const codigoViolacionUnica = "23505"

// Errores de la capa de datos. La capa de servicio los traduce a errores de
// negocio; no viajan hasta el handler.
var (
	// ErrNoEncontrado indica que la consulta no devolvió filas.
	ErrNoEncontrado = errors.New("no encontrado")
	// ErrClaveDuplicada indica que otro INSERT ya usó esa clave de
	// idempotencia. Es la carrera entre dos reintentos de la misma reserva.
	ErrClaveDuplicada = errors.New("clave de idempotencia duplicada")
)

// Stock es una fila de stock_productos.
type Stock struct {
	SKU        string `json:"sku"`
	Disponible int    `json:"disponible"`
	Reservado  int    `json:"reservado"`
	Minimo     int    `json:"minimo"`
}

// Reserva es una fila de reservas.
type Reserva struct {
	ID                string    `json:"id"`
	SKU               string    `json:"sku"`
	Cantidad          int       `json:"cantidad"`
	ClaveIdempotencia string    `json:"clave_idempotencia"`
	Estado            string    `json:"estado"`
	CreadoEn          time.Time `json:"creado_en"`
}

// Tx es el límite transaccional visto desde afuera de este paquete: la capa de
// servicio lo recibe y lo vuelve a pasar, pero no puede ejecutar nada con él.
// El método no exportado hace que solo este paquete pueda implementarlo, así
// que ningún otro puede abrir una transacción por su cuenta.
type Tx interface {
	pgxTx() pgx.Tx
}

type txPgx struct{ tx pgx.Tx }

func (t txPgx) pgxTx() pgx.Tx { return t.tx }

// Repo es el repositorio de stock sobre un pool de pgx.
type Repo struct {
	pool *pgxpool.Pool
}

// NuevoRepo crea el repositorio sobre un pool ya abierto.
func NuevoRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// NuevoPool abre un pool pgx a partir de DATABASE_URL.
func NuevoPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL inválida: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = TimeoutConsulta
	// Instrumenta el acceso a la base: cada consulta —y cada intento de
	// conexión que haga el pool— abre su span dentro de la traza de la request
	// (ver tracer.go).
	cfg.ConnConfig.Tracer = NuevoTracer(nil)
	return pgxpool.NewWithConfig(ctx, cfg)
}

// Ping verifica que la base responda dentro del timeout.
func (r *Repo) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return r.pool.Ping(ctx)
}

const queryStock = `
SELECT sku, disponible, reservado, minimo
FROM stock_productos
WHERE sku = $1`

// StockPorSKU devuelve la fila de stock sin bloquearla, o ErrNoEncontrado.
// Es la consulta de solo lectura: no sirve para decidir una reserva.
func (r *Repo) StockPorSKU(ctx context.Context, sku string) (Stock, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()

	var s Stock
	err := r.pool.QueryRow(ctx, queryStock, sku).
		Scan(&s.SKU, &s.Disponible, &s.Reservado, &s.Minimo)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stock{}, ErrNoEncontrado
	}
	if err != nil {
		return Stock{}, fmt.Errorf("consultar stock: %w", err)
	}
	return s, nil
}

const queryReservaPorClave = `
SELECT id::text, sku, cantidad, clave_idempotencia, estado, creado_en
FROM reservas
WHERE clave_idempotencia = $1`

// ReservaPorClave devuelve la reserva ya aplicada con esa clave de
// idempotencia, o ErrNoEncontrado si la clave es nueva.
func (r *Repo) ReservaPorClave(ctx context.Context, clave string) (Reserva, error) {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return escanearReserva(r.pool.QueryRow(ctx, queryReservaPorClave, clave))
}

// EnTransaccion ejecuta fn dentro de una transacción: commit si devuelve nil,
// rollback si devuelve error. Es el único lugar donde se abre una transacción, y
// lo llama la capa de servicio, que es la dueña de la regla de negocio.
func (r *Repo) EnTransaccion(ctx context.Context, fn func(Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, TimeoutConsulta)
	defer cancel()
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(txPgx{tx: tx})
	})
}

// El FOR UPDATE es el corazón de RN-13: serializa a los competidores sobre esta
// única fila. El segundo que llega queda esperando el commit del primero y
// recién entonces lee el disponible ya descontado, así que su rechazo es
// correcto y no una carrera ganada por azar.
const queryBloquearStock = queryStock + `
FOR UPDATE`

// BloquearStock lee la fila de stock del SKU y la bloquea hasta el fin de la
// transacción, o devuelve ErrNoEncontrado.
func (r *Repo) BloquearStock(ctx context.Context, tx Tx, sku string) (Stock, error) {
	var s Stock
	err := tx.pgxTx().QueryRow(ctx, queryBloquearStock, sku).
		Scan(&s.SKU, &s.Disponible, &s.Reservado, &s.Minimo)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stock{}, ErrNoEncontrado
	}
	if err != nil {
		return Stock{}, fmt.Errorf("bloquear stock: %w", err)
	}
	return s, nil
}

const queryInsertarReserva = `
INSERT INTO reservas (id, sku, cantidad, clave_idempotencia, estado)
VALUES ($1::uuid, $2, $3, $4, $5)
RETURNING creado_en`

// InsertarReserva graba la reserva dentro de la transacción. Si otra
// transacción ya usó la misma clave de idempotencia devuelve
// ErrClaveDuplicada, que es lo que vuelve segura la operación ante dos
// reintentos simultáneos (CA-13.2).
func (r *Repo) InsertarReserva(ctx context.Context, tx Tx, res *Reserva) error {
	err := tx.pgxTx().QueryRow(ctx, queryInsertarReserva,
		res.ID, res.SKU, res.Cantidad, res.ClaveIdempotencia, res.Estado).
		Scan(&res.CreadoEn)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == codigoViolacionUnica {
		return ErrClaveDuplicada
	}
	if err != nil {
		return fmt.Errorf("insertar reserva: %w", err)
	}
	return nil
}

// El CHECK (disponible >= 0) de la tabla es la última red: si esta resta
// dejara el disponible negativo, la base aborta la transacción.
const queryAplicarReserva = `
UPDATE stock_productos
SET disponible = disponible - $2,
    reservado  = reservado + $2,
    actualizado_en = now()
WHERE sku = $1`

// AplicarReserva descuenta las unidades del disponible y las suma al
// reservado, dentro de la misma transacción que bloqueó la fila.
func (r *Repo) AplicarReserva(ctx context.Context, tx Tx, sku string, cantidad int) error {
	tag, err := tx.pgxTx().Exec(ctx, queryAplicarReserva, sku, cantidad)
	if err != nil {
		return fmt.Errorf("aplicar reserva: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("aplicar reserva: se actualizaron %d filas", tag.RowsAffected())
	}
	return nil
}

// escanearReserva traduce pgx.ErrNoRows a ErrNoEncontrado: fuera de este
// paquete nadie conoce los errores de pgx.
func escanearReserva(fila pgx.Row) (Reserva, error) {
	var res Reserva
	err := fila.Scan(&res.ID, &res.SKU, &res.Cantidad, &res.ClaveIdempotencia,
		&res.Estado, &res.CreadoEn)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reserva{}, ErrNoEncontrado
	}
	if err != nil {
		return Reserva{}, fmt.Errorf("leer reserva: %w", err)
	}
	return res, nil
}
