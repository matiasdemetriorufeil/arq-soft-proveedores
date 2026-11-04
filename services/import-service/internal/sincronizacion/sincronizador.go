package sincronizacion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/matiasdemetriorufeil/arq-soft-proveedores/pkg/mensajeria"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/dominio"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/fav"
	"github.com/matiasdemetriorufeil/arq-soft-proveedores/services/import-service/internal/resiliencia"
)

// VigenciaMaxima: pasado este tiempo sin sincronizar, las ofertas de FAV se
// retiran del ranking para no recomendar precios viejos (ADR-009).
const VigenciaMaxima = 24 * time.Hour

// Config del sincronizador.
type Config struct {
	Proveedor mensajeria.ProveedorEvento
	Reglas    dominio.Reglas
	Fuente    FuenteCatalogo
	Breaker   *resiliencia.Breaker
	Almacen   Almacen
	Pub       Publicador
	Logger    *slog.Logger
	// Ahora puede ser nil (usa time.Now). Los tests lo fijan para probar las
	// 24 horas sin esperarlas.
	Ahora func() time.Time
}

// Sincronizador corre una sincronización por vez: el ciclo periódico y el
// disparo manual comparten el mismo candado.
type Sincronizador struct {
	cfg Config
	mu  sync.Mutex
	// pubMu serializa la publicación del outbox: la llaman la sincronización
	// y el ciclo de 15 s, y si corrieran a la vez publicarían dos veces el
	// mismo evento.
	pubMu sync.Mutex
}

// Nuevo arma el sincronizador.
func Nuevo(cfg Config) *Sincronizador {
	if cfg.Ahora == nil {
		cfg.Ahora = time.Now
	}
	return &Sincronizador{cfg: cfg}
}

// Resumen es lo que devuelve una sincronización (y lo que muestra
// POST /sincronizar).
type Resumen struct {
	ImportacionID string `json:"importacion_id"`
	Estado        string `json:"estado"`
	Error         string `json:"error,omitempty"`
	Variantes     int    `json:"variantes"`
	Descartadas   int    `json:"descartadas"`
	Altas         int    `json:"altas"`
	Modificadas   int    `json:"modificadas"`
	Bajas         int    `json:"bajas"`
	Publicados    int    `json:"eventos_publicados"`
	Pendientes    int64  `json:"eventos_pendientes"`
}

var tracer = otel.Tracer("import-service/sincronizacion")

// Sincronizar hace una vuelta completa. Solo devuelve error si no pudo guardar
// en la base; que FAV esté caído es un resultado, no un error.
func (s *Sincronizador) Sincronizar(ctx context.Context) (Resumen, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, span := tracer.Start(ctx, "sincronizar catalogo FAV")
	defer span.End()

	inicio := s.cfg.Ahora().UTC()
	imp := Importacion{ID: mensajeria.NuevoID(), Proveedor: s.cfg.Proveedor.Codigo, IniciadaEn: inicio}
	span.SetAttributes(attribute.String("importacion.id", imp.ID))

	var res fav.Resultado
	err := s.cfg.Breaker.Ejecutar(func() error {
		var e error
		res, e = s.cfg.Fuente.ListarProductos(ctx, imp.ID)
		return e
	}, func(e error) bool { return errors.Is(e, fav.ErrNoAutenticado) })

	var resumen Resumen
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		resumen, err = s.registrarFalla(ctx, imp, err)
	} else {
		imp.Intentos, imp.CorrelationID = res.Intentos, res.CorrelationID
		resumen, err = s.registrarCatalogo(ctx, imp, res)
	}
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return Resumen{}, err
	}

	resumen.Publicados = s.PublicarPendientes(ctx)
	if n, err := s.cfg.Almacen.ContarPendientes(ctx); err == nil {
		resumen.Pendientes = n
	}
	span.SetAttributes(
		attribute.String("importacion.estado", resumen.Estado),
		attribute.Int("importacion.altas", resumen.Altas),
		attribute.Int("importacion.modificadas", resumen.Modificadas),
		attribute.Int("importacion.bajas", resumen.Bajas),
	)
	s.cfg.Logger.InfoContext(ctx, "sincronización del catálogo de FAV terminada",
		"importacion_id", resumen.ImportacionID, "estado", resumen.Estado,
		"altas", resumen.Altas, "modificadas", resumen.Modificadas, "bajas", resumen.Bajas,
		"descartadas", resumen.Descartadas, "publicados", resumen.Publicados,
		"pendientes", resumen.Pendientes, "error", resumen.Error)
	return resumen, nil
}

