---
title: 'Entrega 3, Integrante 2: consumidor de eventos, caché Redis y búsqueda en proveedores-service'
type: 'feature'
ticket: ''
created: '2026-11-06'
status: 'built'
baseline_revision: '68a36704398a2efb18035ebff597cb59e9e07ec0'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/docs/PLAN-ENTREGA-3.md'
  - '{project-root}/docs/contracts/eventos.md'
  - '{project-root}/docs/adr/ADR-006-busqueda.md'
  - '{project-root}/docs/adr/ADR-007-cache.md'
  - '{project-root}/docs/adr/ADR-002-arquitectura-interna.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** Las ofertas de FAV quedan en la cola sin consumidor y no llegan al ranking; la consigna exige además caché medida antes y después y búsqueda indexada con paginación, filtros, orden y reconstrucción. Todo eso es la parte del Integrante 2 de `docs/PLAN-ENTREGA-3.md`.

**Approach:** Dentro de `proveedores-service`, sin tocar `internal/domain`: consumir `ListaPreciosActualizada`, agregar Redis como decorador de `OfertasRepo` y Meilisearch como índice derivado de PostgreSQL. Corregir el pool del gateway. Medir con k6 antes y después y dejar los ADR con esos números. No commitear ni pushear: al terminar, listar los commits sugeridos por parte y los comandos.

## Boundaries & Constraints

**Always:**
- El consumidor cumple los pasos de `eventos.md`: `LeerSobre`; sobre inválido o tipo/versión desconocidos → `Nack(false,false)` directo a DLQ; deduplicar en `eventos_procesados` y aplicar en **la misma transacción**; descartar `secuencia` menor o igual a la última del proveedor; upsert de productos y ofertas, borrar `bajas` y, si `completa`, borrar las ofertas del proveedor que no vinieron. Error transitorio → `Nack(false,true)`. Continúa la traza del `traceparent`.
- **Orden con dos réplicas:** `x-single-active-consumer: true` en `argsColaListaPrecios` de `pkg/mensajeria` y prefetch 1, para que no se aplique un evento nuevo antes que uno anterior (los eventos son deltas: descartar el viejo perdería datos). Documentarlo en `eventos.md`.
- El servicio arranca y atiende la capacidad aunque RabbitMQ, Redis o Meilisearch estén caídos, y reconecta solo. `/healthz` y `/readyz` no dependen de ellos.
- Caché según ADR-007: decorador de `ports.OfertasRepo` en `internal/adapters/redis`, clave `ofertas:v1:{sku}`, TTL 60 s, marca de "no existe" con TTL 10 s, timeout 50 ms, falla abierta a PostgreSQL y `singleflight`. Se apaga con `CACHE_HABILITADA=false`. Tras el commit de un evento se invalidan los SKU tocados.
- Búsqueda según ADR-006: un documento por producto, con `precio_min` y `proveedores_con_stock` calculados solo con ofertas con stock. Cliente HTTP propio con la biblioteca estándar (sin SDK). Comando `cmd/reindexar` que reconstruye con índice temporal y `swap-indexes`; el servicio reconstruye al arrancar si el índice no existe.
- Un SKU por reindexar no se pierde si Meilisearch falla: se registra en una tabla de pendientes dentro de la transacción del evento y un ciclo lo drena.
- `GET /api/v1/productos/buscar?q=&categoria=&proveedor=&precio_min=&precio_max=&con_stock=&orden=&pagina=&tamanio=` con `X-API-Key`, errores `problem+json`, `tamanio` máximo 50, `orden` ∈ {`precio_asc`, `precio_desc`} (relevancia por defecto). Los valores de los filtros se escapan: un parámetro no puede alterar la expresión de filtro de Meilisearch. El gateway agrega solo esta ruta, reenviada igual que la capacidad.
- Gateway: transporte propio con `MaxIdleConnsPerHost` acorde a la concurrencia (ADR-013), en lugar de `http.DefaultTransport`.
- Mediciones con `tests/carga/medir.sh`, en la misma máquina y con las mismas tasas (200, 1000 y 1600 req/s): **A** antes de tocar el código (commit base), **B** con el pool arreglado y `CACHE_HABILITADA=false`, **C** con pool y caché. Cada una guarda evidencia en `tests/carga/resultados/`, incluidas las consultas a PostgreSQL. Además, tasa de aciertos a demanda esperada (pocas req/s, SKU sesgados) con los contadores de Redis. Retraso del índice medido con al menos 20 eventos reales (objetivo p95 < 5 s).
- Un ADR pasa a "aceptado" solo con su evidencia. Si la caché no mejora la capacidad, ADR-007 lo dice, como ya anticipa.

