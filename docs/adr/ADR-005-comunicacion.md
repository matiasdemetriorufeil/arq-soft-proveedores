# ADR-005 — Comunicación entre servicios (D5)

- **Estado:** aceptado e implementado para la comunicación síncrona `api-gateway → proveedores-service → PostgreSQL`, la llamada `import-service → FAV` y la **publicación** de `ListaPreciosActualizada` (RabbitMQ, outbox y confirmaciones); **propuesto** para el **consumo** del evento en `proveedores-service` (Integrante 2) y las llamadas de `stock-service`
- **Fecha:** 2026-10-23 · actualizado 2026-11-03
- **Entrega:** 2 (23/10), primera versión publicada (la inicial de la Entrega 1 no se presentó); actualización en la 3 (06/11).
- **Decisión de la consigna:** D5. Uso de interacciones síncronas y asíncronas, tiempos de espera, reintentos, eventos e idempotencia.

## Contexto

La consigna (2.5) pide comunicación **síncrona y asíncrona**. Para la síncrona hay que definir timeout, manejo de errores, respuestas tardías o ausentes y política de reintentos. Para la asíncrona hay que usar mensajería, con la publicación y el consumo de un evento de dominio y el tratamiento de los mensajes fallidos.

Hoy existen el gateway y `proveedores-service` (dos réplicas). Los límites de [ADR-001](ADR-001-limites-de-los-servicios.md) prevén dos interacciones más: `stock-service` le pregunta a `proveedores-service` a quién reponer, e `import-service` le avisa que hay precios nuevos. La única capacidad que publicamos, `GET /api/v1/proveedores/recomendados`, es una lectura sin efectos.

## Opciones evaluadas

1. **Todo síncrono (HTTP).** Simple de seguir en una traza, pero acopla la disponibilidad: si `proveedores-service` está caído, `import-service` no puede avisar que llegaron precios y el aviso se pierde.
2. **Todo asíncrono (eventos).** Desacopla, pero una consulta que necesita respuesta inmediata (el ranking para reponer, la capacidad publicada) se vuelve request/reply sobre colas, más difícil de razonar y de depurar.
3. **Síncrono para consultas, asíncrono para avisos de cambio.** Se pregunta por HTTP cuando hace falta la respuesta para seguir; se publica un evento cuando algo cambió y a quien lo emite no le importa quién lo procesa ni cuándo.

Para la mensajería: **RabbitMQ** frente a **Kafka** y **NATS**. Kafka resuelve necesidades de volumen y de replay que no tenemos, y es pesado de levantar en compose; NATS core no garantiza la entrega sin JetStream. RabbitMQ trae colas durables, confirmaciones y dead-letter exchanges de fábrica, y está en el stack del complemento.

## Qué elegimos

**La opción 3.**

### Síncrono (implementado)

| Llamada | Timeout | Reintentos | Respuesta tardía o ausente |
|---|---|---|---|
| Consumidor → `api-gateway` | `ReadTimeout` 10 s; `WriteTimeout` = `TIMEOUT_UPSTREAM` + 10 s | los decide el consumidor; el contrato dice que el `GET` es seguro de reintentar | — |
| `api-gateway` → `proveedores-service` | **3 s** (`TIMEOUT_UPSTREAM`) | **1**, en otra réplica, ante un **error de transporte que no sea timeout** | error de transporte: marca la réplica como caída y reintenta; **timeout: no reintenta** y devuelve `504 NO_DISPONIBLE`; sin réplicas sanas: `502 NO_DISPONIBLE` |
| `proveedores-service` → PostgreSQL | **2 s** por consulta y por conexión (`TimeoutConsulta`) | ninguno en la request | `503 NO_DISPONIBLE` en `problem+json` |

Código: [`proxy.go`](../../services/api-gateway/internal/gateway/proxy.go), [`balanceador.go`](../../services/api-gateway/internal/gateway/balanceador.go) y [`repo.go`](../../services/proveedores-service/internal/adapters/postgres/repo.go).

