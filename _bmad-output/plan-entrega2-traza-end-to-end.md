---
title: 'Traza distribuida end-to-end: instrumentar proveedores-service hasta Postgres (Entrega 2, Integrante 2)'
type: 'feature'
ticket: ''
created: '2026-10-22'
status: 'built'
baseline_revision: 'af3a673ff6bb5cb930395bfb5106611a307ad265'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/docs/PLAN-ENTREGA-2.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** La consigna 6.3 exige para esta entrega "una primera traza distribuida del flujo principal", y el paso 5 de la demo es seguir el mismo id desde los logs hasta Jaeger. Hoy la traza se corta en la llamada de salida del gateway: `proveedores-service` no importa OpenTelemetry en ningún archivo, así que en Jaeger no hay tramo de servicio ni de Postgres. Es también el último pendiente del Integrante 1 en el plan de la entrega.

**Approach:** Instrumentar `proveedores-service` con el paquete compartido que ya existe: logger correlacionado, `IniciarTrazas`, `otelhttp` sobre el handler y un `QueryTracer` de pgx que abra un span por consulta. Como hay que tocar su Dockerfile para que vea `pkg/`, se aprovecha para darle su propia sonda de healthcheck y borrar del compose el andamio del `busybox` compartido. Tres partes; al cerrar cada una, commit sugerido al usuario.

**Decisiones del usuario al aprobar:**
- Los cambios en `services/proveedores-service` van en **un commit aislado**, para que el merge con la rama del Integrante 1 se lea bien. Los commits del plan de plataforma ya están hechos y el árbol arrancó limpio.
- **Renegociado después de la revisión (pass 1, hallazgo 7):** la fila "Base caída" pedía que quedara marcado el **span de la consulta**, y eso no puede pasar: `pool.Query` falla al tomar la conexión y nunca llega a ejecutar una consulta. Queda ratificado que la falla se vea como un span `postgres connect` en rojo dentro de la traza del 503, que además dice con más precisión dónde estuvo el problema. La fila quedó reescrita en esos términos.

## Boundaries & Constraints

**Always:**
- Los cambios en `proveedores-service` son aditivos y mínimos: no se toca `internal/domain`, ni `internal/ports`, ni sus tests, ni la lógica de ranking, ni el SQL, ni el contrato de sus respuestas.
- Sin dependencias nuevas más allá de OpenTelemetry (`otel`, `otel/sdk`, `otlptracehttp` v1.47.0, `otelhttp` v0.72.0). El span de Postgres sale de un `pgx.QueryTracer` propio, sin librerías de terceros.
- `pkg/observabilidad` no cambia su API: `NuevoLogger` e `IniciarTrazas` se usan tal como quedaron.
- El servicio tiene que seguir arrancando y respondiendo sin colector OTLP, y `go test ./...` tiene que seguir corriendo sin Docker.
- El `traceparent` que manda el gateway se continúa: una request por el gateway produce **una sola traza**, no dos.
- Los tests que ya existen del Integrante 1 pasan sin que se cambie ninguna de sus expectativas, incluidos los de integración con `-tags=integration`.
- Pausar al final de cada parte (A span de Postgres · B arranque e imagen · C verificación) y dar el commit sugerido. No commitear ni pushear nunca.

**Never:**
- No tocar el `api-gateway` ni `pkg/observabilidad` salvo que la verificación muestre un defecto en ellos.
- No tocar el OpenAPI ni el README raíz (Integrante 3), ni los ADR (son un plan aparte).
- Sin métricas, tablero ni alertas; sin cambiar la regla de negocio ni el seed.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Traza completa | request por el gateway con Jaeger arriba | **una** traza con spans anidados: gateway server → gateway client → servicio server → consulta a Postgres | — |
| Logs del servicio | la misma request | cada línea JSON del servicio trae `service`, `request_id` y el **mismo** `trace_id` que la del gateway | — |
| Sin colector | `OTEL_EXPORTER_OTLP_ENDPOINT` vacío | el servicio arranca y responde 200 igual; trazas no-op | log info |
| Base caída | Postgres detenido | la falla de base queda marcada con error dentro de la traza y la respuesta sigue siendo 503 `NO_DISPONIBLE` | sin panic |
| SKU inexistente | `producto=NO-EXISTE` | span de consulta normal (no es falla de base) y 404 `PRODUCTO_NO_ENCONTRADO` | — |
| Consulta sin datos sensibles | cualquier consulta | el span lleva el SQL y la tabla, **nunca** los valores de los parámetros | — |
| Tests del Integrante 1 | `go test ./...` y con `-tags=integration` | pasan sin cambios en sus expectativas | — |
| Healthcheck propio | contenedor del servicio arriba | queda `healthy` con una sonda de su propia imagen, sin volumen compartido | — |

