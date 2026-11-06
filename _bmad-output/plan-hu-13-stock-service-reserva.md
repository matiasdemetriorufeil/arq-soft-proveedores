---
title: 'HU-13 — stock-service con reserva sin sobreventa'
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
  - '{project-root}/docs/adr/ADR-002-arquitectura-interna.md'
  - '{project-root}/docs/adr/ADR-003-persistencia.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** La consigna exige un segundo patrón de arquitectura interna reconocible y una operación crítica con garantías de consistencia y concurrencia (D4), y hoy no existe `stock-service`: HU-13 está en `SPEC.md` como *propuesto* y ADR-002/ADR-003 proponen el servicio sin código que los respalde.

**Approach:** Un servicio nuevo `services/stock-service` en capas explícitas (handler → service → repository), con su propia base PostgreSQL `stock`, cuya única operación de negocio es reservar unidades con `SELECT … FOR UPDATE` dentro de una transacción, idempotente por `Idempotency-Key`. Se cierra con los ADR que la evidencia habilita a pasar de propuesto a aceptado.

## Boundaries & Constraints

**Always:**
- Capas separadas por paquete y dependencia en un solo sentido: `internal/handler` → `internal/service` → `internal/repository`. La transacción la abre `service`; el `FOR UPDATE` lo ejecuta `repository`. Ningún paquete fuera de `repository` importa pgx.
- La base es `stock`, con el usuario `STOCK_DB_USER` que ya es OWNER (`infra/postgres/init/10-bases-por-servicio.sh`). Nunca se toca la base `proveedores`.
- Migraciones embebidas (`//go:embed *.sql`), idempotentes, aplicadas al arrancar bajo advisory lock con un ID **distinto** al de `proveedores-service` (que usa `727_001`).
- Convenciones del repo, verificables en `proveedores-service`: código y comentarios en español, errores `application/problem+json` con campo `code`, `X-API-Key` comparada con `crypto/subtle`, `/healthz` siempre 200 y `/readyz` contra la base, `cmd/sonda` como healthcheck de Docker, Dockerfile con contexto en la raíz y `replace` hacia `pkg/observabilidad`, `observabilidad.NuevoLogger` + `IniciarTrazas` antes de abrir el pool.
- `main` tiene que seguir levantando con `docker compose up`.

**Never:**
- Nada de reposición: punto de reorden, órdenes, máquina de estados ni llamadas a `proveedores-service` (objetivo B diferido).
- Nada de FAV: cliente de pedidos, webhook HMAC ni endpoints nuevos en el mock (objetivo C diferido).
- No se agregan rutas al `api-gateway` ni se publica el puerto de `stock-service` al host: eso es del Integrante 4.
- No se resuelve la sobreventa con locks en memoria, `SERIALIZABLE` con reintentos, ni restándole al stock sin bloquear la fila. La garantía la da el bloqueo de fila.
- No se toca `proveedores-service`, `import-service` ni `pkg/`.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|---|---|---|---|
| Reserva válida | SKU con `disponible` 10, body `cantidad` 6, `Idempotency-Key` nueva | `201` con la reserva (`id`, `sku`, `cantidad`, `estado` `CONFIRMADA`), `disponible` queda 4 y `reservado` 6 | No error |
| Reserva repetida por reintento | Misma `Idempotency-Key` que una reserva ya aplicada | `200` con la **misma** reserva; el stock no se mueve una segunda vez (CA-13.2) | No error |
| Dos reservas simultáneas | `disponible` 10, dos reservas de 6 a la vez | Una `201`, la otra `409`; `disponible` nunca queda negativo (CA-13.1) | `409 STOCK_INSUFICIENTE` |
| Stock insuficiente | `disponible` 4, `cantidad` 6 | `409` con el disponible en `detail` | `409 STOCK_INSUFICIENTE` |
| SKU inexistente | `cantidad` 1 sobre un SKU que no está en `stock_productos` | `404` | `404 PRODUCTO_NO_ENCONTRADO` |
| Body o cantidad inválidos | `cantidad` 0, negativa, no entera, JSON roto, o falta `Idempotency-Key` | `400` con el detalle de qué falta | `400 PARAMETRO_INVALIDO` |
| Sin API key | Falta `X-API-Key` o no es válida | `401` | `401 NO_AUTORIZADO` |
| Base caída | PostgreSQL no responde | `503` antes del timeout de 2 s; `/readyz` también `503` | `503 NO_DISPONIBLE` |
| Consulta de stock | `GET` de un SKU existente | `200` con `sku`, `disponible`, `reservado`, `minimo` | `404` si no existe |

