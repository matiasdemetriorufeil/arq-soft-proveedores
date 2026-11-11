// Package fav es el cliente de los pedidos de FAV (contrato catalogo-pedidos
// v1.1.0): POST /v1/pedidos con Idempotency-Key, GET /v1/pedidos/{pedidoId} y
// la verificación de la firma del webhook pedidoActualizado (ADR-009).
//
// Es la capa anticorrupción de stock-service hacia FAV: el resto del servicio
// no conoce sus nombres de campo, sus códigos de error ni sus reintentos.
package fav

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// timeoutPorIntento es el que recomienda el contrato (ADR-009).
	timeoutPorIntento = 3 * time.Second
	// maxReintentos son los reintentos técnicos de un mismo envío, con la
	// misma Idempotency-Key y el mismo cuerpo: 4 intentos en total.
	maxReintentos = 3
	// esperaMaxima acota el Retry-After que pida FAV.
	esperaMaxima = 30 * time.Second
	maxRespuesta = 1 << 20
)

// MaxUnidadesPorVariante es el límite por variante de un pedido que FAV marca
// como propuesta en su contrato.
const MaxUnidadesPorVariante = 10

var esperas = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}

// Errores del cliente. El servicio decide con errors.Is.
var (
	// ErrNoDisponible: timeout, error de red, 429, 500 o 503 después de los
	// reintentos. El pedido se puede reintentar con la MISMA clave.
	ErrNoDisponible = errors.New("FAV no está disponible")
	// ErrNoAutenticado: FAV rechazó la clave del grupo (401). Es
	// configuración, no una caída: no se reintenta.
	ErrNoAutenticado = errors.New("FAV rechazó la clave del grupo")
	// ErrStockInsuficiente: 409 STOCK_INSUFICIENTE. No se apartó nada.
	ErrStockInsuficiente = errors.New("FAV no tiene stock suficiente")
	// ErrPedidoRechazado: un 400 o un 409 que no es de stock. Reintentar igual
	// daría lo mismo: el intento siguiente usa una clave nueva.
	ErrPedidoRechazado = errors.New("FAV rechazó el pedido")
	// ErrPedidoInexistente: 404 PEDIDO_INEXISTENTE.
	ErrPedidoInexistente = errors.New("FAV no conoce ese pedido")
	// ErrRespuestaInvalida: la respuesta no cumple el contrato. No pisa los
	// datos que ya teníamos.
	ErrRespuestaInvalida = errors.New("la respuesta de FAV no cumple su contrato")
)

// ErrorFAV lleva el detalle de un rechazo de FAV (status, código y detalle
// del problem+json) y se compara con los centinelas de arriba.
type ErrorFAV struct {
	Status  int
	Codigo  string
	Detalle string
	causa   error
}

func (e *ErrorFAV) Error() string {
	msg := fmt.Sprintf("%v (%d %s)", e.causa, e.Status, e.Codigo)
	if e.Detalle != "" {
		msg += ": " + e.Detalle
	}
	return msg
}

func (e *ErrorFAV) Unwrap() error { return e.causa }

// ItemPedido es una variante del pedido.
type ItemPedido struct {
	VarianteID string `json:"varianteId"`
	Cantidad   int    `json:"cantidad"`
}

// PedidoNuevo es el cuerpo de POST /v1/pedidos.
//
// Hasta la 1.1.0 llevaba también `notificacionUrl`, con el receptor de nuestro
// webhook. Su contrato **1.3.0 quitó ese campo** y su `PedidoNuevo` declara
// `additionalProperties: false`, así que mandarlo hoy hace que el pedido sea
// rechazado con `400 PEDIDO_INVALIDO`. Se saca: FAV ya no tiene webhook y el
// estado se sigue con `GET /v1/pedidos/{pedidoId}` (ADR-009).
type PedidoNuevo struct {
	ReferenciaExterna string       `json:"referenciaExterna,omitempty"`
	Items             []ItemPedido `json:"items"`
}

// Pedido es lo que FAV devuelve al crear o consultar un pedido. Solo están
// los campos que usamos: el resto se ignora, como pide el contrato.
type Pedido struct {
	PedidoID          string  `json:"pedidoId"`
	ReferenciaExterna string  `json:"referenciaExterna"`
	Estado            string  `json:"estado"`
	Total             float64 `json:"total"`
	Moneda            string  `json:"moneda"`
	Pago              struct {
		VenceEn time.Time `json:"venceEn"`
	} `json:"pago"`
}

// Cliente habla con FAV. Es seguro para uso concurrente.
type Cliente struct {
	base    string
	clave   string
	http    *http.Client
	timeout time.Duration
	dormir  func(context.Context, time.Duration) error
}

