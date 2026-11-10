# ADR-011 — Observabilidad (D11)

- **Estado:** aceptado e implementado: logs y trazas (Entrega 2); métricas, tablero, objetivo de servicio y alertas (06/11)
- **Fecha:** 2026-10-22
- **Entrega:** 2 (23/10) — primera versión, como pide la consigna 6.3. Versión completa el 06/11 (Entrega 3).
- **Decisión de la consigna:** D11. Estrategia de logs, métricas, trazas y tableros, junto con el objetivo de servicio y los criterios de alerta.

## Contexto

La consigna (2.10) pide como mínimo logs estructurados, métricas y un tablero. Para esta entrega (6.3) exige solo **logs correlacionados y una primera traza distribuida del flujo principal**.

El sistema son dos procesos Go (`api-gateway` y dos réplicas de `proveedores-service`) más PostgreSQL, todos en contenedores. Dos cosas lo hacen necesario antes que cómodo:

- **La capacidad la consume otro grupo.** Cuando reporten "les pedimos proveedores y falló", no vamos a tener su pantalla: necesitamos un identificador que ellos puedan pasarnos y que nos lleve a la request exacta.
- **Hay un salto de proceso en el camino feliz.** Con el gateway repartiendo entre dos réplicas, "el servicio anduvo mal" ya no alcanza: hay que saber cuál réplica y en qué tramo.

## Opciones evaluadas

1. **Logs de texto plano y `grep`.** Cero dependencias, pero correlacionar dos procesos a mano es inviable y no deja traza.
2. **Logs JSON correlacionados + OpenTelemetry + Jaeger.** Un estándar de propagación (W3C `traceparent`), un colector que ya trae visor, y el logger y las trazas compartiendo el mismo identificador.
3. **Stack completo: Prometheus + Grafana + Loki + Tempo.** Es lo que cubriría los tres pilares de una, pero son cuatro componentes más en el `docker compose up` que la consigna exige que levante con un comando, a un día de la entrega.
4. **APM gestionado (Datadog, New Relic).** Fuera de discusión: exige credenciales y los secretos no van al repo.

## Qué elegimos

**La opción 2.** En el código:

- `pkg/observabilidad/logger.go` — `NuevoLogger` envuelve el handler JSON de `log/slog` y agrega `trace_id` y `span_id` leyendo el span del `context.Context`. La correlación vive en el handler, no en cada llamada: por eso un servicio la adopta cambiando una línea.
- `pkg/observabilidad/trazas.go` — `IniciarTrazas` deja el propagador W3C y el exportador OTLP/HTTP. **Sin `OTEL_EXPORTER_OTLP_ENDPOINT` es no-op**, así los binarios arrancan y los tests corren sin Jaeger.
- `jaegertracing/all-in-one` en el compose: colector y visor en un contenedor, OTLP en 4318, UI en 16686.
- **Dos identificadores, con públicos distintos.** `X-Request-ID` es para quien nos consume: el gateway lo genera si no viene y lo devuelve siempre, también en los errores. `trace_id` es para nosotros. Las dos líneas de log de una misma request, una por servicio, traen los dos.
- **Las rutas de salud no se trazan** (`otelhttp.WithFilter` en el servicio): las golpean cada 5 segundos y taparían las trazas que cuentan un flujo.

Detalle operativo y guion de la demo en [`docs/observabilidad/README.md`](../observabilidad/README.md).

### Métricas, tablero y alertas (06/11)

Es la opción 3 recortada a lo que pide la 2.10: **Prometheus y Grafana, sin Loki ni Tempo** (los logs y las trazas ya los resuelven `slog` y Jaeger).

- **Dónde se mide: en el gateway.** Es donde el cliente vive la capacidad, así que tasa, errores y latencia medidos ahí son los que importan para el objetivo de servicio. Métricas (`internal/gateway/metricas.go`, con `client_golang` y un registro propio):
  - `gateway_http_requests_total{ruta, metodo, status}`
  - `gateway_http_request_duration_seconds{ruta}`, un histograma con un corte en **0,3 s**, el umbral del objetivo
  - `gateway_limite_rechazos_total` (los `429` del límite de tráfico, [ADR-010](ADR-010-resiliencia.md))
  - `gateway_replicas_sanas`
- **La etiqueta `ruta` es el patrón del mux** (`r.Pattern`, por ejemplo `/api/v1/stock/`), nunca el path: con el SKU adentro, cada producto sería una serie nueva.
- **Puerto aparte (9091) y sin publicar.** Prometheus lo lee por la red interna; `/metrics` no queda en la URL pública.
- **Objetivo de servicio** de la capacidad (`GET /api/v1/proveedores/recomendados`), medido en el gateway: **p95 por debajo de 300 ms y menos del 1 % de respuestas 5xx**. Los 4xx no cuentan: son errores del cliente, incluido el `429`.
- **Tres alertas** (`infra/prometheus/alertas.yml`), cada una con `for` para no saltar por un pico aislado: `LatenciaP95Alta` (p95 > 300 ms durante 2 min), `ErroresAltos` (5xx > 1 % durante 2 min) y `SinReplicasSanas` (ninguna réplica en el reparto durante 30 s).
- **Tablero** (`infra/grafana/tableros/capacidad.json`, se carga solo al arrancar): requests por segundo, % de 5xx, p95, réplicas sanas, tasa por ruta y status, p50/p95/p99 con la línea del objetivo, rechazos por límite y alertas activas.