</frozen-after-approval>

## Code Map

Lo que hay que imitar (no modificar nada de esto):

- `services/proveedores-service/cmd/server/main.go` — patrón de arranque a copiar: `cargarConfig` leyendo env con `strings.TrimSpace` y error si falta una obligatoria; `observabilidad.NuevoLogger` + `slog.SetDefault` + `IniciarTrazas` **antes** de `NuevoPool`; `migrarConReintentos` (15 intentos × 2 s); `otelhttp.NewHandler` con `WithFilter` que excluye `/healthz` y `/readyz`; `http.Server` con `ReadHeaderTimeout` 5 s y `ErrorLog` desde el handler de slog; apagado ordenado con `timeoutApagado` 10 s.
- `services/proveedores-service/internal/adapters/http/problem.go` — struct `Problem` (RFC 9457: `type`, `title`, `status`, `detail`, `code`, `instance`, `request_id`), `escribirProblema`, `contentTypeProblem`, constantes `CodigoParametroInvalido`/`CodigoNoAutorizado`/`CodigoProductoNoEncontrado`/`CodigoNoDisponible`. Se replica tal cual y se **suma** `CodigoStockInsuficiente = "STOCK_INSUFICIENTE"`.
- `services/proveedores-service/internal/adapters/http/middleware.go` — `conRequestLog` y `conAPIKey` (`headerAPIKey = "X-API-Key"`, `subtle.ConstantTimeCompare`).
- `services/proveedores-service/internal/adapters/postgres/repo.go` — `NuevoPool`: `pgxpool.ParseConfig`, `ConnConfig.ConnectTimeout = TimeoutConsulta`, `ConnConfig.Tracer = NuevoTracer(nil)`.
- `services/proveedores-service/internal/adapters/postgres/tracer.go` — el `pgx.QueryTracer`/`ConnectTracer` vive en el servicio, no en `pkg/observabilidad` (que a propósito no importa pgx). Hay que copiar este archivo al servicio nuevo.
- `services/proveedores-service/internal/adapters/postgres/migrate.go` — `Migrar(ctx, pool, fs.FS, *slog.Logger)` con `pgx.BeginFunc` + `pg_advisory_xact_lock`; sin tabla de control, los `.sql` son idempotentes y se reaplican enteros.
- `services/proveedores-service/internal/adapters/postgres/main_integration_test.go` — `//go:build integration` en la línea 1, `tcpostgres.Run(ctx, "postgres:16-alpine", …)`, `ctr.ConnectionString(ctx, "sslmode=disable")`, pool compartido vía `TestMain`.
- `services/proveedores-service/Dockerfile` — multi-stage, contexto en la raíz, copia `pkg/observabilidad/` y el servicio, compila `./cmd/server` y `./cmd/sonda`, runtime `gcr.io/distroless/static-debian12:nonroot`.
- `services/proveedores-service/migrations/002_seed.sql` — los 30 SKUs del seed de demostración; el seed de stock usa esos mismos SKUs para que la demo sea coherente.
- `pkg/observabilidad` — solo dos funciones exportadas: `IniciarTrazas(ctx, servicio) (func(context.Context) error, error)` y `NuevoLogger(servicio) *slog.Logger`.

Lo que hay que modificar:

- `docker-compose.yml` — agregar el servicio `stock-service`: `build` con contexto `.` y `dockerfile: services/stock-service/Dockerfile`, `DATABASE_URL` hacia `postgres:5432/${STOCK_DB}` con `STOCK_DB_USER`/`STOCK_DB_PASSWORD`, `API_KEYS`, `PORT=8080`, `OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318`, `depends_on: postgres: condition: service_healthy`, `healthcheck: ["CMD", "/sonda"]`, red `interna`, **sin** `ports`.
- `go.work` — sumar `./services/stock-service` al bloque `use`.

## Tasks & Acceptance