// NuevoCliente arma el cliente. `transport` puede ser nil; en el servicio es
// el de otelhttp, para que la llamada quede en la traza.
func NuevoCliente(base, clave string, transport http.RoundTripper) *Cliente {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Cliente{
		base:    strings.TrimRight(base, "/"),
		clave:   clave,
		http:    &http.Client{Transport: transport},
		timeout: timeoutPorIntento,
		dormir: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
}

// CrearPedido hace el pedido. Los reintentos técnicos usan la misma clave y
// exactamente el mismo cuerpo: si FAV lo había creado antes del timeout,
// devuelve el mismo pedido en lugar de crear otro (ADR-009).
func (c *Cliente) CrearPedido(ctx context.Context, clave string, p PedidoNuevo) (Pedido, error) {
	cuerpo, err := json.Marshal(p)
	if err != nil {
		return Pedido{}, err
	}
	status, b, err := c.hacer(ctx, http.MethodPost, "/v1/pedidos", cuerpo,
		map[string]string{"Idempotency-Key": clave, "Content-Type": "application/json"}, clave)
	if err != nil {
		return Pedido{}, err
	}
	if status != http.StatusCreated {
		return Pedido{}, rechazo(status, b)
	}
	return leerPedido(b)
}

// ConsultarPedido devuelve el estado vigente del pedido: es la fuente de
// verdad, por encima de lo que diga un aviso.
func (c *Cliente) ConsultarPedido(ctx context.Context, pedidoID string) (Pedido, error) {
	status, b, err := c.hacer(ctx, http.MethodGet, "/v1/pedidos/"+url.PathEscape(pedidoID), nil, nil, pedidoID)
	if err != nil {
		return Pedido{}, err
	}
	if status != http.StatusOK {
		return Pedido{}, rechazo(status, b)
	}
	return leerPedido(b)
}

// hacer manda la request con reintentos ante timeout, error de red, 429, 500
// y 503. Devuelve el status y el cuerpo de la última respuesta definitiva.
func (c *Cliente) hacer(ctx context.Context, metodo, ruta string, cuerpo []byte, headers map[string]string, correlacion string) (int, []byte, error) {
	var ultimo error
	for intento := 0; ; intento++ {
		status, b, espera, err := c.unIntento(ctx, metodo, ruta, cuerpo, headers, correlacion)
		if err == nil {
			return status, b, nil
		}
		ultimo = err
		if ctx.Err() != nil || intento == maxReintentos {
			return 0, nil, ultimo
		}
		if espera == 0 {
			espera = esperas[min(intento, len(esperas)-1)]
		}
		if err := c.dormir(ctx, espera); err != nil {
			return 0, nil, ultimo
		}
	}
}

// unIntento devuelve un error de ErrNoDisponible si conviene reintentar, con
// la espera que pidió FAV en Retry-After (0 si no pidió).
func (c *Cliente) unIntento(ctx context.Context, metodo, ruta string, cuerpo []byte, headers map[string]string, correlacion string) (int, []byte, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var body io.Reader
	if cuerpo != nil {
		body = bytes.NewReader(cuerpo)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+ruta, body)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("armar la request a FAV: %w", err)
	}
	req.Header.Set("X-API-Key", c.clave)
	req.Header.Set("Accept", "application/json")
	if correlacion != "" {
		// El mismo id en los logs de los dos sistemas.
		req.Header.Set("X-Correlation-Id", correlacion)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Timeout, conexión rechazada o cortada: todo se reintenta.
		return 0, nil, 0, fmt.Errorf("%w: %v", ErrNoDisponible, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRespuesta))
	if err != nil {
		return 0, nil, 0, fmt.Errorf("%w: leer la respuesta: %v", ErrNoDisponible, err)
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable,
		http.StatusBadGateway, http.StatusGatewayTimeout:
		return 0, nil, retryAfter(resp.Header.Get("Retry-After")),
			&ErrorFAV{Status: resp.StatusCode, Codigo: codigoDe(b), causa: ErrNoDisponible}
	}
	return resp.StatusCode, b, 0, nil
}

func retryAfter(v string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || s <= 0 {
		return 0
	}
	return min(time.Duration(s)*time.Second, esperaMaxima)
}

// rechazo traduce una respuesta definitiva que no es la esperada.
func rechazo(status int, cuerpo []byte) error {
	var p struct {
		Codigo string `json:"codigo"`
		Detail string `json:"detail"`
	}
	json.Unmarshal(cuerpo, &p)
	e := &ErrorFAV{Status: status, Codigo: p.Codigo, Detalle: p.Detail}
	switch {
	case status == http.StatusUnauthorized:
		e.causa = ErrNoAutenticado
	case status == http.StatusNotFound:
		e.causa = ErrPedidoInexistente
	case status == http.StatusConflict && p.Codigo == "STOCK_INSUFICIENTE":
		e.causa = ErrStockInsuficiente
	case status >= 400 && status < 500:
		e.causa = ErrPedidoRechazado
	default:
		e.causa = ErrRespuestaInvalida
	}
	return e
}

func codigoDe(cuerpo []byte) string {
	var p struct {
		Codigo string `json:"codigo"`
	}
	json.Unmarshal(cuerpo, &p)
	return p.Codigo
}

// leerPedido exige los campos que usamos; si falta alguno, la respuesta no
// cumple el contrato y no se usa.
func leerPedido(b []byte) (Pedido, error) {
	var p Pedido
	if err := json.Unmarshal(b, &p); err != nil {
		return Pedido{}, fmt.Errorf("%w: %v", ErrRespuestaInvalida, err)
	}
	if p.PedidoID == "" || p.Estado == "" || p.Moneda == "" || p.Pago.VenceEn.IsZero() {
		return Pedido{}, fmt.Errorf("%w: faltan pedidoId, estado, moneda o pago.venceEn", ErrRespuestaInvalida)
	}
	return p, nil
}
