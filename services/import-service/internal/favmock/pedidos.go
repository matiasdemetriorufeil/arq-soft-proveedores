package favmock

// Pedidos de compra del contrato de FAV v1.1.0: POST /v1/pedidos,
// GET /v1/pedidos/{pedidoId} y el webhook pedidoActualizado firmado con
// HMAC-SHA256. A diferencia del mock que publica FAV, este guarda estado: un
// pedido creado se puede consultar, y cambiarle el estado con
// POST /_mock/pedidos/{pedidoId}/estado envía el aviso a su notificacionUrl.
// Así se prueba de punta a punta la orden de reposición de stock-service.

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Estados del pedido según el contrato.
const (
	EstadoPendienteDePago = "PENDIENTE_DE_PAGO"
	EstadoPagado          = "PAGADO"
	EstadoVencido         = "VENCIDO"
	EstadoAVerificar      = "A_VERIFICAR"
)

// Límites que el contrato marca como propuesta de FAV.
const (
	maxItemsPorPedido   = 20
	maxUnidadesVariante = 10
	vigenciaPago        = 48 * time.Hour
)

// Los estados solo avanzan (contrato, callback pedidoActualizado).
var avancesPermitidos = map[string][]string{
	EstadoPendienteDePago: {EstadoPagado, EstadoVencido},
	EstadoVencido:         {EstadoAVerificar},
}

type itemPedido struct {
	VarianteID string `json:"varianteId"`
	Cantidad   int    `json:"cantidad"`
}

type pedidoNuevo struct {
	ReferenciaExterna string       `json:"referenciaExterna,omitempty"`
	NotificacionURL   string       `json:"notificacionUrl,omitempty"`
	Items             []itemPedido `json:"items"`
}

type itemConPrecio struct {
	VarianteID     string  `json:"varianteId"`
	Cantidad       int     `json:"cantidad"`
	PrecioUnitario float64 `json:"precioUnitario"`
	Subtotal       float64 `json:"subtotal"`
}

type datosTransferencia struct {
	Alias   string `json:"alias"`
	Titular string `json:"titular"`
}

type pago struct {
	Medio              string             `json:"medio"`
	VenceEn            string             `json:"venceEn"`
	DatosTransferencia datosTransferencia `json:"datosTransferencia"`
}

type pedido struct {
	PedidoID          string          `json:"pedidoId"`
	ReferenciaExterna string          `json:"referenciaExterna,omitempty"`
	Estado            string          `json:"estado"`
	Items             []itemConPrecio `json:"items"`
	Total             float64         `json:"total"`
	Moneda            string          `json:"moneda"`
	Pago              pago            `json:"pago"`
	NotificacionURL   string          `json:"notificacionUrl,omitempty"`
	CreadoEn          string          `json:"creadoEn"`
	ActualizadoEn     string          `json:"actualizadoEn"`
}

// eventoPedido es el cuerpo del webhook pedidoActualizado.
type eventoPedido struct {
	EventoID   string `json:"eventoId"`
	Tipo       string `json:"tipo"`
	OcurridoEn string `json:"ocurridoEn"`
	Pedido     struct {
		PedidoID          string `json:"pedidoId"`
		ReferenciaExterna string `json:"referenciaExterna,omitempty"`
		Estado            string `json:"estado"`
	} `json:"pedido"`
}

// respuestaGuardada es lo que se devuelve igual ante la misma Idempotency-Key
// con el mismo cuerpo.
type respuestaGuardada struct {
	huella      [32]byte
	status      int
	contentType string
	cuerpo      []byte
}