**Never:**
- No modificar `internal/domain`, `stock-service`, frontend, JWT, ni límite de tráfico.
- No cambiar `openapi.yaml` ni el contrato v1 de la capacidad: la búsqueda no es parte de lo publicado a paneles solares.
- No commitear, pushear ni mergear. No tocar contenedores que no sean del proyecto ni usar `down -v` sobre volúmenes ajenos.
- No inventar números: lo que no se haya medido se declara como no medido.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Evento nuevo | `completa` con productos y ofertas de FAV | SKU `FAV-…` aparece en ranking y búsqueda | Ack |
| Duplicado | mismo `evento_id` dos veces | se aplica una vez | Ack |
| Secuencia vieja | `secuencia` ≤ última | sin cambios | Ack |
| Baja y completa | `bajas`, o `completa` sin un SKU previo | oferta borrada; el producto queda sin esa oferta | Ack |
| Mensaje basura | JSON inválido o tipo desconocido | va a DLQ sin reintentos | Nack sin requeue |
| Base caída | PostgreSQL no responde al aplicar | no se aplica nada | Nack con requeue; a la 3.ª entrega, DLQ |
| Acierto de caché | SKU cacheado | responde sin consultar PostgreSQL | — |
| SKU inexistente | dos pedidos seguidos | 404 las dos veces; el segundo no va a la base | — |
| Redis caído o lento | timeout de 50 ms | responde 200 desde PostgreSQL | log, sin error al cliente |
| Invalidación | evento toca un SKU cacheado | la siguiente lectura trae el valor nuevo | — |
| Concurrencia | N lecturas simultáneas de un SKU vencido | una sola consulta por réplica | — |
| Búsqueda | `q`, filtros, orden y página | resultados correctos con `total`, `pagina`, `tamanio` | — |
| Filtro hostil | `categoria=x" OR categoria="y` | no amplía los resultados | se trata como valor literal |
| Parámetro inválido | `tamanio=51`, `orden=foo`, `pagina=0` | 400 `PARAMETRO_INVALIDO` | problem+json |
| Meilisearch caído | búsqueda | 503 `NO_DISPONIBLE`; la capacidad sigue en 200 | problem+json |
| Meilisearch caído | llega un evento | oferta aplicada y Ack; SKU queda pendiente y se indexa al volver | log |

</frozen-after-approval>

## Code Map

- `services/proveedores-service/internal/ports/repositorio.go` -- agregar acá los puertos nuevos; `OfertasRepo` no cambia.
- `services/proveedores-service/internal/adapters/postgres/repo.go` -- modelo del adaptador; `TimeoutConsulta` 2 s; agregar la escritura transaccional y la lectura de documentos.
- `services/proveedores-service/internal/adapters/http/handler.go` -- `NuevoHandler` y `Config`; `conAPIKey` y `escribirProblema` se reutilizan.
- `services/proveedores-service/cmd/server/main.go` -- armado; `run` conecta los adaptadores nuevos.
- `services/proveedores-service/migrations/` -- siguiente archivo `004_*.sql`, idempotente; se aplican todos en cada arranque.
- `services/proveedores-service/Dockerfile` -- copia `pkg/observabilidad`; también tiene que copiar `pkg/mensajeria` y construir `cmd/reindexar`. Revisar `.dockerignore`.
- `pkg/mensajeria/topologia.go` -- `argsColaListaPrecios`; `LeerSobre`, `DeclararTopologia`, `ContextoDeTraza`. No redeclarar colas.
- `services/import-service/internal/integracion/rabbitmq_integration_test.go` -- modelo de test contra broker real; tiene que seguir pasando con el argumento nuevo.
- `services/api-gateway/internal/gateway/proxy.go` -- línea 49 usa `http.DefaultTransport`; `mux` en 56-59.
- `docker-compose.yml` -- ancla `x-proveedores`: agregar variables y dependencias; Redis y Meilisearch ya están. `.env.example` ya trae `REDIS_PASSWORD` y `MEILI_MASTER_KEY`.
- `tests/carga/medir.sh` -- usa la red `arq-soft-proveedores_interna`: correr con el nombre de proyecto por defecto. `recomendados.js`: sumar SKU sesgados por variable, con el comportamiento actual por defecto.
- `go.work` -- ya incluye `pkg/mensajeria`.

## Tasks & Acceptance

**Execution:**
- [x] **Parte A** `tests/carga/resultados/<fecha>-a-base/` -- medición A sobre el código sin cambios -- es imposible de obtener después
- [x] **Parte A** `services/api-gateway/internal/gateway/proxy.go`, test -- transporte con pool y ruta de búsqueda -- ADR-013; medición B
- [x] **Parte B** `pkg/mensajeria/topologia.go`, `migrations/004_*.sql`, puertos, `internal/adapters/postgres`, `internal/adapters/rabbitmq` -- consumidor con reconexión -- matriz de eventos, tests unitarios y de integración contra RabbitMQ y PostgreSQL reales
- [x] **Parte C** `internal/adapters/redis` -- decorador e invalidación -- tests unitarios y de integración contra Redis real; medición C y tasa de aciertos
- [x] **Parte D** `internal/adapters/meilisearch`, `cmd/reindexar`, handler -- índice, pendientes y búsqueda -- tests unitarios y de integración contra Meilisearch real; medir retraso y reconstrucción
- [x] **Parte E** `cmd/server/main.go`, `Dockerfile`, `docker-compose.yml`, `.env.example` -- armado completo
- [x] **Parte F** ADR-006, ADR-007, ADR-013, ADR-005, ADR-010, `eventos.md`, README del servicio y `tests/carga`, `docs/PLAN-ENTREGA-3.md` -- estado real con números

