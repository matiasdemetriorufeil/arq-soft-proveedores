package gateway

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricas son las del borde del sistema (ADR-011): tasa, errores y latencia
// de todo lo que entra, medidas en el gateway, que es donde las ve el cliente.
// Usa un registro propio y no el global de Prometheus: así los tests arman
// tantos gateways como quieran sin chocar por nombres repetidos.
type metricas struct {
	registro *prometheus.Registry
	requests *prometheus.CounterVec
	duracion *prometheus.HistogramVec
	rechazos prometheus.Counter
}

// bucketsLatencia: los cortes incluyen 0,3 s a propósito, que es el objetivo
// de p95 de la capacidad (ADR-011). Así la alerta compara contra un borde
// real del histograma y no contra una interpolación.
var bucketsLatencia = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 1, 2.5, 5}

// NuevasMetricas crea las métricas del gateway. `replicasSanas` se consulta en
// cada scrape (puede ser nil en los tests).
func NuevasMetricas(replicasSanas func() int) *metricas {
	reg := prometheus.NewRegistry()
	m := &metricas{
		registro: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "Requests atendidas por el gateway, por ruta (patrón, no path crudo), método y status.",
		}, []string{"ruta", "metodo", "status"}),
		duracion: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_http_request_duration_seconds",
			Help:    "Latencia de punta a punta vista desde el gateway, por ruta.",
			Buckets: bucketsLatencia,
		}, []string{"ruta"}),
		rechazos: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gateway_limite_rechazos_total",
			Help: "Requests rechazadas con 429 por el límite de tráfico.",
		}),
	}
	reg.MustRegister(m.requests, m.duracion, m.rechazos,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	if replicasSanas != nil {
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gateway_replicas_sanas",
			Help: "Réplicas de proveedores-service que el balanceador tiene en rotación.",
		}, func() float64 { return float64(replicasSanas()) }))
	}
	return m
}

// Handler expone las métricas en el formato de texto de Prometheus. Se sirve
// en un puerto aparte, que no se publica: no es parte del contrato.
func (m *metricas) Handler() http.Handler {
	return promhttp.HandlerFor(m.registro, promhttp.HandlerOpts{})
}

// observar registra una request terminada. `ruta` es el patrón del mux (por
// ejemplo "GET /api/v1/stock/"), nunca el path con el SKU adentro: si no, cada
// SKU sería una serie nueva y Prometheus se llena.
func (m *metricas) observar(ruta, metodo string, status int, d time.Duration) {
	if m == nil {
		return
	}
	if ruta == "" {
		ruta = "sin_ruta"
	}
	m.requests.WithLabelValues(ruta, metodo, strconv.Itoa(status)).Inc()
	m.duracion.WithLabelValues(ruta).Observe(d.Seconds())
}

func (m *metricas) rechazoPorLimite() {
	if m == nil {
		return
	}
	m.rechazos.Inc()
}