</frozen-after-approval>

## Code Map

- `internal/adapters/postgres/repo.go:34-41` -- `NuevoPool` hace `pgxpool.ParseConfig` y setea `cfg.ConnConfig.ConnectTimeout`. **`cfg.ConnConfig.Tracer` es el enganche del tracer**; es la única línea que hay que agregar ahí.
- `internal/adapters/postgres/repo.go:60-64` -- `OfertasPorProducto` ya le pasa el `ctx` de la request a `pool.Query`, así que el span de la consulta cuelga solo del span del servicio. No hay que cambiar firmas.
- `pgx.QueryTracer` (pgx v5.11.0, verificado en el módulo) -- `TraceQueryStart(ctx, *pgx.Conn, pgx.TraceQueryStartData{SQL, Args}) context.Context` y `TraceQueryEnd(ctx, *pgx.Conn, pgx.TraceQueryEndData{CommandTag, Err})`. El `ctx` que devuelve `Start` es el que recibe `End`: ahí viaja el span.
- `cmd/server/main.go:59` -- `slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", …)`: se reemplaza por `observabilidad.NuevoLogger`. `main.go:30-56` es la config por env; `main.go:74,121-125` el apagado ordenado, donde entra el `shutdown` de las trazas.
- `internal/adapters/http/handler.go:26,35` -- `NuevoHandler(cfg Config) http.Handler` devuelve el mux ya envuelto en `conRequestLog`: envolver **ese** handler con `otelhttp.NewHandler` desde `main.go` deja el span del servidor por fuera del log de request, que es lo que hace que la línea de log salga con `trace_id`.
- `Dockerfile:4-20` -- contexto propio, `COPY go.mod go.sum ./` y `COPY . .` sin prefijo; pasa a contexto de raíz como el del gateway (`services/api-gateway/Dockerfile` es el modelo a imitar, incluida la segunda compilación para la sonda).
- `docker-compose.yml` -- el ancla `x-proveedores` monta el volumen `sonda` y depende del servicio `sonda`; el `build.context` de la réplica 1 apunta al directorio del servicio. Las tres cosas cambian.
- `pkg/observabilidad/{logger,trazas}.go` -- `NuevoLogger(servicio)` e `IniciarTrazas(ctx, servicio)`, ya verificados. Se usan, no se tocan.

## Tasks & Acceptance

**Execution:**
- [x] **Parte A** `internal/adapters/postgres/tracer.go` -- `QueryTracer` que abre un span por consulta con `db.system`, `db.statement` (el SQL, sin los args) y registra el error en `TraceQueryEnd` -- va acá y no en `pkg/observabilidad` para no meter pgx en el grafo del gateway
- [x] **Parte A** `internal/adapters/postgres/repo.go` -- enganchar el tracer en `NuevoPool` vía `cfg.ConnConfig.Tracer`
- [x] **Parte A** `internal/adapters/postgres/tracer_test.go` -- el span se abre y se cierra, lleva el SQL y no los parámetros, y marca error cuando `TraceQueryEnd` trae uno
- [x] **Parte B** `cmd/server/main.go` -- `observabilidad.NuevoLogger`, `IniciarTrazas` con su `shutdown` en el apagado, y `otelhttp.NewHandler` envolviendo el handler del servicio
- [x] **Parte B** `go.mod`, `go.sum` -- agregar el paquete compartido por `replace` relativo y las dependencias de OpenTelemetry, como hizo el gateway
- [x] **Parte B** `cmd/sonda/main.go`, `Dockerfile` -- sonda propia y Dockerfile con contexto de raíz, copiando `pkg/observabilidad` y el servicio
- [x] **Parte B** `docker-compose.yml` -- `build.context` a la raíz con `dockerfile:`, healthcheck con la sonda propia, y borrar el servicio `sonda`, su volumen y el `depends_on` que lo espera
- [x] **Parte C** `docs/observabilidad/traza-entrega-2.png`, `docs/observabilidad/README.md` -- captura de la traza con los cuatro spans y el procedimiento para reproducirla en la demo