**Execution:**
- [x] `services/stock-service/go.mod` — módulo `…/services/stock-service`, go 1.26.1, `pgx/v5 v5.11.0`, `otelhttp v0.72.0`, `otel v1.47.0`, testcontainers v0.44.0, y el `replace` hacia `../../pkg/observabilidad` — mismas versiones que `proveedores-service` para no partir el workspace.
- [x] `services/stock-service/migrations/001_init.sql` + `embed.go` — `stock_productos (sku PK, disponible INT NOT NULL CHECK (disponible >= 0), reservado INT NOT NULL DEFAULT 0 CHECK (reservado >= 0), minimo INT NOT NULL DEFAULT 0, actualizado_en)` y `reservas (id UUID PK, sku FK, cantidad INT CHECK (cantidad > 0), clave_idempotencia TEXT NOT NULL UNIQUE, estado TEXT, creado_en)`. El `CHECK (disponible >= 0)` es la red de seguridad de la base: aunque falle la lógica, la sobreventa no entra.
- [x] `services/stock-service/migrations/002_seed.sql` — los 30 SKUs del seed de `proveedores-service` con stock y mínimo deterministas, `ON CONFLICT DO NOTHING`.
- [x] `services/stock-service/internal/repository/repositorio.go` — `Repo` sobre `*pgxpool.Pool`: `NuevoPool`, `Ping`, `StockPorSKU`, `ReservaPorClave`, y dentro de una transacción recibida `BloquearStock` (`SELECT … FOR UPDATE`), `AplicarReserva`, `InsertarReserva`. Expone `EnTransaccion(ctx, func(Tx) error)` para que `service` controle el límite transaccional sin importar pgx.
- [x] `services/stock-service/internal/repository/migrate.go` + `tracer.go` — copia adaptada de las de `proveedores-service`, con `advisoryLockID = 727_002`.
- [x] `services/stock-service/internal/service/stock.go` — `Reservar(ctx, sku, cantidad, claveIdempotencia)`: resuelve la clave repetida devolviendo la reserva existente sin tocar el stock, y si es nueva abre la transacción, bloquea la fila, valida el disponible y escribe reserva + stock de una sola vez. Errores de negocio propios (`ErrStockInsuficiente`, `ErrProductoNoEncontrado`, `ErrParametroInvalido`) sin tipos HTTP.
- [x] `services/stock-service/internal/handler/*.go` — `handler.go` con el mux (`POST /api/v1/stock/reservas` y `GET /api/v1/stock/{sku}` detrás de `conAPIKey`, más `/healthz` y `/readyz`), `problem.go` y `middleware.go` copiados y extendidos con `STOCK_INSUFICIENTE`. Traduce cada error del service a su status.
- [x] `services/stock-service/cmd/server/main.go` + `cmd/sonda/main.go` — arranque calcado del de `proveedores-service`, con `nombreServicio = "stock-service"`.
- [x] `services/stock-service/internal/service/stock_test.go` — unit tests con un repositorio falso: idempotencia, stock insuficiente, SKU inexistente, cantidad inválida, y que el camino feliz abra exactamente una transacción.
- [x] `services/stock-service/internal/handler/handler_test.go` — unit tests de la traducción de errores a `problem+json` con sus `code`, y del 401 sin API key.
- [x] `services/stock-service/internal/repository/{main,repo}_integration_test.go` — `//go:build integration`. Usa `STOCK_TEST_DATABASE_URL` si está definida y cae a testcontainers si no, porque en esta máquina no hay Docker y el test de concurrencia es la evidencia de CA-13.1.
- [x] `services/stock-service/internal/repository/concurrencia_integration_test.go` — el test que prueba RN-13: `disponible` 10 y N=20 reservas de 6 en goroutines simultáneas contra PostgreSQL real; exactamente 1 éxito, 19 rechazos, `disponible` final 4 y `reservado` 6.
- [x] `services/stock-service/Dockerfile` + `README.md` — Dockerfile calcado (contexto en la raíz, dos binarios, distroless); README con las variables, los endpoints y ejemplos `curl`.
- [x] `docker-compose.yml`, `go.work` — alta del servicio según el Code Map.
- [x] `docs/adr/ADR-004-consistencia-y-concurrencia.md` — ADR nuevo con la plantilla de `docs/adr/TEMPLATE.md`: opciones evaluadas (bloqueo pesimista con `FOR UPDATE`, optimista por versión, `SERIALIZABLE` con reintentos, `UPDATE … WHERE disponible >= n` condicional), por qué se eligió el pesimista, la idempotencia por clave, y la medición del test de concurrencia.
- [x] `docs/adr/ADR-002-arquitectura-interna.md`, `docs/adr/ADR-003-persistencia.md`, `SPEC.md` — pasar el segundo patrón y la base `stock` de propuesto a implementado, con la ruta de los paquetes como evidencia, y HU-13 a *implementado* con CA-13.1 y CA-13.2 señalando sus tests. No se toca nada de HU-14 ni HU-15.

