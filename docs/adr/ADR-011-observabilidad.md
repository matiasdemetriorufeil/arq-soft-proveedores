# ADR-011 — Observabilidad (D11)

- **Estado:** aceptado e implementado para **logs y trazas**; **propuesto** para métricas, tablero, objetivo de servicio y alertas (no implementados todavía)
- **Fecha:** 2026-10-22
- **Entrega:** 2 (23/10) — primera versión, como pide la consigna 6.3. La versión completa va a la presentación final.
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

## Por qué

- **Un flujo, una traza.** El `traceparent` viaja del gateway al servicio, y el `QueryTracer` de pgx cuelga la consulta del span de la request: la traza del flujo principal tiene cuatro spans, del borde del sistema hasta el SQL.
- **El log encuentra la traza y la traza encuentra el log.** Con el `X-Request-ID` que devolvemos se llega a la línea, y de ahí al `trace_id` que se pega en Jaeger.
- **No agrega una dependencia dura.** Si Jaeger no está, el sistema responde igual. Eso vale para la demo y para los tests.
- **Pocas piezas.** Biblioteca estándar más OpenTelemetry; ningún framework.

## Consecuencias aceptadas

- **Todavía no cumplimos la 2.10 completa:** no hay métricas, ni tablero, ni objetivo de servicio, ni alertas. Es deliberado para esta entrega y es lo que falta para la versión completa de este ADR (ver abajo).
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

## Qué nos haría cambiar de idea

- **Si hace falta historial de trazas** —comparar antes y después del Game Day, o revisar una falla del día anterior— Jaeger con almacenamiento persistente, o Tempo.
- **Si aparece una pregunta que las trazas no contestan** ("¿cuántos 5xx por minuto?", "¿cuándo empezó?"), hay que agregar métricas: Prometheus con las cuatro señales RED por ruta, un tablero en Grafana y una alerta sobre la tasa de 5xx y el p95. Eso es la versión completa de este ADR y es lo que falta para la 2.10.
- **Si el volumen de logs molesta**, dejar de loguear los sondeos de salud o bajarlos a `DEBUG`.
- **Si sumamos un tercer servicio con otro lenguaje**, revisar que el propagador W3C siga siendo el denominador común.