## Por qué

- **Un flujo, una traza.** El `traceparent` viaja del gateway al servicio, y el `QueryTracer` de pgx cuelga la consulta del span de la request: la traza del flujo principal tiene cuatro spans, del borde del sistema hasta el SQL.
- **El log encuentra la traza y la traza encuentra el log.** Con el `X-Request-ID` que devolvemos se llega a la línea, y de ahí al `trace_id` que se pega en Jaeger.
- **No agrega una dependencia dura.** Si Jaeger no está, el sistema responde igual. Eso vale para la demo y para los tests.
- **Pocas piezas.** Biblioteca estándar más OpenTelemetry; ningún framework.

## Consecuencias aceptadas

- **Las alertas no notifican.** Se ven en Prometheus (`/alerts`) y en el tablero, pero no hay Alertmanager que mande un mensaje: para el TP alcanza con verlas.
- **Solo el gateway expone métricas.** Los servicios no exponen las suyas (pool de conexiones, ciclo de reposición, sincronización con FAV); para eso están los logs y las trazas.
- **El objetivo de 300 ms es holgado a propósito:** hoy el p95 está en milisegundos. Sirve para detectar una degradación seria (la base lenta, el gateway colapsado), no para afinar.
- **Jaeger `all-in-one` guarda las trazas en memoria.** Reiniciarlo las borra: no hay historial para comparar entre días ni después del Game Day.
- **Se exporta el 100 % de las trazas**, sin muestreo. Alcanza al volumen de la demo; con carga real (la prueba de D13) hay que muestrear.
- **Los sondeos de salud dominan los logs.** Medido en una réplica: **363 líneas de `/healthz` contra 12 de la capacidad**. Se filtraron de las trazas, no de los logs.
- **El gateway todavía traza sus propias rutas de salud.** Queda como deuda anotada; en la UI hay que filtrar por operación.

## Con qué lo medimos

Mediciones del 22/10 sobre el sistema levantado con `docker compose up`:

- **Traza del flujo principal:** 4 spans anidados en una sola traza — `api-gateway` (server) → `api-gateway` (client) → `proveedores-service` (server) → `SELECT productos`, este último con `db.system`, `db.statement`, `db.sql.table` y `db.rows_affected`. Captura en [`docs/observabilidad/traza-entrega-2.png`](../observabilidad/traza-entrega-2.png).
- **Correlación:** las dos líneas de log de una misma request comparten `request_id` y `trace_id`. Verificado contra la API de Jaeger, no solo contra la UI.
- **Latencia de la capacidad medida en el gateway** (campo `duration_ms`, n=23): mediana **1,99 ms**, p95 **4,92 ms**, máximo **8,48 ms**. Comparable con los 1–3 ms que midió el [ADR-002](ADR-002-arquitectura-interna.md) contra el servicio directo, así que el gateway y la instrumentación no agregan un costo visible a este volumen. Es una medición puntual; la de D13 la reemplaza.
- **Con Jaeger detenido**, la capacidad sigue respondiendo 200.

Mediciones del 06/11 sobre el sistema completo (14 contenedores) levantado con `docker compose up`:

- **Prometheus lee al gateway** (`health: up`) y las tres reglas cargan sin errores.
- **Etiquetas acotadas:** después de consultar dos SKUs distintos de stock, `/metrics` tiene una sola serie `ruta="/api/v1/stock/"` y ningún SKU como etiqueta (también lo cubre un test).
- **Una ráfaga de 1 000 requests** quedó en las métricas como 173 `200` y 827 `429`, y `gateway_limite_rechazos_total` llegó a 827: los mismos números que vio el cliente.
- **Alerta disparada de verdad:** con las dos réplicas detenidas, `SinReplicasSanas` pasó a `firing` **48 s** después (5 s del sondeo de salud, 5 s del scrape y los 30 s del `for`), mientras la capacidad respondía `502 NO_DISPONIBLE`.
- **Grafana** carga solo el datasource y el tablero, y el datasource responde a la consulta de salud.

## Qué nos haría cambiar de idea

- **Si hace falta historial de trazas** —comparar antes y después del Game Day, o revisar una falla del día anterior— Jaeger con almacenamiento persistente, o Tempo.
- **Si alguien tiene que enterarse de una alerta sin mirar el tablero**, sumar Alertmanager con un canal (mail o Discord).
- **Si una falla no se ve desde el gateway** (por ejemplo, el ciclo de reposición trabado), exponer métricas propias en ese servicio.
- **Si el volumen de logs molesta**, dejar de loguear los sondeos de salud o bajarlos a `DEBUG`.
- **Si sumamos un tercer servicio con otro lenguaje**, revisar que el propagador W3C siga siendo el denominador común.