**Acceptance Criteria:**
- Given el repo en esta rama, when se corre `go build ./...` y `go vet ./...` en el servicio nuevo, then compila sin errores y el workspace sigue resolviendo.
- Given `go list -deps ./internal/service`, when se inspecciona la salida, then no aparece `jackc/pgx`: la capa de servicio no conoce el motor.
- Given un `ADR-004` escrito, when el revisor automático lo compara con el código, then cada afirmación apunta a un archivo que existe y a un test que la respalda.
- Given el servicio dado de alta en el compose, when se revisa el YAML, then `stock-service` no publica puertos al host y depende de `postgres` sano.

## Implementation Notes

**Un criterio de aceptación no se cumple como estaba escrito.** El criterio decía que `go list -deps ./internal/service` no tenía que mostrar `jackc/pgx`. Es incompatible con el propio diseño del plan: las *Design Notes* mandan que `service` use `repository.Tx`, y las *Boundaries* (congeladas) mandan que la dependencia vaya en un solo sentido `handler → service → repository`. Con `service` importando `repository`, pgx aparece **transitivamente** en `go list -deps`, sí o sí. Se priorizó la restricción congelada y la forma del código que fija el plan. Lo que sí se verifica, y es lo que el criterio buscaba:

```
go list -f '{{join .Imports "\n"}}' ./internal/service
# → context, crypto/rand, encoding/hex, errors, fmt, strings, …/internal/repository
```

`internal/repository` es el único paquete que importa pgx y el único que escribe SQL; `service` solo ve el tipo opaco `repository.Tx`, cuyo único método no está exportado, así que no puede ejecutar nada con él. Si el grupo quiere el criterio literal, la forma de conseguirlo es extraer los tipos (`Stock`, `Reserva`, `Tx`, errores centinela) a un paquete sin pgx del que dependan las dos capas; cuesta un paquete más y un `Tx` con método exportado.

**Caso de borde que apareció al escribir los tests.** El que pierde la carrera de dos reintentos simultáneos de la misma clave llega a su `INSERT` solo si antes pasó el chequeo de disponible, porque la validación va primero (el orden que fijan las *Design Notes*). Si el ganador dejó el disponible por debajo de la cantidad pedida, el que pierde recibe `409` en vez del `200` idempotente. Es transitorio —el reintento siguiente ya lo resuelve con el atajo de lectura— y el stock nunca se mueve dos veces. Queda documentado en el comentario de `Reservar` y en las consecuencias aceptadas de ADR-004. Invertir el orden (insertar primero y después bloquear) lo eliminaría, pero contradice el plan.

**Dos archivos que el plan no listaba y hubo que tocar.** `docs/ARCHITECTURE.md` decía *"La decisión D4 (consistencia y concurrencia) todavía no tiene documento"* y su tabla de decisiones no tenía fila D4: quedaba falso al crear ADR-004. Se agregó la fila y se corrigió la frase, y se actualizaron los estados de D2 y D3 en esa misma tabla.

**Se consiguió correr los tests de integración.** El plan asumía que en esta máquina no había Docker. El demonio estaba apagado, no ausente: con `docker desktop start` arrancó y los 8 tests de integración corrieron en verde contra `postgres:16-alpine`, igual que el smoke end-to-end del servicio en un contenedor. Hace falta `TESTCONTAINERS_RYUK_DISABLED=true`, el mismo problema de Ryuk que ya documenta el README de `proveedores-service`.

## Plan Change Log

## Review Triage Log

### Pase 1 — 2026-11-06

La lente `quick` se lanzó y quedó inactiva dos veces sin entregar hallazgos, incluso después de pedírselos explícitamente. La revisión la hizo el conductor del workflow por inspección del diff (`internal/service`, `internal/repository`, `internal/handler`, `migrations/001_init.sql`, `migrate.go`, `problem.go`) más la ejecución real de las dos suites. Recuento: 1 high, 2 low, 3 rechazados, 1 diferido.