**Acceptance Criteria:**
- Given el sistema levantado con `docker compose up -d --build`, when se consulta la capacidad por el gateway, then en Jaeger hay **una sola traza** con spans de `api-gateway` y de `proveedores-service`, y dentro del segundo el span de la consulta a Postgres.
- Given esa misma request, when se comparan los logs de los dos contenedores, then las líneas de ambos comparten `trace_id` y `request_id`.
- Given `go test ./...` y `go test -tags=integration ./internal/adapters/postgres/` en el servicio, then pasan sin que se haya cambiado ninguna expectativa previa.
- Given `docker compose ps`, then los cinco contenedores quedan `healthy` y ya no existe el servicio `sonda` ni su volumen.
- Given `git diff` del commit, then no hay cambios en `internal/domain`, `internal/ports` ni en los tests que ya existían.

## Implementation Notes

**Span de conexión, además del de la consulta (desvío del plan, Parte A).** La
fila "Base caída" de la matriz no se cumplía con solo el `QueryTracer`: con
`docker compose stop postgres`, `pgxpool.Query` falla al **tomar la conexión** y
pgx nunca llega a llamar a `TraceQueryStart`, así que la traza del 503 no decía
en ninguna parte que el problema fuera Postgres (verificado: la traza quedaba con
3 spans y el error solo en el span del servicio). El mismo tipo implementa
también `pgx.ConnectTracer` y abre `postgres connect`, que en ese caso sale rojo
y con el motivo. En el camino feliz no aparece: el pool ya tiene su conexión
abierta desde las migraciones, así que la traza queda en los cuatro spans que
pide el criterio de aceptación (verificado en varias requests seguidas, incluida
la primera después de recuperar la base).

Como el tipo ahora cubre dos interfaces de pgx, se llama `Tracer` y no
`QueryTracer`; el archivo sigue siendo `tracer.go`.

**No se trazan las sondas ni las consultas sin traza en curso.** El healthcheck
del contenedor pega a `/readyz` y el gateway a `/healthz` cada 5 s. Sin filtro,
cada una abría una traza de un span —y el `Ping` de `/readyz` otra más para la
consulta—, unas 48 trazas de ruido por minuto que tapaban en Jaeger la que
cuenta el flujo (y rompían la verificación por `?service=proveedores-service`).
Dos decisiones chicas lo evitan: `otelhttp.WithFilter` descarta `/healthz` y
`/readyz`, y el tracer de pgx solo abre span si el contexto ya trae uno (lo que
además deja fuera las migraciones del arranque). Verificado: una request, una
traza, cero ruido.

**`db.sql.table` sale de un reconocedor mínimo**, no de un parser: el
identificador que sigue al primer `FROM`. Si no hay `FROM` el atributo no se
agrega. Es lo que hace que el span se llame `SELECT productos` y no algo
genérico.

**Documentación que quedó desactualizada por este cambio.** Tres viñetas de
`services/api-gateway/README.md` describían el andamio del `busybox`, el contexto
de build del servicio y el motivo de no correr `go work sync` (OpenTelemetry
1.44 vs 1.47); las tres pasaron a ser falsas y se corrigieron. En
`services/proveedores-service/README.md` se arregló el `docker build`, que con el
contexto nuevo ya no funcionaba. `services/proveedores-service/.dockerignore`
quedó muerto —con contexto de raíz manda el de la raíz— y se borró.