func (s *Sincronizador) registrarCatalogo(ctx context.Context, imp Importacion, res fav.Resultado) (Resumen, error) {
	norm := dominio.Normalizar(res.Catalogo, s.cfg.Reglas)
	imp.Variantes, imp.Descartes = norm.Variantes, norm.Descartes
	imp.TerminadaEn = s.cfg.Ahora().UTC()

	previa, hayPrevia, err := s.cfg.Almacen.UltimaVigente(ctx)
	if err != nil {
		return Resumen{}, fmt.Errorf("leer la última importación vigente: %w", err)
	}
	anterior := map[string]dominio.Item{}
	if hayPrevia {
		for _, it := range previa.Vigentes {
			anterior[it.SKU] = it
		}
	}
	// Completa: el consumidor no tiene nada de FAV (primera vez, o después de
	// un vencimiento), así que se le manda el catálogo entero.
	completa := !hayPrevia || previa.Estado == EstadoVencido
	cambios := dominio.Comparar(anterior, norm.Items)
	imp.Altas, imp.Modific, imp.Bajas = len(cambios.Altas), len(cambios.Modificadas), len(cambios.Bajas)

	if cambios.Vacio() && !completa {
		imp.Estado = EstadoSinCambios
		if err := s.cfg.Almacen.Guardar(ctx, imp); err != nil {
			return Resumen{}, fmt.Errorf("guardar importación: %w", err)
		}
		return resumenDe(imp), nil
	}

	imp.Estado = EstadoConCambios
	imp.Crudo = res.Crudo
	imp.Secuencia = previa.Secuencia + 1
	imp.Vigentes = ordenados(norm.Items)

	datos := mensajeria.ListaPreciosActualizada{
		Proveedor:     s.cfg.Proveedor,
		ImportacionID: imp.ID,
		Secuencia:     imp.Secuencia,
		Motivo:        mensajeria.MotivoSincronizacion,
		Completa:      completa,
		Productos:     []mensajeria.ProductoEvento{},
		Ofertas:       []mensajeria.OfertaEvento{},
		Bajas:         cambios.Bajas,
	}
	if datos.Bajas == nil {
		datos.Bajas = []string{}
	}
	cambiadas := append(append([]dominio.Item{}, cambios.Altas...), cambios.Modificadas...)
	if completa {
		cambiadas = imp.Vigentes
	}
	for _, it := range cambiadas {
		if completa || cambios.ProductosCambiados[it.SKU] {
			datos.Productos = append(datos.Productos, producto(it))
		}
		datos.Ofertas = append(datos.Ofertas, oferta(it))
	}
	if err := s.guardarConEvento(ctx, &imp, datos); err != nil {
		return Resumen{}, err
	}
	return resumenDe(imp), nil
}

func (s *Sincronizador) registrarFalla(ctx context.Context, imp Importacion, causa error) (Resumen, error) {
	imp.Estado, imp.Error = EstadoError, causa.Error()
	imp.TerminadaEn = s.cfg.Ahora().UTC()
	s.cfg.Logger.WarnContext(ctx, "no se pudo bajar el catálogo de FAV; siguen valiendo las últimas ofertas",
		"importacion_id", imp.ID, "error", causa.Error(), "breaker", string(s.cfg.Breaker.Estado()))
	if err := s.cfg.Almacen.Guardar(ctx, imp); err != nil {
		return Resumen{}, fmt.Errorf("guardar importación fallida: %w", err)
	}
	resumen := resumenDe(imp)

	vencida, err := s.retirarSiVencio(ctx)
	if err != nil {
		return Resumen{}, err
	}
	if vencida != nil {
		resumen.Bajas = vencida.Bajas
	}
	return resumen, nil
}