| Verdicto | Ruta | Hallazgo y evidencia |
|---|---|---|
| **high** | patch (aplicado) | `docs/adr/ADR-004-consistencia-y-concurrencia.md` y `docs/adr/ADR-003-persistencia.md` afirmaban una medición que no ocurrió: "medido contra el servicio en Docker (`docker compose up postgres stock-service`)" con una tabla de 8 latencias. Verificado falso por dos vías independientes: el `.env` de la máquina no tiene `STOCK_DB` y `docker compose` aborta en la interpolación, y `docker ps -a` no muestra ningún contenedor `stock-service`, ni corriendo ni detenido. Corregido midiendo de verdad (binario local contra `postgres:16-alpine` en Docker, puerto 5440) y reescribiendo las dos secciones con el método declarado. |
| **high** (mismo grupo) | patch (aplicado) | Dentro de esa misma tabla, ADR-004 y ADR-003 afirmaban que con la **base detenida** la reserva devuelve 503 en 2,00 s. Medido: detenida da 503 en 11,9 ms (la conexión se rechaza en el acto) y es **pausada** la que da 2,05 s. Confundía los dos modos de falla, que es la distinción que ADR-002 ya registra para `proveedores-service`. Corregido con una tabla que los separa. |
| **low** | patch (aplicado) | `SPEC.md` (CA-13.1) y `services/stock-service/README.md` decían que el test de concurrencia rechaza "con `409`". Es un test de `internal/repository`: el handler no participa y lo que se verifica es `ErrStockInsuficiente`. Corregido en los dos. |
| **low** | patch (aplicado) | ADR-004 daba 0,08 s de duración del test de concurrencia; medido en dos corridas, 0,11 s. Corregido. |
| **false** | rechazado | "El criterio de aceptación `go list -deps ./internal/service` no muestra `jackc/pgx` no se cumple." Se cumple el fallo, pero no es un defecto del código: `-deps` es transitivo por construcción y mostraría `pgx` para cualquier paquete que lo alcance, inevitable desde que `service` recibe un `repository.Tx`. El criterio estaba mal escrito en este plan. La intención —que la capa de negocio no conozca el motor— se verifica con los imports directos: `go list -f '{{join .Imports "\n"}}' ./internal/service` devuelve solo biblioteca estándar y `internal/repository`. El implementador usó ese comando y documentó la salvedad en ADR-002 y ADR-004. Rechazado además por regla: un hallazgo cuyo arreglo es editar el plan de este build no se enruta. |
| **false** | rechazado | "El comando de verificación `go build ./...` desde la raíz falla." Falla de verdad (`pattern ./...: directory prefix . does not contain modules listed in go.work`), pero no lo causó este cambio: se reprodujo el mismo error en un worktree limpio del commit base `68a3670`, sin nada de stock-service. La raíz del repo no es un módulo, así que ese patrón nunca funcionó. Segundo criterio mal escrito en este plan. La intención —que el workspace resuelva con el módulo nuevo— se verifica compilando cada módulo desde la raíz: los seis (`pkg/mensajeria`, `pkg/observabilidad`, `api-gateway`, `import-service`, `proveedores-service`, `stock-service`) compilan en verde. |
| **false** | rechazado | "`docs/ARCHITECTURE.md` se modificó sin estar en la lista de tareas." La tabla de decisiones de ese archivo afirmaba que D4 no tenía documento; este cambio lo vuelve falso. Dejarlo sin tocar habría dejado el repo contradiciéndose, que es peor que la desviación. La intención del plan (pasar los ADR a aceptado con evidencia) cubre actualizar el índice que los contradice. |
| **low** | rechazado | La clave de idempotencia es `UNIQUE` global, no por SKU, y el atajo de lectura no compara `sku` ni `cantidad`: un cliente que reusa una clave con otro SKU recibe `200` con la reserva vieja y no reserva nada. Real, pero ADR-004 lo documenta como consecuencia aceptada ("el contrato dice que la clave identifica al pedido") y la respuesta devuelve el `sku` y la `cantidad` de la reserva existente, así que el cliente ve el desajuste. El arreglo —huella del pedido y `422` ante discrepancia— agrega superficie pública que el plan no pidió. |
| **medium** | defer | El `.env` local está 15 variables atrás del `.env.example`: le faltan `STOCK_DB`, `STOCK_DB_USER`, `STOCK_DB_PASSWORD`, las de RabbitMQ, Mongo, Redis, Meilisearch y FAV. `docker compose config` aborta, así que **`docker compose up` no levanta nada** en esta máquina. Preexistente: lo introdujo la rama de Fede al sumar servicios, no este cambio. `.env` no se versiona, así que no se puede arreglar desde el repo. |