## Plan Change Log

- **2026-10-22** -- Parte A suma el span del intento de conexión
  (`pgx.ConnectTracer`) y el tipo pasa a llamarse `Tracer`. Motivo: sin eso la
  fila "Base caída" de la matriz no se cumple. Ver Implementation Notes.

## Review Triage Log

**Pass 1 (quick):** high 0 · medium 2 · low 11 · false 0 · maybe-false 0

| # | Hallazgo | Veredicto | Ruta | Evidencia / acción |
|---|---|---|---|---|
| 1 | `.dockerignore`: `.env` dejó de ser recursivo y el `.dockerignore` del servicio que cubría el hueco se borró | low | patch | Real: los patrones de `.dockerignore` se anclan en la raíz del contexto (el propio archivo usa `**/` para los que sí deben serlo), y git ignora `.env` en cualquier nivel, así que un `services/<svc>/.env` sería invisible y entraría a la imagen. Corregido a `**/.env` y `**/.env.*`. |
| 2 | El bullet del README del gateway sobre `go.work.sum` y `x/crypto` quedó falso con este cambio | low | patch | Verificado: el `go.mod` del servicio ahora fija `crypto v0.57.0` y `sync v0.23.0`, exactamente lo que resuelve el workspace. El bullet describía una condición que ya no existe; se borró. |
| 3 | Se tocó `services/api-gateway/README.md` contra el "Never", y varias ediciones no están en la lista de tareas | low | rechazado | Real como contabilidad. Esas ediciones del README del gateway las hice yo en el turno anterior por pedido explícito del usuario, antes de que este plan existiera; el implementador solo las actualizó. El contenido falso se arregla en el hallazgo 2. |
| 4 | Los docs se contradicen: "la primera request sale con tres spans" contra "tres requests seguidas dan tres trazas de cuatro spans" | medium | patch | Real, y el texto era mío. Las dos frases describían dos experimentos distintos. Reescrito: una request puede salir sin span de base según qué conexión del pool le toque, con el dato medido, y la instrucción para la demo pasa a "dos o tres requests y mostrar una con `postgres connect`". |
| 5 | El camino feliz puede traer cinco spans, no cuatro, si hay requests en paralelo | low | patch | Real: el pool abre conexiones a demanda y la request que abre una carga con el span `postgres connect`. Importa para la corrida de k6 del D13. Documentado en la tabla de casos. |
| 6 | El span de conexión puede sobrevivir a su padre si Postgres cuelga en vez de rechazar | low | rechazado | Real según el manejo de contexto de puddle, pero exige una base colgada (no rechazada), el efecto es cosmético en una captura, y el arreglo es reestructurar el manejo del contexto, no una corrección directa. |
| 7 | La fila congelada "Base caída" pide que **el span de la consulta** quede marcado con error, y eso nunca pasa | medium | intent_gap | Real y verificado: `pool.Query` falla al tomar la conexión y nunca llega a `TraceQueryStart`, así que no hay span de consulta que marcar; en su lugar aparece `postgres connect` en rojo. El comportamiento es correcto —y mejor—, pero la fila está en el bloque congelado. Decide el usuario. **No se revierte el código antes de que decida.** |
| 8 | La justificación de por qué no se traza el `Ping` de `/readyz` es incorrecta | low | patch | Verificado en el código de pgx: `Conn.Ping` llama a `pgConn.Ping`, que no pasa por el `QueryTracer`. El guard sigue haciendo falta para las migraciones, pero por otro motivo. Corregido en los docs. |
| 9 | `db.rows_affected` se omite cuando son 0 filas | low | patch | Real: el `else if filas > 0` dejaba el caso `PRODUCTO_NO_ENCONTRADO` sin el atributo, indistinguible de un tracer que no mide filas. Se emite siempre. |
| 10 | Los nombres de atributos son de una generación de semconv anterior a la que usa el resto del repo | low | rechazado | Real, pero los nombres los fijó la tarea del propio plan, no hay consumidor que dependa de ellos, y cambiarlos obliga a tocar tests y documentación sin ganancia visible para nadie. |
| 11 | En el `go.mod` del servicio hay requerimientos directos dentro del bloque de indirectos | low | rechazado | Real y cosmético: `go mod tidy` lo reordena. No se toca el `go.mod` a un día de la entrega por una cuestión de formato. |
| 12 | El volumen y el contenedor `sonda` viejos no se limpian con ningún procedimiento documentado | low | patch | Real en una máquina que ya levantó el compose anterior: compose no borra volúmenes que dejó de declarar. Se documentó `--remove-orphans` en el comando y el `docker volume rm` del volumen huérfano. |
| 13 | El commit aislado del servicio no está garantizado por el cambio; todo está en un solo árbol | — | rechazado | No es un defecto del código sino una instrucción para el momento de commitear. Se respeta al proponer los commits, teniendo en cuenta que el `Dockerfile` del servicio y el `build.context` del compose tienen que viajar juntos o el build se rompe. |