// retirarSiVencio genera el evento que saca a FAV del ranking si la última
// sincronización buena tiene más de VigenciaMaxima. Lo hace una sola vez: si
// lo último publicado ya es un vencimiento, no repite.
func (s *Sincronizador) retirarSiVencio(ctx context.Context) (*Importacion, error) {
	previa, hayPrevia, err := s.cfg.Almacen.UltimaVigente(ctx)
	if err != nil {
		return nil, fmt.Errorf("leer la última importación vigente: %w", err)
	}
	if !hayPrevia || previa.Estado == EstadoVencido || len(previa.Vigentes) == 0 {
		return nil, nil
	}
	ultimaOK, hayOK, err := s.cfg.Almacen.UltimaSincronizacionOK(ctx)
	if err != nil {
		return nil, fmt.Errorf("leer la última sincronización buena: %w", err)
	}
	ahora := s.cfg.Ahora().UTC()
	if hayOK && ahora.Sub(ultimaOK) <= VigenciaMaxima {
		return nil, nil
	}

	imp := Importacion{
		ID: mensajeria.NuevoID(), Proveedor: s.cfg.Proveedor.Codigo,
		IniciadaEn: ahora, TerminadaEn: ahora, Estado: EstadoVencido,
		Error:     fmt.Sprintf("sin sincronizar desde %s (más de %s)", ultimaOK.Format(time.RFC3339), VigenciaMaxima),
		Secuencia: previa.Secuencia + 1,
		Vigentes:  []dominio.Item{},
		Bajas:     len(previa.Vigentes),
	}
	datos := mensajeria.ListaPreciosActualizada{
		Proveedor: s.cfg.Proveedor, ImportacionID: imp.ID, Secuencia: imp.Secuencia,
		Motivo: mensajeria.MotivoCatalogoVencido, Completa: true,
		Productos: []mensajeria.ProductoEvento{}, Ofertas: []mensajeria.OfertaEvento{},
	}
	for _, it := range previa.Vigentes {
		datos.Bajas = append(datos.Bajas, it.SKU)
	}
	if err := s.guardarConEvento(ctx, &imp, datos); err != nil {
		return nil, err
	}
	s.cfg.Logger.WarnContext(ctx, "catálogo de FAV vencido: se retiran sus ofertas del ranking",
		"importacion_id", imp.ID, "bajas", imp.Bajas, "ultima_sincronizacion_ok", ultimaOK)
	return &imp, nil
}

func (s *Sincronizador) guardarConEvento(ctx context.Context, imp *Importacion, datos mensajeria.ListaPreciosActualizada) error {
	sobre, err := mensajeria.NuevoSobre(ctx, mensajeria.TipoListaPreciosActualizada,
		mensajeria.VersionListaPreciosActualizada, imp.TerminadaEn, datos)
	if err != nil {
		return err
	}
	imp.Evento = &EventoOutbox{Sobre: sobre}
	if err := s.cfg.Almacen.Guardar(ctx, *imp); err != nil {
		return fmt.Errorf("guardar importación con su evento: %w", err)
	}
	return nil
}

// PublicarPendientes manda al broker los eventos del outbox en orden de
// secuencia. Se detiene en el primero que falla, para no publicar uno más
// nuevo antes que uno viejo: lo que quedó se reintenta en la vuelta siguiente.
func (s *Sincronizador) PublicarPendientes(ctx context.Context) int {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	pendientes, err := s.cfg.Almacen.Pendientes(ctx, 50)
	if err != nil {
		s.cfg.Logger.ErrorContext(ctx, "no se pudieron leer los eventos pendientes", "error", err.Error())
		return 0
	}
	publicados := 0
	for _, p := range pendientes {
		if err := s.cfg.Pub.Publicar(ctx, mensajeria.ClaveListaPrecios, p.Evento.Sobre); err != nil {
			s.cfg.Logger.WarnContext(ctx, "no se pudo publicar el evento; queda pendiente en el outbox",
				"evento_id", p.Evento.Sobre.EventoID, "secuencia", p.Secuencia, "error", err.Error())
			break
		}
		if err := s.cfg.Almacen.MarcarPublicado(ctx, p.ID, s.cfg.Ahora().UTC()); err != nil {
			// El evento salió pero no quedó marcado: se va a volver a publicar
			// y el consumidor lo descarta por evento_id.
			s.cfg.Logger.ErrorContext(ctx, "evento publicado pero no marcado", "evento_id", p.Evento.Sobre.EventoID, "error", err.Error())
			break
		}
		publicados++
		s.cfg.Logger.InfoContext(ctx, "evento publicado", "evento_id", p.Evento.Sobre.EventoID,
			"tipo", p.Evento.Sobre.Tipo, "secuencia", p.Secuencia)
	}
	return publicados
}