// AvisoEnviado registra un webhook enviado, para verlo en /_mock/estado.
type AvisoEnviado struct {
	EventoID string `json:"eventoId"`
	PedidoID string `json:"pedidoId"`
	Estado   string `json:"estado"`
	URL      string `json:"url"`
	Status   int    `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
}

type errorCampo struct {
	Campo   string `json:"campo"`
	Mensaje string `json:"mensaje"`
}

// FirmarAviso arma el header X-FAV-Firma: `t=<t>,v1=<HMAC-SHA256 en hex de
// "<t>.<cuerpo>">`, con el cuerpo crudo tal cual se envía.
func FirmarAviso(secreto string, t int64, cuerpo []byte) string {
	mac := hmac.New(sha256.New, []byte(secreto))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(cuerpo)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}

func (m *Mock) crearPedido(w http.ResponseWriter, r *http.Request) {
	ponerCorrelacion(w, r)
	if !m.autenticado(r) {
		problema(w, http.StatusUnauthorized, "no-autenticado", "No autenticado", "NO_AUTENTICADO")
		return
	}
	m.mu.Lock()
	modo := m.modo
	m.mu.Unlock()
	if modo == ModoInvalido {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"pedido":"esto no es el contrato"}`))
		return
	}
	if !aplicarModo(w, r, modo) {
		return
	}

	clave := r.Header.Get("Idempotency-Key")
	if n := len(clave); n < 8 || n > 100 {
		escribir(w, armarProblema(http.StatusBadRequest, "pedido-invalido", "Pedido inválido", "PEDIDO_INVALIDO",
			"Falta el header Idempotency-Key o no tiene entre 8 y 100 caracteres.",
			map[string]any{"errores": []errorCampo{{"Idempotency-Key", "obligatorio, entre 8 y 100 caracteres"}}}))
		return
	}
	cuerpo, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		escribir(w, armarProblema(http.StatusBadRequest, "pedido-invalido", "Pedido inválido", "PEDIDO_INVALIDO",
			"El cuerpo es demasiado grande.", nil))
		return
	}
	huella := sha256.Sum256(cuerpo)

	m.mu.Lock()
	defer m.mu.Unlock()
	if previa, ok := m.idem[clave]; ok {
		if previa.huella != huella {
			escribir(w, armarProblema(http.StatusConflict, "clave-idempotencia-reutilizada",
				"Clave de idempotencia reutilizada", "CLAVE_IDEMPOTENCIA_REUTILIZADA",
				"La Idempotency-Key ya se usó con un pedido distinto.", nil))
			return
		}
		escribir(w, previa)
		return
	}
	resp := m.procesarPedido(cuerpo)
	resp.huella = huella
	m.idem[clave] = resp
	escribir(w, resp)
}

// procesarPedido valida y crea el pedido. Se llama con m.mu tomado.
func (m *Mock) procesarPedido(cuerpo []byte) respuestaGuardada {
	var nuevo pedidoNuevo
	dec := json.NewDecoder(bytes.NewReader(cuerpo))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&nuevo); err != nil {
		return armarProblema(http.StatusBadRequest, "pedido-invalido", "Pedido inválido", "PEDIDO_INVALIDO",
			"El cuerpo no es un pedido válido.", map[string]any{"errores": []errorCampo{{"cuerpo", err.Error()}}})
	}
	if errs := validarPedido(nuevo); len(errs) > 0 {
		return armarProblema(http.StatusBadRequest, "pedido-invalido", "Pedido inválido", "PEDIDO_INVALIDO",
			"El pedido no cumple el contrato.", map[string]any{"errores": errs})
	}

	precios := map[string]float64{}
	for _, p := range m.catalogo {
		for _, v := range p.Variantes {
			precios[v.VarianteID] = v.Precio
		}
	}
	var inexistentes []string
	for _, it := range nuevo.Items {
		if _, ok := precios[it.VarianteID]; !ok {
			inexistentes = append(inexistentes, it.VarianteID)
		}
	}
	if len(inexistentes) > 0 {
		return armarProblema(http.StatusBadRequest, "variante-inexistente", "Variante inexistente", "VARIANTE_INEXISTENTE",
			"Una o más variantes no existen o no están publicadas.", map[string]any{"variantes": inexistentes})
	}

	// Todo o nada: si una variante no alcanza, no se aparta ninguna.
	var faltantes []map[string]any
	for _, it := range nuevo.Items {
		if m.stock[it.VarianteID] < it.Cantidad {
			faltantes = append(faltantes, map[string]any{"varianteId": it.VarianteID, "solicitada": it.Cantidad})
		}
	}
	if len(faltantes) > 0 {
		return armarProblema(http.StatusConflict, "stock-insuficiente", "Stock insuficiente", "STOCK_INSUFICIENTE",
			"Al menos una variante no alcanza. No se apartó ninguna unidad.", map[string]any{"faltantes": faltantes})
	}

	ahora := time.Now().UTC().Truncate(time.Second)
	p := &pedido{
		PedidoID: nuevoUUID(), ReferenciaExterna: nuevo.ReferenciaExterna, Estado: EstadoPendienteDePago,
		Moneda: "ARS", NotificacionURL: nuevo.NotificacionURL,
		Pago: pago{Medio: "TRANSFERENCIA", VenceEn: ahora.Add(vigenciaPago).Format(time.RFC3339),
			DatosTransferencia: datosTransferencia{Alias: "FAV.MOCK.ALIAS", Titular: "FAV (mock)"}},
		CreadoEn: ahora.Format(time.RFC3339), ActualizadoEn: ahora.Format(time.RFC3339),
	}
	for _, it := range nuevo.Items {
		m.stock[it.VarianteID] -= it.Cantidad
		sub := precios[it.VarianteID] * float64(it.Cantidad)
		p.Items = append(p.Items, itemConPrecio{it.VarianteID, it.Cantidad, precios[it.VarianteID], sub})
		p.Total += sub
	}
	m.compras[p.PedidoID] = p
	out, _ := json.Marshal(p)
	return respuestaGuardada{status: http.StatusCreated, contentType: "application/json", cuerpo: out}
}

func validarPedido(p pedidoNuevo) []errorCampo {
	var errs []errorCampo
	if len(p.ReferenciaExterna) > 100 {
		errs = append(errs, errorCampo{"referenciaExterna", "hasta 100 caracteres"})
	}
	if p.NotificacionURL != "" {
		u, err := url.Parse(p.NotificacionURL)
		if err != nil || len(p.NotificacionURL) > 500 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, errorCampo{"notificacionUrl", "tiene que ser una URL http o https de hasta 500 caracteres"})
		}
	}
	if len(p.Items) < 1 || len(p.Items) > maxItemsPorPedido {
		errs = append(errs, errorCampo{"items", fmt.Sprintf("entre 1 y %d variantes", maxItemsPorPedido)})
	}
	vistas := map[string]bool{}
	for i, it := range p.Items {
		if strings.TrimSpace(it.VarianteID) == "" {
			errs = append(errs, errorCampo{fmt.Sprintf("items[%d].varianteId", i), "obligatorio"})
		}
		if it.Cantidad < 1 || it.Cantidad > maxUnidadesVariante {
			errs = append(errs, errorCampo{fmt.Sprintf("items[%d].cantidad", i), fmt.Sprintf("debe estar entre 1 y %d", maxUnidadesVariante)})
		}
		if vistas[it.VarianteID] {
			errs = append(errs, errorCampo{fmt.Sprintf("items[%d].varianteId", i), "una variante no puede repetirse"})
		}
		vistas[it.VarianteID] = true
	}
	return errs
}

func (m *Mock) consultarPedido(w http.ResponseWriter, r *http.Request) {
	ponerCorrelacion(w, r)
	if !m.autenticado(r) {
		problema(w, http.StatusUnauthorized, "no-autenticado", "No autenticado", "NO_AUTENTICADO")
		return
	}
	m.mu.Lock()
	modo := m.modo
	m.mu.Unlock()
	if modo == ModoInvalido {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pedido":"esto no es el contrato"}`))
		return
	}
	if !aplicarModo(w, r, modo) {
		return
	}
	m.mu.Lock()
	p, ok := m.compras[r.PathValue("pedidoId")]
	var out []byte
	if ok {
		out, _ = json.Marshal(p)
	}
	m.mu.Unlock()
	if !ok {
		escribir(w, armarProblema(http.StatusNotFound, "pedido-inexistente", "Pedido inexistente", "PEDIDO_INEXISTENTE", "", nil))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// cambiarEstadoPedido simula lo que en FAV hace el administrador (verificar la
// transferencia) o el paso del tiempo (vencimiento), y envía el webhook.
//
//	POST /_mock/pedidos/{pedidoId}/estado {"estado":"PAGADO"}
//
// Opciones para el Game Day: "duplicar": true envía el mismo aviso dos veces
// (el receptor tiene que deduplicarlo) y "firmaInvalida": true lo firma con
// otro secreto (el receptor tiene que responder 401).
func (m *Mock) cambiarEstadoPedido(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Estado        string `json:"estado"`
		Duplicar      bool   `json:"duplicar"`
		FirmaInvalida bool   `json:"firmaInvalida"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `cuerpo esperado: {"estado":"PAGADO"} (PAGADO, VENCIDO o A_VERIFICAR)`, http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	p, ok := m.compras[r.PathValue("pedidoId")]
	if !ok {
		m.mu.Unlock()
		http.Error(w, "pedido inexistente", http.StatusNotFound)
		return
	}
	permitido := false
	for _, e := range avancesPermitidos[p.Estado] {
		permitido = permitido || e == body.Estado
	}
	if !permitido {
		desde := p.Estado
		m.mu.Unlock()
		http.Error(w, fmt.Sprintf("desde %s no se puede pasar a %q", desde, body.Estado), http.StatusConflict)
		return
	}
	ahora := time.Now().UTC().Truncate(time.Second)
	p.Estado = body.Estado
	p.ActualizadoEn = ahora.Format(time.RFC3339)
	if body.Estado == EstadoVencido {
		// Vencido sin pago: el stock apartado se libera.
		for _, it := range p.Items {
			m.stock[it.VarianteID] += it.Cantidad
		}
	}
	secreto := m.secreto
	if body.FirmaInvalida {
		secreto = "otro-secreto"
	}
	destino, refExt, pedidoJSON := p.NotificacionURL, p.ReferenciaExterna, mustJSON(p)
	m.mu.Unlock()

	enviados := []AvisoEnviado{}
	if destino != "" {
		ev := eventoPedido{EventoID: nuevoUUID(), Tipo: "pedido.actualizado", OcurridoEn: ahora.Format(time.RFC3339)}
		ev.Pedido.PedidoID, ev.Pedido.ReferenciaExterna, ev.Pedido.Estado = r.PathValue("pedidoId"), refExt, body.Estado
		aviso := mustJSON(ev)
		veces := 1
		if body.Duplicar {
			veces = 2
		}
		for range veces {
			enviados = append(enviados, m.enviarAviso(destino, secreto, ev.EventoID, ev.Pedido.PedidoID, body.Estado, aviso))
		}
	}
	m.mu.Lock()
	m.avisos = append(m.avisos, enviados...)
	if len(m.avisos) > 20 {
		m.avisos = m.avisos[len(m.avisos)-20:]
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"pedido":%s,"avisos":%s}`, pedidoJSON, mustJSON(enviados))
}