## Design Notes

El `QueryTracer` vive en el adaptador de Postgres del servicio, no en `pkg/observabilidad`: implementarlo exige importar `pgx`, y el paquete compartido lo importa el gateway, que no tiene base de datos. Dejarlo acá mantiene pgx donde ya estaba y el grafo del gateway limpio. Si algún día hay un segundo servicio con Postgres, se promueve a un paquete compartido propio (`pkg/pgxtrace`), no al de observabilidad.

El `otelhttp.NewHandler` se aplica **por fuera** del handler que devuelve `NuevoHandler`, desde `main.go`. Así el span del servidor ya existe cuando corre el middleware de log del Integrante 1, y su línea de request sale con `trace_id` sin tocar su middleware.

`db.statement` lleva el SQL pero nunca los `Args`: el SKU es inocuo, pero la convención evita que mañana un parámetro sensible termine en la traza.

## Verification

**Commands:**
- `go vet ./...` y `go test ./... -cover` en el servicio -- ✅ pasan; dominio 100 % y http 95.5 % sin cambios, `postgres` sube de "no test files" a 43.8 % por los tests del tracer
- `go vet -tags=integration ./...` -- ✅ pasa
- `go test -tags=integration ./internal/adapters/postgres/ -count=1` -- ⚠️ pasa solo con `TESTCONTAINERS_RYUK_DISABLED=true` (87.6 % de cobertura). Sin esa variable falla al levantar el reaper de testcontainers, **igual que en el revisión base `af3a673`** (comprobado en un worktree limpio): es del entorno de Docker Desktop, no de este cambio
- `docker compose down -v; docker compose up -d --build; docker compose ps` -- ✅ cinco `healthy`, sin servicio `sonda`. Ojo: `down -v` no borra el volumen `sonda` de corridas viejas porque ya no está declarado; se borra a mano con `docker volume rm arq-soft-proveedores_sonda`
- `curl -H "X-API-Key: ..." ".../recomendados?producto=PAN-450W-MONO&cantidad=10"` -- ✅ 200
- `curl -s "http://localhost:16686/api/traces?service=proveedores-service"` -- ✅ una sola traza, con los cuatro spans y `SELECT productos` dentro del span del servicio (`db.system`, `db.statement` con `$1`, `db.sql.table=productos`, `db.rows_affected=3`)
- `docker compose logs api-gateway proveedores-service-1 | Select-String <request_id>` -- ✅ las dos líneas con el mismo `trace_id` y el mismo `request_id`
- `docker compose stop postgres`, repetir el `curl` -- ✅ 503 `NO_DISPONIBLE` y el span `postgres connect` en rojo dentro del span del servicio (ver Implementation Notes)
- `producto=NO-EXISTE` -- ✅ 404 `PRODUCTO_NO_ENCONTRADO` con los cuatro spans y el de la consulta sin error
- contenedor del servicio con `OTEL_EXPORTER_OTLP_ENDPOINT` vacío -- ✅ arranca, loguea `trazas deshabilitadas` y responde 200
