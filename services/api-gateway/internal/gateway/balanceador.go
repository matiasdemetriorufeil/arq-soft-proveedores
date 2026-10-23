package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Instancia es una réplica del upstream con su estado de salud.
type Instancia struct {
	URL  *url.URL
	sana atomic.Bool
}

// Sana dice si el último sondeo —o la última request— la encontró viva.
func (i *Instancia) Sana() bool { return i.sana.Load() }

// Destino es el host de la réplica. Es lo que se publica como atributo
// `upstream` del span, así en Jaeger se ve quién atendió cada request.
func (i *Instancia) Destino() string { return i.URL.Host }

// Balanceador reparte las requests entre las réplicas del upstream con
// round-robin, salteando las que están caídas. Sondea /healthz en segundo plano
// para enterarse de las caídas y de las recuperaciones sin que medie una
// request del cliente.
type Balanceador struct {
	instancias []*Instancia
	turno      atomic.Uint64
	cliente    *http.Client
	intervalo  time.Duration
	timeout    time.Duration
	log        *slog.Logger
}

// ConfigBalanceador son los parámetros del balanceador. Upstreams son URLs base
// ("http://host:puerto"); el resto tiene que venir ya resuelto por el llamador.
type ConfigBalanceador struct {
	Upstreams []string
	Intervalo time.Duration
	Timeout   time.Duration
	Logger    *slog.Logger
}

// NuevoBalanceador valida los upstreams y arranca con todas las instancias
// marcadas como caídas: /readyz no puede decir que el gateway está listo antes
// de que alguna réplica haya contestado de verdad.
func NuevoBalanceador(cfg ConfigBalanceador) (*Balanceador, error) {
	if len(cfg.Upstreams) == 0 {
		return nil, errors.New("hace falta al menos un upstream")
	}
	if cfg.Intervalo <= 0 || cfg.Timeout <= 0 {
		return nil, errors.New("el intervalo de sondeo y el timeout tienen que ser mayores a cero")
	}
	if cfg.Logger == nil {
		return nil, errors.New("hace falta un logger")
	}

	b := &Balanceador{
		intervalo: cfg.Intervalo,
		timeout:   cfg.Timeout,
		log:       cfg.Logger,
		cliente:   &http.Client{Timeout: cfg.Timeout},
	}
	for _, crudo := range cfg.Upstreams {
		u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(crudo), "/"))
		if err != nil {
			return nil, fmt.Errorf("upstream %q: %w", crudo, err)
		}
		if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("upstream %q: se espera una URL como http://host:puerto", crudo)
		}
		b.instancias = append(b.instancias, &Instancia{URL: u})
	}
	return b, nil
}

// Instancias son las réplicas configuradas, en el orden en que se declararon.
func (b *Balanceador) Instancias() []*Instancia { return b.instancias }

// Siguiente devuelve la próxima réplica sana en round-robin. El turno avanza
// siempre, también cuando la que toca está caída, así el reparto entre las que
// quedan sigue siendo parejo.
func (b *Balanceador) Siguiente() (*Instancia, bool) {
	n := len(b.instancias)
	inicio := int((b.turno.Add(1) - 1) % uint64(n))
	for i := range n {
		if cand := b.instancias[(inicio+i)%n]; cand.Sana() {
			return cand, true
		}
	}
	return nil, false
}

// Otra devuelve una réplica sana distinta de la que acaba de fallar, para el
// reintento. No mueve el turno: el reintento no es una request nueva.
func (b *Balanceador) Otra(excluir *Instancia) (*Instancia, bool) {
	n := len(b.instancias)
	inicio := int(b.turno.Load() % uint64(n))
	for i := range n {
		cand := b.instancias[(inicio+i)%n]
		if cand != excluir && cand.Sana() {
			return cand, true
		}
	}
	return nil, false
}

// AlgunaSana es lo que responde /readyz: el gateway está listo si tiene al
// menos una réplica a la que mandar requests.
func (b *Balanceador) AlgunaSana() bool {
	for _, inst := range b.instancias {
		if inst.Sana() {
			return true
		}
	}
	return false
}

// MarcarCaida saca a la instancia del reparto sin esperar al próximo sondeo: la
// request que acaba de fallar ya es evidencia de que no está atendiendo. El
// sondeo la vuelve a poner cuando responda.
//
// Recibe el contexto de la request que falló, no uno vacío: la línea que explica
// el failover es la que después se busca por `trace_id`, y los ids salen del
// span que vive en ese contexto.
func (b *Balanceador) MarcarCaida(ctx context.Context, inst *Instancia) {
	b.marcar(ctx, inst, false)
}

// IniciarSondeo hace una primera vuelta sincrónica —así /readyz dice la verdad
// desde la primera request— y después sigue sondeando en segundo plano hasta
// que se cancele el contexto.
func (b *Balanceador) IniciarSondeo(ctx context.Context) {
	b.Sondear(ctx)
	go func() {
		tic := time.NewTicker(b.intervalo)
		defer tic.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tic.C:
				b.Sondear(ctx)
			}
		}
	}()
}

// Sondear hace una vuelta de /healthz sobre todas las réplicas.
func (b *Balanceador) Sondear(ctx context.Context) {
	for _, inst := range b.instancias {
		b.marcar(ctx, inst, b.sondearUna(ctx, inst))
	}
}

// marcar cambia el estado de la instancia y loguea solo las transiciones: un
// upstream caído no puede llenar los logs una vez por intervalo.
func (b *Balanceador) marcar(ctx context.Context, inst *Instancia, sana bool) {
	if previa := inst.sana.Swap(sana); previa == sana {
		return
	}
	nivel, msg := slog.LevelWarn, "upstream caído: deja de recibir requests"
	if sana {
		nivel, msg = slog.LevelInfo, "upstream recuperado: vuelve al reparto"
	}
	// Si la transición la disparó una request, se loguea con el logger de esa
	// request, que ya trae `request_id`; el `trace_id` sale del span del
	// contexto. Desde el sondeo en segundo plano no hay ninguno de los dos y
	// queda el logger del proceso.
	log := b.log
	if l, ok := ctx.Value(ctxLogger).(*slog.Logger); ok {
		log = l
	}
	log.LogAttrs(ctx, nivel, msg, slog.String("upstream", inst.Destino()))
}

// sondearUna pregunta por /healthz, que en proveedores-service responde 200 sin
// tocar la base: el sondeo mide si el proceso atiende, no si la base anda. De
// eso se encarga /readyz, que es el healthcheck del contenedor.
func (b *Balanceador) sondearUna(ctx context.Context, inst *Instancia) bool {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.URL.JoinPath(rutaSalud).String(), nil)
	if err != nil {
		return false
	}
	resp, err := b.cliente.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	// Hay que drenar el cuerpo para poder reusar la conexión en el próximo sondeo.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}
