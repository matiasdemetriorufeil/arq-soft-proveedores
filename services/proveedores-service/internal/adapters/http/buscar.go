package http

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

const (
	paginaPorDefecto  = 1
	tamanioPorDefecto = 20
	tamanioMaximo     = 50
)

// buscar es GET /api/v1/productos/buscar (ADR-006). Es una lectura derivada del
// índice: la capacidad publicada no depende de ella.
func (h *handler) buscar(w http.ResponseWriter, r *http.Request) {
	consulta, detalle := leerConsulta(r.URL.Query())
	if detalle != "" {
		escribirProblema(w, r, http.StatusBadRequest, CodigoParametroInvalido, detalle)
		return
	}
	if h.buscador == nil {
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"la búsqueda no está configurada")
		return
	}

	resultado, err := h.buscador.Buscar(r.Context(), consulta)
	switch {
	case err != nil && r.Context().Err() != nil:
		return // el cliente canceló la request
	case err != nil:
		LoggerDe(r.Context()).Error("falla al buscar productos", "error", err.Error())
		escribirProblema(w, r, http.StatusServiceUnavailable, CodigoNoDisponible,
			"la búsqueda no está disponible; reintentar más tarde")
		return
	}
	escribirJSON(w, http.StatusOK, resultado)
}

// leerConsulta valida los parámetros. Devuelve el detalle del primer error, o
// vacío si están todos bien.
func leerConsulta(q url.Values) (ports.ConsultaProductos, string) {
	c := ports.ConsultaProductos{
		Q:         q.Get("q"),
		Categoria: q.Get("categoria"),
		Proveedor: q.Get("proveedor"),
		Orden:     q.Get("orden"),
	}
	var err error
	if c.Pagina, err = enteroPositivo(q.Get("pagina"), paginaPorDefecto); err != nil {
		return c, "'pagina' " + err.Error()
	}
	c.Tamanio, err = enteroPositivo(q.Get("tamanio"), tamanioPorDefecto)
	if err == nil && c.Tamanio > tamanioMaximo {
		return c, "'tamanio' no puede ser mayor a " + strconv.Itoa(tamanioMaximo)
	}
	if err != nil {
		return c, "'tamanio' " + err.Error()
	}
	if c.PrecioMin, err = precioOpcional(q.Get("precio_min")); err != nil {
		return c, "'precio_min' " + err.Error()
	}
	if c.PrecioMax, err = precioOpcional(q.Get("precio_max")); err != nil {
		return c, "'precio_max' " + err.Error()
	}
	switch q.Get("con_stock") {
	case "", "false":
	case "true":
		c.ConStock = true
	default:
		return c, "'con_stock' debe ser true o false"
	}
	switch c.Orden {
	case "", ports.OrdenPrecioAsc, ports.OrdenPrecioDesc:
	default:
		return c, "'orden' debe ser " + ports.OrdenPrecioAsc + " o " + ports.OrdenPrecioDesc
	}
	return c, ""
}

func precioOpcional(v string) (*float64, error) {
	if v == "" {
		return nil, nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, errors.New("debe ser un número mayor o igual a 0")
	}
	return &n, nil
}
