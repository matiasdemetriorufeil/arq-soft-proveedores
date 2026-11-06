// Package meilisearch es el índice de productos (ADR-006). Habla con el motor
// por HTTP con la biblioteca estándar: se usan muy pocas rutas y no justifican un
// SDK con sus dependencias.
package meilisearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service/internal/ports"
)

const (
	// IndicePorDefecto es el nombre del índice de productos.
	IndicePorDefecto = "productos"
	sufijoTemporal   = "_tmp"
	// clavePrimaria no es el SKU: Meilisearch solo acepta letras, números, - y _
	// como id, y un documento rechazado haría fallar el lote entero. El SKU de un
	// proveedor externo no tiene por qué cumplir eso.
	clavePrimaria = "id"

	timeoutHTTP     = 10 * time.Second
	timeoutBusqueda = 2 * time.Second
	// timeoutTarea es cuánto se espera que el motor termine una tarea encolada.
	timeoutTarea = 2 * time.Minute
	esperaSondeo = 25 * time.Millisecond
	tamanioLote  = 1000
	// totalMaximo sube el tope de 1000 resultados que trae el motor, que si no
	// sería también el tope de `total`.
	totalMaximo = 1_000_000
)

// Cliente implementa ports.BuscadorProductos y ports.IndiceProductos.
type Cliente struct {
	base   string
	clave  string
	indice string
	http   *http.Client
}

var (
	_ ports.BuscadorProductos = (*Cliente)(nil)
	_ ports.IndiceProductos   = (*Cliente)(nil)
)

// NuevoCliente no se conecta todavía. Así el servicio arranca aunque el motor no
// esté.
func NuevoCliente(base, clave, indice string) *Cliente {
	if indice == "" {
		indice = IndicePorDefecto
	}
	return &Cliente{
		base:   strings.TrimRight(base, "/"),
		clave:  clave,
		indice: indice,
		http:   &http.Client{Timeout: timeoutHTTP},
	}
}

// errorAPI es una respuesta de error del motor.
type errorAPI struct {
	Status  int    `json:"-"`
	Mensaje string `json:"message"`
	Codigo  string `json:"code"`
}

func (e *errorAPI) Error() string {
	return fmt.Sprintf("meilisearch %d %s: %s", e.Status, e.Codigo, e.Mensaje)
}

func esCodigo(err error, codigo string) bool {
	var e *errorAPI
	return errors.As(err, &e) && e.Codigo == codigo
}

const (
	codigoIndiceNoExiste  = "index_not_found"
	codigoIndiceYaExiste  = "index_already_exists"
	estadoTareaExitosa    = "succeeded"
	estadoTareaFallida    = "failed"
	estadoTareaCancelada  = "canceled"
	rutaIndices           = "/indexes"
	rutaIntercambioIndice = "/swap-indexes"
)

func (c *Cliente) hacer(ctx context.Context, metodo, ruta string, cuerpo, destino any) error {
	var lector io.Reader
	if cuerpo != nil {
		crudo, err := json.Marshal(cuerpo)
		if err != nil {
			return fmt.Errorf("serializar pedido a meilisearch: %w", err)
		}
		lector = bytes.NewReader(crudo)
	}
	req, err := http.NewRequestWithContext(ctx, metodo, c.base+ruta, lector)
	if err != nil {
		return fmt.Errorf("armar pedido a meilisearch: %w", err)
	}
	if c.clave != "" {
		req.Header.Set("Authorization", "Bearer "+c.clave)
	}
	if cuerpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("meilisearch no responde: %w", err)
	}
	defer res.Body.Close()
	crudo, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("leer respuesta de meilisearch: %w", err)
	}
	if res.StatusCode >= 300 {
		e := &errorAPI{Status: res.StatusCode}
		_ = json.Unmarshal(crudo, e)
		return e
	}
	if destino != nil {
		if err := json.Unmarshal(crudo, destino); err != nil {
			return fmt.Errorf("respuesta de meilisearch ilegible: %w", err)
		}
	}
	return nil
}