// enviarAviso hace una sola entrega del webhook. FAV reintenta a los 1, 5, 15
// y 60 minutos; el mock no, para que la demo sea predecible: para reintentar,
// se repite el aviso con "duplicar".
func (m *Mock) enviarAviso(destino, secreto, eventoID, pedidoID, estado string, cuerpo []byte) AvisoEnviado {
	reg := AvisoEnviado{EventoID: eventoID, PedidoID: pedidoID, Estado: estado, URL: destino}
	req, err := http.NewRequest(http.MethodPost, destino, bytes.NewReader(cuerpo))
	if err != nil {
		reg.Error = err.Error()
		return reg
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-FAV-Firma", FirmarAviso(secreto, time.Now().Unix(), cuerpo))
	req.Header.Set("X-FAV-Evento-Id", eventoID)
	req.Header.Set("X-Correlation-Id", "mock-aviso-"+eventoID)
	resp, err := m.clienteW.Do(req)
	if err != nil {
		reg.Error = err.Error()
		return reg
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	reg.Status = resp.StatusCode
	return reg
}

// cambiarStock fija el stock de una variante, para forzar un 409
// STOCK_INSUFICIENTE en la demo: POST /_mock/stock {"varianteId":"...","stock":0}.
func (m *Mock) cambiarStock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		VarianteID string `json:"varianteId"`
		Stock      *int   `json:"stock"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Stock == nil || *body.Stock < 0 {
		http.Error(w, `cuerpo esperado: {"varianteId":"...","stock":0}`, http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.stock[body.VarianteID]; !ok {
		http.Error(w, "variante inexistente", http.StatusNotFound)
		return
	}
	m.stock[body.VarianteID] = *body.Stock
	w.WriteHeader(http.StatusNoContent)
}

func (m *Mock) autenticado(r *http.Request) bool {
	k := r.Header.Get("X-API-Key")
	return k != "" && k == m.clave
}

func ponerCorrelacion(w http.ResponseWriter, r *http.Request) {
	corr := r.Header.Get("X-Correlation-Id")
	if corr == "" {
		corr = "mock-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	w.Header().Set("X-Correlation-Id", corr)
}

func armarProblema(status int, tipo, titulo, codigo, detalle string, extra map[string]any) respuestaGuardada {
	p := map[string]any{"type": "https://fav.example/errores/" + tipo, "title": titulo, "status": status, "codigo": codigo}
	if detalle != "" {
		p["detail"] = detalle
	}
	for k, v := range extra {
		p[k] = v
	}
	return respuestaGuardada{status: status, contentType: "application/problem+json", cuerpo: mustJSON(p)}
}

func escribir(w http.ResponseWriter, r respuestaGuardada) {
	w.Header().Set("Content-Type", r.contentType)
	w.WriteHeader(r.status)
	w.Write(r.cuerpo)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // solo tipos propios, siempre serializables
	}
	return b
}

func nuevoUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