- **Los timeouts decrecen de afuera hacia adentro** (3 s en el gateway, 2 s en la base): si la que está lenta es la base, el servicio alcanza a contestar `503` antes de que el gateway corte con `504`, y el consumidor recibe el error más preciso.
- **Qué se reintenta:** cualquier error de transporte que no sea el timeout (conexión rechazada, cortada o reiniciada) dispara un reintento en la otra réplica. **No hay garantía de que la primera réplica no haya recibido la request**: una conexión cortada después de enviar es indistinguible. El reintento es seguro porque el único endpoint es un `GET` sin efectos, no porque la request no haya llegado.
- **Qué no se reintenta:** el timeout. La réplica puede estar procesando la request, y reintentar duplicaría la carga sobre una dependencia que ya está lenta.
- **Propagación:** cada salto lleva `X-Request-ID` y `traceparent` (W3C), así que la misma request se sigue en los logs y en Jaeger ([ADR-011](ADR-011-observabilidad.md)).

### Síncrono (propuesto)

- `stock-service → proveedores-service`: `GET` de la misma capacidad por la red interna, con timeout de **1 s** y hasta **2 reintentos** con espera exponencial (100 ms y 300 ms) y jitter, porque es un `GET` idempotente. Si no responde, la orden de reposición queda en `BORRADOR` sin proveedor sugerido y se reintenta en el siguiente ciclo.
- `import-service → FAV` (catálogo, **implementado** en `internal/fav`) y `stock-service → FAV` (pedidos, propuesto): timeout de 3 s y hasta 3 reintentos solo ante timeout, `429`, `500` o `503`; los reintentos de un pedido repiten su `Idempotency-Key`. Detalle en [ADR-009](ADR-009-consumo-proveedor.md).

### Asíncrono (RabbitMQ: publicación implementada, consumo propuesto)

| Evento | Publica | Consume | Efecto |
|---|---|---|---|
| `ListaPreciosActualizada` | `import-service` | `proveedores-service` | Actualiza las ofertas de ese proveedor, invalida la caché ([ADR-007](ADR-007-cache.md)) y reindexa ([ADR-006](ADR-006-busqueda.md)). |
| `StockBajoMinimo` | `stock-service` | `stock-service` (worker) | Crea la orden de reposición en `BORRADOR` con el proveedor recomendado. |

El formato, la topología y lo que tiene que hacer el consumidor están en [`docs/contracts/eventos.md`](../contracts/eventos.md); el código compartido por los dos lados, en [`pkg/mensajeria`](../../pkg/mensajeria/).

- **Formato:** JSON con un sobre común: `evento_id` (UUID), `tipo`, `version`, `ocurrido_en`, `traceparent` y `datos`. *(implementado)*
- **Publicación sin pérdida:** *outbox*. En `import-service` el evento se guarda **dentro del mismo documento de MongoDB** que la importación (una escritura de un documento es atómica), y un publicador lo envía con *publisher confirms*. Si el broker no está, el evento espera en MongoDB y se reintenta cada 15 s, en orden de `secuencia`. En los servicios con PostgreSQL, el outbox es una tabla en la misma transacción. *(implementado en `import-service`)*
- **Entrega al menos una vez** y **consumidor idempotente:** el consumidor registra `evento_id` en una tabla `eventos_procesados` dentro de la misma transacción que aplica el efecto; un duplicado choca con la clave primaria y se descarta con `ack`.
- **Mensajes fallidos:** hasta **3 entregas**; después van a la cola `*.dlq` por el *dead-letter exchange*. El conteo lo lleva RabbitMQ: la cola es *quorum* con `x-delivery-limit: 3`, así el consumidor no tiene que contar reintentos. La reentrega es inmediata; si el consumidor necesita espera creciente, la hace antes de devolver el mensaje. Un mensaje que no se puede parsear va directo a la DLQ, sin reintentos. *(topología implementada y probada contra RabbitMQ real)*
- **Orden por proveedor:** cada evento trae una `secuencia` que crece; el consumidor descarta una menor o igual a la última que aplicó, así un reintento atrasado no pisa precios más nuevos.
- **Retraso aceptable** entre la publicación y el efecto: **menos de 5 s** en condiciones normales.

### Avisos entrantes de terceros (propuesto)

El webhook `pedido.actualizado` de FAV llega por HTTP a `stock-service`, directo y no a través del gateway, y **no es un evento de nuestra mensajería**: tiene el formato de FAV (`eventoId`, `ocurridoEn`, `pedido`), no pasa por RabbitMQ ni por nuestro outbox, y el sobre común de arriba no se le aplica. Comparte una sola regla con nuestros consumidores: se deduplica por su propio id (`eventoId`) antes de aplicar el efecto. Detalle en [ADR-009](ADR-009-consumo-proveedor.md).

## Por qué