// tarea hace un pedido que el motor encola, y espera a que termine.
func (c *Cliente) tarea(ctx context.Context, metodo, ruta string, cuerpo any) error {
	var encolada struct {
		UID int64 `json:"taskUid"`
	}
	if err := c.hacer(ctx, metodo, ruta, cuerpo, &encolada); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutTarea)
	defer cancel()
	for {
		var t struct {
			Estado string    `json:"status"`
			Error  *errorAPI `json:"error"`
		}
		if err := c.hacer(ctx, http.MethodGet, "/tasks/"+strconv.FormatInt(encolada.UID, 10), nil, &t); err != nil {
			return err
		}
		switch t.Estado {
		case estadoTareaExitosa:
			return nil
		case estadoTareaFallida, estadoTareaCancelada:
			if t.Error != nil {
				return t.Error
			}
			return fmt.Errorf("tarea %d de meilisearch: %s", encolada.UID, t.Estado)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("esperando la tarea %d de meilisearch: %w", encolada.UID, ctx.Err())
		case <-time.After(esperaSondeo):
		}
	}
}

// IndiceCargado dice si el índice existe y tiene documentos. Un índice vacío
// cuenta como no cargado porque es lo que deja una reconstrucción que se cortó:
// el intercambio necesita que los dos índices existan.
func (c *Cliente) IndiceCargado(ctx context.Context) (bool, error) {
	var stats struct {
		Documentos int64 `json:"numberOfDocuments"`
	}
	err := c.hacer(ctx, http.MethodGet, rutaIndices+"/"+c.indice+"/stats", nil, &stats)
	if esCodigo(err, codigoIndiceNoExiste) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return stats.Documentos > 0, nil
}

// documento es lo que se guarda: el documento de ADR-006 más su id.
type documento struct {
	ID string `json:"id"`
	ports.DocumentoProducto
}

// idDeDocumento es determinístico: reindexar un SKU reemplaza su documento.
func idDeDocumento(sku string) string {
	suma := sha256.Sum256([]byte(sku))
	return hex.EncodeToString(suma[:16])
}

// Indexar crea o reemplaza esos documentos. El índice ya tiene que existir: si
// no, el motor lo crearía solo y sin la configuración de filtros y orden.
func (c *Cliente) Indexar(ctx context.Context, docs []ports.DocumentoProducto) error {
	return c.cargar(ctx, c.indice, docs)
}

func (c *Cliente) cargar(ctx context.Context, indice string, docs []ports.DocumentoProducto) error {
	for inicio := 0; inicio < len(docs); inicio += tamanioLote {
		lote := docs[inicio:min(inicio+tamanioLote, len(docs))]
		cuerpo := make([]documento, len(lote))
		for i, d := range lote {
			if d.Proveedores == nil {
				d.Proveedores = []string{}
			}
			cuerpo[i] = documento{ID: idDeDocumento(d.SKU), DocumentoProducto: d}
		}
		ruta := rutaIndices + "/" + indice + "/documents?primaryKey=" + clavePrimaria
		if err := c.tarea(ctx, http.MethodPost, ruta, cuerpo); err != nil {
			return fmt.Errorf("cargar documentos en %s: %w", indice, err)
		}
	}
	return nil
}

// ajustes es la configuración del índice (ADR-006). "sort" va primero para que
// pedir un orden por precio ordene de verdad también con texto; sin `sort` en la
// consulta, esa regla no hace nada y queda la relevancia.
var ajustes = map[string]any{
	"searchableAttributes": []string{"nombre", "sku", "proveedores"},
	"filterableAttributes": []string{"categoria", "proveedores", "precio_min", "proveedores_con_stock"},
	"sortableAttributes":   []string{"precio_min"},
	"rankingRules":         []string{"sort", "words", "typo", "proximity", "attribute", "exactness"},
	"pagination":           map[string]int{"maxTotalHits": totalMaximo},
}

func (c *Cliente) crearConfigurado(ctx context.Context, indice string) error {
	err := c.tarea(ctx, http.MethodPost, rutaIndices, map[string]string{"uid": indice, "primaryKey": clavePrimaria})
	if err != nil && !esCodigo(err, codigoIndiceYaExiste) {
		return fmt.Errorf("crear índice %s: %w", indice, err)
	}
	if err := c.tarea(ctx, http.MethodPatch, rutaIndices+"/"+indice+"/settings", ajustes); err != nil {
		return fmt.Errorf("configurar índice %s: %w", indice, err)
	}
	return nil
}