**Acceptance Criteria:**
- Given un clon limpio de esta rama, when `docker compose up -d --build`, then todos los contenedores quedan sanos y un SKU `FAV-…` del mock responde 200 en la capacidad a través del gateway.
- Given el sistema levantado, when se apagan Redis, Meilisearch y RabbitMQ de a uno, then la capacidad sigue respondiendo 200.
- Given las dos réplicas arriba, when llegan varios eventos seguidos, then se aplican en orden, y si cae la réplica activa, la otra toma el consumo.
- Given la evidencia guardada, then ADR-007 y ADR-013 muestran A, B y C con las mismas tasas, y ADR-006 el retraso medido.

## Implementation Notes

- La cola quorum con `x-delivery-limit: 3` descarta en la **cuarta** entrega. La matriz pide DLQ a la tercera, así que la hace el consumidor leyendo `x-delivery-count`.
- Una cola quorum con `x-single-active-consumer` rechaza `basic.get`: el helper `recibir` del test de `import-service` pasó a suscribirse. La UI *Get messages* tampoco anda en esa cola.
- Una cola ya declarada sin ese argumento da `PRECONDITION_FAILED`: remedio en `eventos.md`. La medición A declaró la cola vieja, así que B arrancó con `docker compose down -v` del proyecto.
- El `id` de Meilisearch es un hash del SKU, no el SKU: el motor rechaza un lote entero por un id con caracteres no válidos.
- Espera de 5 y 10 s antes de devolver un mensaje: sin ella, las entregas se gastan en milisegundos con la base caída.
- Si la lectura de Redis falla, no se intenta escribir: 108 ms → 56 ms por request con Redis detenido. La serie C y el simulacro son anteriores a este cambio, que no toca el camino con Redis arriba; 56 ms se midió después.

## Plan Change Log

## Review Triage Log

**Pass 1 (quick):** high 1 · medium 3 · low 3 · false 0 · maybe-false 0

| # | Hallazgo | Veredicto | Ruta | Evidencia / acción |
|---|---|---|---|---|
| 1 | `SKU_SESGADOS` no tiene efecto: `elegirSKU()` nunca se llama | high | patch | Real: `recomendados.js:85` sigue uniforme, así que el 90,2 % de ADR-007 describe una distribución que no corrió. Se corrige la llamada, se mide de nuevo y se actualiza el ADR. |
| 2 | ADR-005 da por medido el retraso de punta a punta | medium | patch | Real: `retraso-indice.js` cuenta desde `actualizado_en`, escrito por el consumidor. Se declara como no medido lo que falta. |
| 3 | ADR-005 indica "forzar una sincronización completa", que no existe | medium | patch | Real: `completa` solo sale en la primera sincronización y tras un vencimiento. Se documenta como limitación. |
| 4 | Recuperación tras perder MongoDB incompleta | medium | patch | Real: si el `completa` llega antes del `DELETE`, se descarta y queda registrado. Se documenta el orden. |
| 5 | Invalidación no garantizada: carrera entre réplicas y `Del` limitado a 50 ms | low | rechazado | Real (`NuevoAlmacen` fija 50 ms y sin reintentos), pero es poco frecuente, el TTL de 60 s lo acota y la carrera ya está aceptada en ADR-007. Reintentar agrega ramas. |
| 6 | Comentarios de `topologia.go` contradicen a `reintentar` | low | patch | Real: corrección directa de comentarios, sin cambiar conducta. |
| 7 | ADR-013 punto 4 presenta la caché como trabajo futuro | low | patch | Real: corrección directa del texto. |

## Design Notes

La búsqueda es lectura derivada: nunca reemplaza al ranking. Son tres mediciones y no dos porque, si el pool y la caché se suman en una sola, la mejora no se puede atribuir: ADR-013 ya dice que el límite era el gateway y no la base.

Limitación que se acepta y se documenta en ADR-005: si un evento agota sus entregas va a la DLQ y los siguientes se aplican igual, así que lo que traía queda desfasado hasta que ese dato vuelva a cambiar o se reprocese desde la DLQ. Queda visible en la DLQ; bloquear la cola por un hueco de `secuencia` frenaría todos los precios por un solo mensaje.

## Verification

**Commands:**
- `go vet ./... && go test -count=1 ./... -cover` en `services/proveedores-service`, `services/api-gateway` y `pkg/mensajeria` -- todo pasa sin Docker
- `go test -count=1 -tags=integration ./...` en `services/proveedores-service` y `services/import-service` -- todo pasa
- `docker compose up -d --build` y `docker compose ps` -- todos `healthy`
- `curl` a la capacidad con un SKU `FAV-…` y a `/api/v1/productos/buscar?q=buzo` por el gateway -- 200 con datos