- **Lo que hoy existe es una lectura**: la respuesta la necesita alguien que está esperando (el grupo de paneles solares), así que es síncrona por naturaleza.
- **Los avisos de cambio no pueden depender de que el destino esté arriba.** Con eventos durables, si `proveedores-service` está caído cuando llega una lista de precios, la procesa al volver.
- **El outbox evita el "guardé pero no avisé"** y el "avisé pero no guardé", que es justo lo que haría inconsistente al ranking.
- **Idempotencia por `evento_id`** porque RabbitMQ entrega al menos una vez: sin ella, un reintento del broker aplicaría dos veces la misma lista.

## Consecuencias aceptadas

- **El evento se publica pero todavía nadie lo consume:** hasta que el Integrante 2 implemente el consumidor, los mensajes esperan en la cola `proveedores-service.lista-precios` y el requisito 2.5 (evento publicado **y consumido**) queda a medias.
- **Consistencia eventual** entre la lista importada y el ranking: durante unos segundos la capacidad puede devolver precios viejos.
- **Un componente más (RabbitMQ)** en el compose y un outbox por cada servicio que publica.
- **Sin espera creciente entre reentregas** del broker: tres fallas seguidas por una caída corta de la base pueden mandar un mensaje bueno a la DLQ. Se reprocesa moviéndolo de vuelta desde la UI de RabbitMQ.
- **El reintento del gateway no sería seguro para un endpoint con efectos**, porque puede repetir una request que la réplica ya recibió. Hoy solo existe el `GET`.
- **Un error de transporte aislado saca a una réplica sana del reparto** hasta el próximo sondeo de salud (hasta 5 s).
- **La escalera de timeouts depende de la configuración:** `TIMEOUT_UPSTREAM` se puede cambiar en `.env` y el gateway no valida que supere los 2 s de la base. Con un valor de 2 s o menos, el consumidor recibiría `504` donde correspondía un `503`.
- **La escalera de timeouts solo ordena los errores cuando la lenta es la base.** Si la lentitud está en el gateway o en la red, el consumidor recibe `504` sin que el servicio llegue a contestar: es lo que pasó bajo sobrecarga en el test de carga ([ADR-013](ADR-013-capacidad-y-costos.md)).
- **Los 3 s no son la espera máxima del consumidor.** Se cuentan desde que el gateway procesa la request; con el gateway saturado, la request puede esperar antes en la cola, y el test de carga midió esperas más largas.
- **Un `504` no se reintenta**, aunque la otra réplica estuviera libre. **Un `503` del servicio tampoco**: si la base cayó, les pasa a las dos réplicas por igual.

## Con qué lo medimos

- **Timeouts vigentes**, verificados en el código y el compose del 23/10: 3 s en el gateway y 2 s por consulta en el servicio.
- **Comportamiento ante fallas y bajo carga** (réplica caída, base caída o pausada, errores por código bajo carga): ver [ADR-010](ADR-010-resiliencia.md) y [ADR-013](ADR-013-capacidad-y-costos.md).
- **Publicación, contra RabbitMQ real** (03/11, tests de `services/import-service/internal/integracion/` corridos contra un RabbitMQ 3.12 local; el compose usa 4.1, con la misma semántica de colas quorum y `x-delivery-limit`): el broker confirma el mensaje y llega persistente con `message_id` = `evento_id`; un `nack` sin reencolar lo manda a la DLQ; después de **3** entregas reencoladas también; sin broker, publicar falla en menos de 6 s sin colgarse.
- **Outbox** (tests unitarios de `internal/sincronizacion`): con el broker caído, dos sincronizaciones dejan **2** eventos pendientes y, al volver, se publican los **2 en orden** de secuencia.
- **Cuando exista el consumidor:** retraso entre `ocurrido_en` y el procesamiento (objetivo p95 < 5 s), mensajes en la DLQ y duplicados descartados.

## Qué nos haría cambiar de idea

- **Si la latencia normal de la capacidad se acerca a 1 s**, los 3 s del gateway quedan justos y habría que revisar los dos timeouts juntos.
- **Si aparece un endpoint con efectos**, el reintento automático del gateway tiene que limitarse a `GET` y a errores de conexión (antes de enviar), o exigir una clave de idempotencia.
- **Si el volumen de listas de precios pasa a miles por minuto** o hace falta reprocesar historia, evaluar Kafka en lugar de RabbitMQ.
- **Si la DLQ acumula mensajes de forma sostenida**, el problema es de contrato entre servicios y no de transporte: revisar la versión del evento.