func (c *Cliente) borrar(ctx context.Context, indice string) error {
	err := c.tarea(ctx, http.MethodDelete, rutaIndices+"/"+indice, nil)
	if err != nil && !esCodigo(err, codigoIndiceNoExiste) {
		return fmt.Errorf("borrar índice %s: %w", indice, err)
	}
	return nil
}

// Reconstruir carga un índice temporal y lo intercambia con el vigente, así la
// búsqueda sigue respondiendo con los datos viejos hasta el último momento.
func (c *Cliente) Reconstruir(ctx context.Context, docs []ports.DocumentoProducto) error {
	temporal := c.indice + sufijoTemporal
	if err := c.borrar(ctx, temporal); err != nil { // el de una corrida que se cortó
		return err
	}
	if err := c.crearConfigurado(ctx, temporal); err != nil {
		return err
	}
	if err := c.cargar(ctx, temporal, docs); err != nil {
		return err
	}
	// El intercambio exige que el vigente exista.
	if err := c.crearConfigurado(ctx, c.indice); err != nil {
		return err
	}
	intercambio := []map[string][]string{{"indexes": {c.indice, temporal}}}
	if err := c.tarea(ctx, http.MethodPost, rutaIntercambioIndice, intercambio); err != nil {
		return fmt.Errorf("intercambiar índices: %w", err)
	}
	return c.borrar(ctx, temporal)
}

// entreComillas deja un valor como literal de la expresión de filtro: sin esto,
// un parámetro podría agregar condiciones.
func entreComillas(valor string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(valor) + `"`
}

// armarFiltro devuelve una condición por elemento: el motor las combina con AND.
func armarFiltro(c ports.ConsultaProductos) []string {
	filtro := []string{}
	if c.Categoria != "" {
		filtro = append(filtro, "categoria = "+entreComillas(c.Categoria))
	}
	if c.Proveedor != "" {
		filtro = append(filtro, "proveedores = "+entreComillas(c.Proveedor))
	}
	if c.PrecioMin != nil {
		filtro = append(filtro, "precio_min >= "+strconv.FormatFloat(*c.PrecioMin, 'f', -1, 64))
	}
	if c.PrecioMax != nil {
		filtro = append(filtro, "precio_min <= "+strconv.FormatFloat(*c.PrecioMax, 'f', -1, 64))
	}
	if c.ConStock {
		filtro = append(filtro, "proveedores_con_stock > 0")
	}
	return filtro
}

// Buscar consulta el índice. Cualquier error indica que el motor no está
// disponible: el handler lo traduce a 503.
func (c *Cliente) Buscar(ctx context.Context, consulta ports.ConsultaProductos) (ports.ResultadoProductos, error) {
	ctx, span := otel.Tracer("proveedores-service/meilisearch").Start(ctx, "meilisearch buscar")
	defer span.End()
	ctx, cancel := context.WithTimeout(ctx, timeoutBusqueda)
	defer cancel()

	cuerpo := map[string]any{
		"q":           consulta.Q,
		"page":        consulta.Pagina,
		"hitsPerPage": consulta.Tamanio,
		"filter":      armarFiltro(consulta),
	}
	switch consulta.Orden {
	case ports.OrdenPrecioAsc:
		cuerpo["sort"] = []string{"precio_min:asc"}
	case ports.OrdenPrecioDesc:
		cuerpo["sort"] = []string{"precio_min:desc"}
	}

	var respuesta struct {
		Hits  []ports.DocumentoProducto `json:"hits"`
		Total int                       `json:"totalHits"`
	}
	ruta := rutaIndices + "/" + c.indice + "/search"
	if err := c.hacer(ctx, http.MethodPost, ruta, cuerpo, &respuesta); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "búsqueda fallida")
		return ports.ResultadoProductos{}, err
	}
	if respuesta.Hits == nil {
		respuesta.Hits = []ports.DocumentoProducto{}
	}
	return ports.ResultadoProductos{
		Resultados: respuesta.Hits,
		Total:      respuesta.Total,
		Pagina:     consulta.Pagina,
		Tamanio:    consulta.Tamanio,
	}, nil
}
