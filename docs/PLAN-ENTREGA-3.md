# Plan de trabajo: Entrega 3 (viernes 6/11) y Presentación grupal (11 o 13/11)

Reparto del trabajo entre los 4 integrantes para llegar a la Entrega 3 y a la presentación con lo obligatorio de la consigna (sección 6, "Presentación Grupal") y lo que pide la cátedra para el 6/11.

- **Alcance:** mínimo obligatorio de la presentación. Nada de bonus (ya anunciamos que no tomamos ninguno).
- **Dominio:** el mismo. El grupo de paneles solares consume nuestra capacidad y nosotros consumimos a FAV (grupo de ropa).
- **Fuentes:** [consigna oficial](consigna-practico-integrador-2026.md) y [complemento](TP-Integrador-Complemento%20de%20Consigna.md). Si algo no coincide, manda la consigna.
- **Punto de partida:** tag `entrega-2` (`main` al 23/10). Todo lo que sigue está en los ADR como "propuesto"; ahora hay que construirlo y pasar cada ADR a "aceptado" con evidencia.

## Estado

| Integrante | Parte | Estado |
|---|---|---|
| 1 (Fede) | Infraestructura, mensajería e importación de FAV | ✅ Hecho (rama `feat/import-service`): compose con RabbitMQ, MongoDB, Redis, Meilisearch, `fav-mock` y base de stock; `pkg/mensajeria` y `docs/contracts/eventos.md`; `import-service` con test de contrato; ADR-014 y ADR-001, 003, 005, 009 y 010 actualizados. Falta: correr los tests de integración de MongoDB y `docker compose up` completo en una máquina con Docker |
| 2 | Lectura rápida: caché y búsqueda en `proveedores-service` | ✅ Hecho (en `main` desde el PR #8, 06/11): consumidor del evento con DLQ y consumidor único, caché Redis, búsqueda en Meilisearch con reconstrucción y pool del gateway; ADR-006 y ADR-007 aceptados, ADR-005, 010 y 013 actualizados, con mediciones A, B y C en `tests/carga/resultados/2026-11-06-*`. La capacidad máxima con pool y caché no está medida: ninguna serie llegó al límite a 1 600 req/s |
| 3 | `stock-service`: reserva, reposición y pedidos a FAV | ✅ Hecho (rama `feat/stock-service`): servicio en capas con su base, reserva sin sobreventa con test de concurrencia, reposición automática, pedidos a FAV con `Idempotency-Key` y webhook HMAC (estos dos los terminó el Integrante 1), ADR-004 nuevo y ADR-002, 009 y SPEC al día. Probado contra PostgreSQL real y de punta a punta contra el mock de FAV. Ruteado en el gateway por la parte 4 |
| 4 (Santino) | Entrada, frontend, nube y operación | 🟡 Casi todo (rama `feat/frontend-nube`, sobre `feat/stock-service` y con `main` mergeado): login JWT, límite de tráfico con `429`, ruteo a `stock-service` y a la búsqueda, frontend, métricas, tablero, 3 alertas, CI, contrato 1.1.0 y despliegue preparado (`docs/NUBE.md`); ADR-008, 010, 011 y `ARCHITECTURE.md` al día. Falta: crear la VM y publicar la URL, y el Game Day |

Cada uno actualiza su fila cuando termina.

**Cierre del 10/11.** Las cuatro partes están en `main`. Lo que se cerró después del
6/11, fuera del reparto de arriba porque no era de nadie en particular:

- **CI en verde.** El job `Integración (services/import-service)` venía fallando en
  `main`: MongoDB 8.0 no arranca sobre el kernel 6.19 de los runners de GitHub
  (SERVER-121912). Se subió la imagen a 9.0 en el compose y en el test. Esto también
  cierra el pendiente del Integrante 1 de correr los tests de integración de MongoDB:
  ahora corren en cada push.
- **Game Day y [`POSTMORTEM.md`](POSTMORTEM.md)**, que era el hueco obligatorio de la
  defensa individual.
- **[`DEMO-ENTREGA-3.md`](DEMO-ENTREGA-3.md)** y el tag `entrega-3`.
- **`README.md`**, cuya tabla de estado seguía dando por pendientes el frontend, las
  métricas y la nube, y **[ADR-001](adr/ADR-001-limites-de-los-servicios.md)**, que
  daba `stock-service` como "propuesto" contra lo que ya decían ADR-002, 003 y 004.
- Se cerró el **PR #7** (`feat/stock-service`): su contenido entró a `main` por el
  squash del PR #9, que salió de esa misma rama.

**Lo único obligatorio que queda sin cumplir es la URL pública en la nube.**

## Antes de arrancar

- **El 6/11 se congela el código** (presentación de los profes). Lo que no esté en `main` ese día no entra a la presentación. Del 7 al 10 solo se ensaya, se mide y se documenta. **Confirmarlo con el profe**: la consigna escrita no lo dice.
- **El 4/11 es el recuperatorio.** Quien rinda, que avise para mover su bloque.
- **Tres servicios, no cuatro.** La consigna pide al menos tres operativos. Proponemos `proveedores-service`, `import-service` y `stock-service`, y **no construir `catalog-service`**: el mapeo de FAV ya lo resuelve la capa anticorrupción de `import-service` ([ADR-009](adr/ADR-009-consumo-proveedor.md)). Hay que dejarlo escrito en un ADR nuevo que reemplace esa parte de [ADR-001](adr/ADR-001-limites-de-los-servicios.md). **Lo valida el grupo en la reunión de inicio.**
- **El revisor automático compara los ADR con el código.** Un ADR pasa a "aceptado" solo cuando el código existe y hay una medición o un test que lo muestre.
- **Las mismas reglas de siempre:** Go con biblioteca estándar y pocas dependencias, `main` siempre levanta con `docker compose up`, rama por integrante, PR revisado por otro y squash merge.

## Qué tiene que estar

### Entrega 3 — viernes 6/11 (presentación de los profes)

- [x] Consumo **real** de FAV (ya no diseñado): catálogo en el ranking y pedidos de reposición. **Contra el mock que cumple su OpenAPI v1.1.0**, porque FAV nunca publicó su URL; el test de contrato pasa igual (era el plan B escrito en *Riesgos*)
- [ ] Capacidad propia **en la nube con URL pública** — falta crear la VM; el override, el script y la guía están ([`NUBE.md`](NUBE.md))
- [x] Caché **medida** antes y después ([ADR-007](adr/ADR-007-cache.md), series en `tests/carga/resultados/2026-11-06-*`)
- [x] Test de carga corrido de nuevo con caché y con el pool del gateway. **Contra la nube no**, porque no hay nube
- [x] Plan escrito ante la caída de cada dependencia ([ADR-010](adr/ADR-010-resiliencia.md) actualizado y [`POSTMORTEM.md`](POSTMORTEM.md) con el Game Day)
- [x] Tag `entrega-3` y demo de 5 minutos ([`DEMO-ENTREGA-3.md`](DEMO-ENTREGA-3.md))

### Presentación grupal — 11 o 13/11 (consigna)

- [x] Frontend completo, desde el ingreso del usuario hasta la funcionalidad compartida (login → buscar → ranking → reservar, embebido en el gateway)
- [x] Gateway con autenticación (API key y JWT), direccionamiento y **control de tráfico** (token bucket con `429`)
- [x] Al menos **3 servicios** operativos: `proveedores-service`, `import-service` y `stock-service`
- [ ] Capacidad propia en la nube con URL pública — **el único obligatorio que falta**
- [x] Capacidad del proveedor (FAV) incorporada al flujo principal: su catálogo entra al ranking y las reposiciones salen como pedidos
- [x] **2 tipos de almacenamiento** en el flujo: PostgreSQL (`proveedores` y `stock`) y MongoDB (`import-service`)
- [x] Caché en un flujo de lectura relevante, con medición antes y después ([ADR-007](adr/ADR-007-cache.md))
- [x] **2 patrones** de arquitectura interna reconocibles: hexagonal (`proveedores-service`) y en capas (`stock-service`)
- [x] Evento de dominio por mensajería, con **consumidor idempotente** por `evento_id` y mensajes no procesables a la DLQ
- [x] Búsqueda indexada con paginación, filtros, orden y reconstrucción del índice (Meilisearch, [ADR-006](adr/ADR-006-busqueda.md))
- [x] Decisiones verificadas con evidencia medida en el sistema (13 de los 14 ADR en "aceptado"; ADR-014 espera a la cátedra)
- [x] Limitaciones y deuda técnica actualizadas en `ARCHITECTURE.md`

### Defensa individual (lo que cada uno tiene que poder mostrar)

- [x] Operación crítica con garantías de consistencia y concurrencia (reserva sin sobreventa, **D4**)
- [x] Tests unitarios y un test de integración contra una dependencia real, por servicio nuevo
- [x] **Test de contrato** sobre lo que consumimos de FAV (catálogo y pedidos)
- [x] Balanceo operativo (ya está, [ADR-012](adr/ADR-012-balanceo-de-carga.md))
- [x] Caída simulada de una dependencia propia y de FAV, con su informe en [`POSTMORTEM.md`](POSTMORTEM.md)
- [x] Logs, métricas, trazas, **tablero y alerta** (tres alertas, [ADR-011](adr/ADR-011-observabilidad.md))
- [x] Pipeline de CI que corre los tests en cada push (verde en `main`; los 10 jobs)

## Lo que ya está decidido (sale de los ADR, no se vuelve a discutir)

| Tema | Decisión | ADR |
|---|---|---|
| Mensajería | RabbitMQ. Evento `ListaPreciosActualizada`, que publica `import-service` y consume `proveedores-service`; formato en [`eventos.md`](contracts/eventos.md) | [ADR-005](adr/ADR-005-comunicacion.md) |
| Persistencia | PostgreSQL para `proveedores-service` y `stock-service` (cada uno con **su propia base**); MongoDB para `import-service` | [ADR-003](adr/ADR-003-persistencia.md) |
| Búsqueda | Meilisearch, un documento por producto, alimentado por `proveedores-service` | [ADR-006](adr/ADR-006-busqueda.md) |
| Caché | Redis como decorador de `OfertasRepo`: TTL 60 s, invalidación por evento, timeout 50 ms y lectura de PostgreSQL si falla | [ADR-007](adr/ADR-007-cache.md) |
| FAV | Catálogo cada 5 min desde `import-service`; pedidos con `Idempotency-Key` desde `stock-service`; webhook HMAC directo a `stock-service`, **nunca por el gateway** | [ADR-009](adr/ADR-009-consumo-proveedor.md) |
| Resiliencia | Circuit breaker hacia FAV; límite de tráfico (token bucket) en el gateway | [ADR-010](adr/ADR-010-resiliencia.md) |
| Nube | Opción A de ADR-013: una VM con el mismo `docker compose` | [ADR-013](adr/ADR-013-capacidad-y-costos.md) |

## Las cuatro partes

### Integrante 1 (Fede): Infraestructura, mensajería e importación de FAV

Rama: `feat/import-service`. **Tu parte arranca primero y destraba a los demás:** el compose con la infraestructura nueva y el formato del evento.

Primero (martes y miércoles a la mañana):
- [x] `docker-compose.yml`: RabbitMQ, MongoDB, Redis y Meilisearch, con healthchecks, volúmenes y variables en `.env.example` (sin secretos)
- [x] Una base de PostgreSQL por servicio (`proveedores` y `stock`) en el mismo contenedor, con usuarios distintos
- [x] Formato del evento `ListaPreciosActualizada`: exchange, cola, cola de mensajes fallidos (DLQ), `evento_id` para idempotencia y JSON de ejemplo en `docs/contracts/eventos.md`.
- [ ] Avisar en el grupo cuando el PR #6 esté en `main` (lo necesitan los integrantes 2 y 3).

Después:
- [x] `import-service` en Go: adaptador `fav` (capa anticorrupción) que lee el catálogo cada 5 min, guarda la importación en MongoDB, compara con la anterior y publica solo los cambios
- [x] Si FAV no responde: timeout de 3 s, reintentos, circuit breaker y última importación válida (hasta 24 h)
- [x] Test de contrato contra el OpenAPI de FAV (`docs/contracts/consumidos/`)
- [x] Test de integración contra MongoDB o RabbitMQ real (testcontainers, como en `proveedores-service`)
- [x] Logs con `trace_id` usando `pkg/observabilidad`

Documentación:
- [x] ADR nuevo que reemplaza la parte de `catalog-service` de ADR-001
- [x] ADR-005 (mensajería) y ADR-009 (catálogo) de "propuesto" a "aceptado"
- [x] ADR-001 y ADR-003 validados con los servicios nuevos
- [ ] Correr `go test -tags=integration ./internal/adapters/mongo/ ./internal/integracion/` y `docker compose up` completo en una máquina con Docker (los de RabbitMQ ya pasaron contra un broker real)

**Lo que quedó listo para los demás**

- **Integrante 2:** el formato del evento, las colas y los pasos del consumidor están en [`contracts/eventos.md`](contracts/eventos.md). Usá `pkg/mensajeria` (`LeerSobre`, `DeclararTopologia`, los tipos de `ListaPreciosActualizada`) para no declarar las colas distinto. FAV ya existe como proveedor 11 (`003_proveedor_fav.sql`). Redis y Meilisearch ya levantan en el compose (`REDIS_PASSWORD` y `MEILI_MASTER_KEY` en `.env`).
- **Integrante 3:** la base `stock` con su usuario ya se crea en el Postgres del compose (`STOCK_DB`, `STOCK_DB_USER`, `STOCK_DB_PASSWORD`). El mock de FAV (`services/import-service/internal/favmock`) ya tiene `POST /v1/pedidos`, `GET /v1/pedidos/{id}` y manda el webhook firmado, validados por el test de contrato.
- **Integrante 4:** el mock de FAV se publica en `localhost:8090` y la UI de RabbitMQ en `localhost:15672`, para el tablero y el Game Day (`POST /_mock/modo {"modo":"caido"}`).

### Integrante 2: Lectura rápida: caché y búsqueda en `proveedores-service`

Rama: `feat/cache-busqueda`. Todo pasa dentro de `proveedores-service`, sin tocar el dominio (hexagonal, [ADR-002](adr/ADR-002-arquitectura-interna.md)).

- [x] **Antes de tocar nada:** medir con k6 la capacidad sin caché (línea de base para el "antes")
- [x] Consumidor de `ListaPreciosActualizada` ([`eventos.md`](contracts/eventos.md)): idempotente por `evento_id`, descarta secuencias viejas, mensajes no procesables a la DLQ, actualiza las ofertas en PostgreSQL
- [x] Caché Redis como decorador de `OfertasRepo` (`internal/adapters/redis`), con invalidación por evento y `singleflight`
- [x] Medir "después" con el mismo script y guardar la evidencia en `tests/carga/resultados/`
- [x] Meilisearch: índice de productos, actualización por evento y comando para reconstruirlo desde cero
- [x] Endpoint de búsqueda con paginación, filtros (`categoria`, proveedor, precio, solo con stock) y orden
- [x] Arreglo del transporte del gateway (`MaxIdleConnsPerHost`), ya probado en el experimento de [ADR-013](adr/ADR-013-capacidad-y-costos.md)
- [x] Tests unitarios del decorador y de integración contra Redis real

Documentación:
- [x] ADR-006 y ADR-007 a "aceptado", con el retraso máximo del índice medido y la comparación antes y después
- [x] ADR-013 actualizado con la corrida nueva

### Integrante 3: `stock-service`: reserva, reposición y pedidos a FAV

Rama: `feat/stock-service`. Es **la operación crítica de la defensa** (D4) y el **segundo patrón** (en capas).

- [x] `stock-service` en Go, en capas (handler → service → repository), con su propia base PostgreSQL
- [x] Reserva de stock **sin sobreventa** con `SELECT … FOR UPDATE` dentro de una transacción (RN-13)
- [x] Test de concurrencia: N reservas simultáneas sobre el mismo producto nunca superan el disponible
- [x] Punto de reorden y orden de reposición, una sola abierta por producto (RN-14, RN-15)
- [x] Le pide a `proveedores-service` el mejor proveedor (timeout 1 s y 2 reintentos, [ADR-005](adr/ADR-005-comunicacion.md))
- [x] Pedido a FAV con `Idempotency-Key` y receptor del webhook con firma HMAC (directo, fuera del gateway). Lo terminó el Integrante 1 el 06/11, junto con los pedidos en el mock de FAV y su test de contrato
- [x] Tests unitarios de las transiciones de la orden y de integración contra PostgreSQL real

Quedó pendiente (anotado en ADR-009, sección *Pendiente*): circuit breaker en los pedidos, pasar a `EN_REVISION` un intento sin respuesta en 24 h, y la URL pública del webhook para el FAV desplegado (parte de la nube).

Documentación:
- [x] **ADR-004 (D4, consistencia y concurrencia):** es el único obligatorio que todavía no existe
- [x] ADR-002 actualizado con el segundo patrón ya implementado
- [x] ADR-009 (pedidos) a "aceptado"
- [x] `SPEC.md`: HU de stock y reposición de "propuesto" a "implementado"

### Integrante 4: Entrada, frontend, nube y operación

Rama: `feat/frontend-nube`. Es lo que se ve en la presentación.

Gateway:
- [x] Login de usuario (JWT) para el frontend, sin romper el `X-API-Key` del grupo de paneles solares
- [x] Ruta hacia `stock-service` (`/api/v1/stock/`, sin el webhook)
- [x] Ruta hacia búsqueda (`/api/v1/productos/buscar`, la sumó la parte 2; con el merge acepta también el JWT)
- [x] Límite de tráfico (token bucket por clave) con `429` en el contrato (1.1.0)

Frontend:
- [x] Web simple: login → elegir producto → ver el ranking de proveedores → reservar stock. **Llama solo al gateway**, nunca a FAV. Embebida en el gateway (`/`)
- [x] Búsqueda por texto en vez de la lista de SKUs: resultados mientras se escribe, con tolerancia a errores de tipeo; si Meilisearch no responde, se puede escribir el SKU exacto

Nube:
- [x] Override `docker-compose.nube.yml`, script `infra/nube/instalar.sh` y guía `docs/NUBE.md`
- [ ] Crear la VM y poner la URL pública en `README.md`, `docs/NUBE.md` y `docs/contracts/README.md`
- [ ] Pasarle la URL y la clave al grupo de paneles solares

Operación:
- [x] Métricas (Prometheus) y tablero (Grafana) con tasa, errores y latencia de la capacidad
- [x] Alertas: p95 > 300 ms, errores > 1 % y sin réplicas sanas (esta última probada: dispara en 48 s)
- [x] Pipeline de CI (GitHub Actions) que corre los tests en cada push (sin probar todavía en GitHub)

Documentación:
- [x] ADR-008 actualizado (`429`; la URL pública cuando exista) y ADR-011 completo (métricas, objetivo de servicio, alertas)
- [x] ADR-010 con el límite de tráfico implementado y medido
- [x] `ARCHITECTURE.md`: diagramas con los servicios nuevos y limitaciones actualizadas

## Cronograma

| Cuándo | Integrante 1 (Fede) | Integrante 2 | Integrante 3 | Integrante 4 |
|---|---|---|---|---|
| **Mar 3 · noche** | Reunión de 30 min entre los cuatro: leer este plan, validar los 3 servicios y crear las ramas. | | | |
| **Mié 4** | Compose con la infraestructura y formato del evento → `main` | Medición "antes" y caché Redis | Esqueleto de `stock-service` y reserva con transacción | Login JWT, límite de tráfico y VM en la nube |
| **Jue 5** | `import-service` con FAV y MongoDB | Consumidor del evento, Meilisearch y búsqueda | Reposición, pedidos a FAV y webhook | Frontend, métricas, tablero y CI |
| **Jue 5 · 23:30** | Checkpoint: todo a `main`, `docker compose up` levanta en dos computadoras y la nube responde. | | | |
| **Vie 6 · mañana** | Test de contrato y ADR | Medición "después" y ADR | Test de concurrencia y ADR-004 | Alerta, URL pública en los docs |
| **Vie 6 · −2 h** | **Congelamiento del código.** Tag `entrega-3` y ensayo de la demo desde un clon limpio. | | | |
| **Sáb 7 → Mar 10** | Ensayo del **Game Day** (caída de PostgreSQL, de una réplica y de FAV) y `docs/POSTMORTEM.md` entre los cuatro. Cada uno repasa su parte para la defensa. | | | |

## Demo de la Entrega 3 (5 minutos)

1. Mostrar la URL pública respondiendo la capacidad
2. Un producto de FAV apareciendo en el ranking (consumo real)
3. Caché: la misma consulta con y sin Redis, y el gráfico antes y después
4. Apagar FAV (o simularlo) y mostrar que el ranking sigue con la última importación válida
5. Recorrer rápido los ADR que pasaron a "aceptado"

## Pendiente con gente de afuera (al 10/11)

**A los profes**
- ¿Alcanza con tres servicios sin `catalog-service`? Sin esta respuesta [ADR-014](adr/ADR-014-tres-servicios-sin-catalogo.md) no puede pasar de "propuesto", y es el único ADR que sigue sin aceptar.

**Grupo de ropa (FAV)**
- **No llegó nada**: ni URL desplegada, ni clave, ni secreto de avisos (el contrato los marca **[?]**). Se aplicó el plan B de *Riesgos*: todo el consumo está probado contra `fav-mock`, que cumple su OpenAPI v1.1.0, y los tests de contrato pasan. Con `FAV_URL` en `.env` se apunta al real sin cambiar código.

**Grupo de paneles solares**
- **Sigue pendiente** avisarles del `429` del contrato 1.1.0 y pasarles la URL pública, que todavía no existe. Mientras tanto consumen la instancia local.

## Riesgos

- **Si FAV no tiene su URL el 6/11**, `import-service` y `stock-service` se prueban contra un mock que cumpla su OpenAPI, y se deja escrito. El test de contrato tiene que pasar igual.
- **Si la nube se complica**, subir solo el gateway, `proveedores-service` y PostgreSQL: es lo único que la consigna exige que esté en la nube.
- **Si el tiempo no alcanza**, el orden para recortar es: primero el tablero lindo, después los filtros extra de la búsqueda. Nunca la reserva sin sobreventa, el evento ni el consumo de FAV.