// Estado es lo que informa GET /estado.
type Estado struct {
	Proveedor              string     `json:"proveedor"`
	UltimaSincronizacionOK *time.Time `json:"ultima_sincronizacion_ok"`
	AntiguedadSegundos     *float64   `json:"antiguedad_segundos"`
	Vencido                bool       `json:"vencido"`
	OfertasVigentes        int        `json:"ofertas_vigentes"`
	SecuenciaVigente       int64      `json:"secuencia_vigente"`
	EventosPendientes      int64      `json:"eventos_pendientes"`
	Breaker                string     `json:"circuit_breaker"`
	BrokerConectado        bool       `json:"broker_conectado"`
}

// Estado arma el reporte.
func (s *Sincronizador) Estado(ctx context.Context) (Estado, error) {
	e := Estado{
		Proveedor:       s.cfg.Proveedor.Codigo,
		Breaker:         string(s.cfg.Breaker.Estado()),
		BrokerConectado: s.cfg.Pub.Conectado(),
	}
	if t, ok, err := s.cfg.Almacen.UltimaSincronizacionOK(ctx); err != nil {
		return Estado{}, err
	} else if ok {
		ant := s.cfg.Ahora().Sub(t).Seconds()
		e.UltimaSincronizacionOK, e.AntiguedadSegundos = &t, &ant
	}
	if v, ok, err := s.cfg.Almacen.UltimaVigente(ctx); err != nil {
		return Estado{}, err
	} else if ok {
		e.Vencido = v.Estado == EstadoVencido
		e.OfertasVigentes = len(v.Vigentes)
		e.SecuenciaVigente = v.Secuencia
	}
	n, err := s.cfg.Almacen.ContarPendientes(ctx)
	if err != nil {
		return Estado{}, err
	}
	e.EventosPendientes = n
	return e, nil
}

func resumenDe(imp Importacion) Resumen {
	return Resumen{
		ImportacionID: imp.ID, Estado: imp.Estado, Error: imp.Error,
		Variantes: imp.Variantes, Descartadas: len(imp.Descartes),
		Altas: imp.Altas, Modificadas: imp.Modific, Bajas: imp.Bajas,
	}
}

// ordenados devuelve los items por SKU. Nunca devuelve nil: una importación
// con el catálogo vacío también es vigente (Vigentes != nil), si no la
// siguiente vuelta compararía contra la anterior y repetiría las bajas.
func ordenados(m map[string]dominio.Item) []dominio.Item {
	c := dominio.Comparar(map[string]dominio.Item{}, m)
	if c.Altas == nil {
		return []dominio.Item{}
	}
	return c.Altas
}

func producto(it dominio.Item) mensajeria.ProductoEvento {
	return mensajeria.ProductoEvento{SKU: it.SKU, Nombre: it.Nombre, Categoria: it.Categoria, Unidad: it.Unidad}
}

func oferta(it dominio.Item) mensajeria.OfertaEvento {
	return mensajeria.OfertaEvento{
		ProductoSKU:     it.SKU,
		PrecioUnitario:  it.PrecioUnitario,
		Flete:           it.Flete,
		StockDisponible: it.StockDisponible,
		// FAV no tiene descuento por volumen (ADR-009).
		DescuentoPct:            0,
		CantidadMinimaDescuento: 0,
	}
}