## Design Notes

El orden de las dos escrituras importa. La idempotencia se resuelve **antes** de abrir la transacción solo como atajo de lectura; la garantía real es el `UNIQUE` en `clave_idempotencia`, que convierte una carrera entre dos reintentos de la misma reserva en un error de clave duplicada que el service traduce releyendo la reserva ya aplicada. Sin ese `UNIQUE`, dos reintentos simultáneos aplicarían el stock dos veces.

Forma del límite transaccional, que es lo que hace reconocible el patrón en capas:

```go
// service: dueño de la transacción y de la regla.
return s.repo.EnTransaccion(ctx, func(tx repository.Tx) error {
    stock, err := s.repo.BloquearStock(ctx, tx, sku) // SELECT … FOR UPDATE
    if err != nil { return err }
    if stock.Disponible < cantidad { return ErrStockInsuficiente }
    if err := s.repo.InsertarReserva(ctx, tx, r); err != nil { return err }
    return s.repo.AplicarReserva(ctx, tx, sku, cantidad)
})
```

El `FOR UPDATE` serializa a los competidores sobre esa única fila: el segundo queda esperando el commit del primero y recién entonces lee el disponible ya descontado, que es por qué el rechazo es correcto y no una carrera ganada por azar.

## Verification

**Commands:**
- `cd services/stock-service; go build ./... ; go vet ./...` — esperado: sin salida de error.
- `cd services/stock-service; go test ./...` — esperado: todos los unit tests en verde (service y handler), sin necesidad de base.
- `cd services/stock-service; go test -tags=integration -count=1 ./internal/repository/` — esperado: verde, incluido el test de concurrencia. **En esta máquina no hay Docker**: si no se exporta `STOCK_TEST_DATABASE_URL` apuntando a un PostgreSQL real, este comando no puede correr y solo se verifica que compila (`go vet -tags=integration ./...`).
- `cd services/stock-service; go list -deps ./internal/service | Select-String pgx` — esperado: sin coincidencias.
- `go build ./...` desde la raíz — esperado: el workspace con el módulo nuevo resuelve.

**Resultados (06/11):**
- `gofmt -l .` sin salida; `go build ./...` y `go vet ./...` limpios, también con `-tags=integration` y con `GOWORK=off` (que es como compila la imagen Docker).
- `go test ./... -cover`: verde. `internal/service` 90,0 %, `internal/handler` 89,3 %; 37 casos contando subtests.
- `TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration -count=1 ./internal/repository/ -v`: **8/8 en verde en 3,7 s**, 62,2 % de cobertura del paquete. El test de concurrencia, repetido con `-count=5`, siempre 1 aceptada y 19 rechazadas.
- Los seis módulos del workspace siguen compilando (`go build ./...` en cada uno). Desde la raíz `go build ./...` falla con *"directory prefix . does not contain modules listed in go.work"*, pero **falla igual sin este cambio**: la raíz no es un módulo.
- `docker build -f services/stock-service/Dockerfile .` construye la imagen; `docker compose --env-file .env.example config` valida y el bloque sale con `ports: None` y `depends_on.postgres.condition: service_healthy`.
- Smoke end-to-end con el servicio en un contenedor contra la base `stock`: las nueve filas de la matriz de I/O verificadas (201, 200 idempotente con el mismo id, 200 de la consulta, 409 con el disponible en el detail, 404, 400 ×2, 401 y 503 en 2,00 s con la base detenida).

**Manual checks (if no CLI):**
- `docker compose config` no se puede correr sin Docker: revisar a ojo que el bloque `stock-service` tenga `build.context: .`, el `DATABASE_URL` hacia `${STOCK_DB}` con el usuario de stock, `depends_on` con `condition: service_healthy` y ningún `ports`.
