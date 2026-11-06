---
title: 'HU-14 — Reposición automática: punto de reorden y orden con el mejor proveedor'
type: 'feature'
ticket: ''
created: '2026-11-06'
status: 'built'
baseline_revision: 'ed65f5222350220e0422ba91fe6a6af6796c00da'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: []
review_loop_iteration: 0
context:
  - '{project-root}/docs/adr/ADR-009-consumo-proveedor.md'
  - '{project-root}/services/stock-service/README.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** `stock-service` ya reserva sin sobreventa, pero nada detecta el faltante: HU-14 sigue *propuesto* en `SPEC.md` y RN-14, RN-15 y RN-16 no tienen código. Sin esto la orden de reposición no existe, y es lo que el objetivo C (pedidos a FAV) necesita para tener algo que enviar.

**Approach:** Un ciclo de reposición dentro de `stock-service` que, cada `INTERVALO_REPOSICION` y al arrancar, busca los SKUs cuyo `disponible` bajó del `minimo`, le pregunta a `proveedores-service` quién es el más barato y crea una orden en `BORRADOR`, una sola abierta por producto. Más la máquina de estados completa de la orden, con las transiciones validadas en la capa de servicio.

**Decisión tomada antes de planificar:** la orden nace **síncrona, sin RabbitMQ**. ADR-005 y `docs/contracts/eventos.md` proponen hoy un evento `StockBajoMinimo` publicado y consumido por `stock-service`; esa propuesta se retira y los dos documentos se corrigen. El requisito de evento publicado y consumido de la consigna lo cubre `ListaPreciosActualizada` (Integrante 2).

## Boundaries & Constraints

**Always:**
- Se respeta la arquitectura en capas ya construida: `internal/handler` → `internal/service` → `internal/repository`, y `repository` sigue siendo el único paquete que importa `pgx` y el único que escribe SQL.
- **Una sola orden abierta por producto (CA-14.2) la garantiza la base**, con un índice único parcial, no solo la lógica. Misma filosofía que el `UNIQUE` de la idempotencia en [ADR-004](docs/adr/ADR-004-consistencia-y-concurrencia.md): la invariante se escribe en el esquema.
- **Transiciones de RN-15, y ninguna más:** `BORRADOR → SOLICITADA`; `SOLICITADA → CONFIRMADA | VENCIDA | EN_REVISION`; `VENCIDA → EN_REVISION`. Cualquier otra se rechaza con un error de negocio.
- La llamada a `proveedores-service` usa los valores que fija [ADR-005](docs/adr/ADR-005-comunicacion.md): timeout **1 s** por intento y hasta **2 reintentos** con espera de 100 ms y 300 ms más jitter. Es un `GET` idempotente.
- **RN-16:** la orden va al proveedor de menor `costo_total`. El contrato ya devuelve el array ordenado ascendente, así que es el primer elemento.
- Migración nueva embebida e idempotente, aplicada al arrancar, bajo el mismo advisory lock `727_002`.
- Código y comentarios en español; errores en `problem+json` con su `code`.

**Never:**
- Nada de RabbitMQ, outbox, `eventos_procesados` ni DLQ: la decisión es síncrona.
- Nada de FAV: ni cliente de pedidos, ni webhook, ni endpoints en el mock, ni pasar una orden a `SOLICITADA` de verdad (eso lo hace el objetivo C, que es quien llama a `POST /v1/pedidos`).
- No se toca `proveedores-service`: se lo consume por su contrato publicado, nada más.
- No se agregan rutas al `api-gateway` ni se publica el puerto de `stock-service`.
- No se inventan estados fuera de los cinco de RN-15.
- La reserva de stock ya construida no se modifica.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|---|---|---|---|
| Baja del mínimo | SKU con `disponible` 4 y `minimo` 10, sin orden abierta | se crea una orden en `BORRADOR` con el proveedor de menor `costo_total` y la cantidad a reponer (CA-14.1) | No error |
| Ya hay una orden abierta | el mismo SKU bajo el mínimo y una orden en `BORRADOR` | **no** se crea otra; el ciclo la saltea (CA-14.2) | No error |
| Stock por encima del mínimo | `disponible` 120, `minimo` 30 | no se crea ninguna orden | No error |
| `proveedores-service` no responde | el SKU está bajo el mínimo y la llamada agota timeout y reintentos | la orden se crea igual en `BORRADOR`, **sin proveedor sugerido** y con el motivo registrado; se reintenta en el ciclo siguiente | se registra, el ciclo sigue con los demás SKUs |
| El producto no existe para el proveedor | `proveedores-service` devuelve `404` | orden en `BORRADOR` sin proveedor, con el motivo | sin reintento: el `404` no es transitorio |
| Nadie tiene stock suficiente | `proveedores-service` devuelve `200` con `[]` | orden en `BORRADOR` sin proveedor, con el motivo "ningún proveedor con stock suficiente" | No error |
| Transición válida | orden en `BORRADOR`, se pide `SOLICITADA` | la orden queda en `SOLICITADA` | No error |
| Transición inválida | orden en `BORRADOR`, se pide `CONFIRMADA` | se rechaza y la orden no cambia (CA-14.3) | `409 TRANSICION_INVALIDA` |
| Consulta de órdenes | `GET` de las órdenes, con filtro opcional por estado | `200` con la lista, más nueva primero | `400` si el estado pedido no existe |
| Carrera entre dos ciclos | dos réplicas corren el ciclo a la vez sobre el mismo SKU | una crea la orden, la otra choca con el índice único parcial y la saltea sin error | el choque se traduce a "ya hay una abierta", no a `500` |

</frozen-after-approval>

## Code Map

El servicio ya existe y las convenciones están establecidas en él; leerlo es suficiente. Lo que importa de cada pieza:

- `services/stock-service/internal/repository/repositorio.go` — `Repo` sobre `*pgxpool.Pool`, `TimeoutConsulta` 2 s, `ErrNoEncontrado`, `ErrClaveDuplicada`, `codigoViolacionUnica` (`23505`) y el tipo opaco `Tx` con `EnTransaccion`. El patrón de traducir un SQLSTATE a un error de dominio se reusa para el choque del índice único parcial.
- `services/stock-service/internal/service/stock.go` — `Servicio`, la interfaz `Repositorio` declarada del lado del consumidor y los errores de negocio (`ErrParametroInvalido`, `ErrProductoNoEncontrado`, `StockInsuficienteError` con su método `Is`). Los errores nuevos siguen ese molde.
- `services/stock-service/internal/handler/handler.go` y `problem.go` — `NuevoHandler` con el mux de Go 1.22, `escribirErrorDeNegocio` como única traducción a HTTP, y las constantes de `code`. Se suma `CodigoTransicionInvalida = "TRANSICION_INVALIDA"` con su título.
- `services/stock-service/cmd/server/main.go` — `cargarConfig` con `strings.TrimSpace`, `migrarConReintentos`, `signal.NotifyContext` y el apagado ordenado. El ciclo nuevo arranca acá y tiene que terminar con el contexto de la señal.
- `services/stock-service/migrations/` — `001_init.sql`, `002_seed.sql` y `embed.go` con `//go:embed *.sql`; se aplican todas en cada arranque, ordenadas por nombre.
- `services/stock-service/internal/repository/migrate.go` — `advisoryLockID = 727_002`; no hay tabla de control, los `.sql` son idempotentes.
- `docs/contracts/openapi.yaml` — el contrato que se consume: `GET /api/v1/proveedores/recomendados?producto&cantidad&limite`, header `X-API-Key`, respuesta array de `Recomendacion` (`proveedor_id`, `proveedor_nombre`, `precio_unitario`, `descuento_pct`, `descuento_aplicado`, `flete`, `stock_disponible`, `costo_total`) **ya ordenado por `costo_total` ascendente**; `200` con `[]` si nadie tiene stock; `404 PRODUCTO_NO_ENCONTRADO` si el SKU no existe.
- `docs/adr/ADR-009-consumo-proveedor.md` — la tabla de la sección "Reposición → pedidos" es la fuente de la semántica de cada estado y de por qué `VENCIDA` y `EN_REVISION` necesitan una persona.

Archivos a modificar: `docker-compose.yml` y `.env.example` (variables nuevas), `docs/adr/ADR-005-comunicacion.md` y `docs/contracts/eventos.md` (retirar el evento), `SPEC.md` (HU-14) y `docs/adr/ADR-002-arquitectura-interna.md` (las transiciones ya viven en la capa de servicio).

## Tasks & Acceptance

**Execution:**
- [ ] `services/stock-service/migrations/003_ordenes_reposicion.sql` — tabla `ordenes_reposicion (id UUID PK, sku FK → stock_productos, cantidad CHECK > 0, estado CHECK IN los cinco de RN-15, proveedor_id NULL, proveedor_nombre NULL, costo_total NUMERIC(14,2) NULL, motivo TEXT NULL, intento INT NOT NULL DEFAULT 0, creado_en, actualizado_en)`. Más `CREATE UNIQUE INDEX IF NOT EXISTS ux_ordenes_abierta_por_sku ON ordenes_reposicion (sku) WHERE estado <> 'CONFIRMADA'` — el índice parcial es la invariante de CA-14.2 en la base. Idempotente.
- [ ] `services/stock-service/internal/repository/ordenes.go` — `Orden`, y sobre `Repo`: `SKUsBajoMinimo` (`WHERE disponible < minimo`), `CrearOrden` (traduce `23505` a `ErrOrdenAbiertaExistente`), `OrdenPorID`, `OrdenesPorEstado` (estado vacío = todas, más nueva primero) y `ActualizarEstadoOrden`. Nada de lógica de negocio acá.
- [ ] `services/stock-service/internal/service/ordenes.go` — la máquina de estados como un mapa de transiciones permitidas y `TransicionarOrden`, que rechaza lo que no esté en el mapa con `TransicionInvalidaError` (con `Is`, como `StockInsuficienteError`). Es la pieza que hace reconocible el patrón en capas: la regla vive acá, no en SQL.
- [ ] `services/stock-service/internal/service/reposicion.go` — `CicloDeReposicion(ctx)`: por cada SKU bajo el mínimo sin orden abierta, pide la recomendación, calcula la cantidad a reponer y crea la orden en `BORRADOR`. Un SKU que falla no corta el ciclo. Devuelve un resumen (creadas, salteadas, sin proveedor) para loguearlo.
- [ ] `services/stock-service/internal/proveedores/cliente.go` — cliente HTTP del contrato publicado: timeout 1 s por intento, 2 reintentos con 100 ms y 300 ms más jitter, solo ante error de transporte, timeout, `429`, `500`, `502`, `503`. `404` y `[]` no se reintentan y devuelven errores distinguibles. `MaxIdleConnsPerHost` configurado, no el transporte por defecto.
- [ ] `services/stock-service/internal/handler/ordenes.go` — `GET /api/v1/stock/ordenes` con filtro opcional `?estado=`, y `POST /api/v1/stock/ordenes/{id}/transiciones` con body `{"estado":"SOLICITADA"}` para operar a mano las transiciones que hoy no dispara nada (el objetivo C las automatiza). Las dos detrás de `conAPIKey`.
- [ ] `services/stock-service/cmd/server/main.go` — leer `PROVEEDORES_URL`, `PROVEEDORES_API_KEY` e `INTERVALO_REPOSICION` (por defecto `1h`, mínimo `10s` para la demo); armar el cliente y lanzar el ciclo en una goroutine que corre una vez al arrancar y después cada intervalo, y que termina con el contexto de la señal. Sin `PROVEEDORES_URL` el servicio arranca igual y el ciclo queda apagado, con un aviso: no puede impedir que la reserva funcione.
- [ ] `services/stock-service/internal/service/ordenes_test.go` — test de tabla de **todas** las transiciones, válidas e inválidas, incluidas las que RN-15 prohíbe explícitamente (`BORRADOR → CONFIRMADA`, saltear `SOLICITADA`).
- [ ] `services/stock-service/internal/service/reposicion_test.go` — ciclo con dobles: crea para el que está bajo el mínimo, saltea el que ya tiene orden abierta, ignora el que está por encima, y crea sin proveedor cuando el cliente falla o devuelve `[]`.
- [ ] `services/stock-service/internal/proveedores/cliente_test.go` — con `httptest`: camino feliz, que toma el primer elemento como el más barato, reintento ante `503` y éxito al segundo intento, que un `404` **no** se reintenta, y que se agota tras 3 intentos. Verificar el `X-API-Key` enviado.
- [ ] `services/stock-service/internal/repository/ordenes_integration_test.go` — `//go:build integration`: `SKUsBajoMinimo` contra el seed, y que **crear una segunda orden abierta del mismo SKU choca con el índice parcial** y devuelve `ErrOrdenAbiertaExistente`, mientras que crear una nueva cuando la anterior está `CONFIRMADA` sí funciona.
- [ ] `docker-compose.yml`, `.env.example` — `PROVEEDORES_URL` apuntando **directo** a una réplica por la red interna (`http://proveedores-service-1:8080`), **no** al gateway: el gateway es el punto de entrada de consumidores externos, y el tráfico entre servicios propios no tiene que dar esa vuelta. Más `PROVEEDORES_API_KEY` e `INTERVALO_REPOSICION` con su valor por defecto.
- [ ] `services/stock-service/README.md` — el ciclo, las variables nuevas, los endpoints de órdenes y la máquina de estados.
- [ ] `docs/adr/ADR-005-comunicacion.md` y `docs/contracts/eventos.md` — **retirar** `StockBajoMinimo`: la orden nace síncrona. Dejar escrito por qué (el evento obligatorio lo cubre `ListaPreciosActualizada`, y un segundo evento no agregaba nada que la consigna pida) y pasar la llamada `stock-service → proveedores-service` de propuesta a implementada con sus valores reales.
- [ ] `SPEC.md` y `docs/adr/ADR-002-arquitectura-interna.md` — HU-14 a *implementado* con CA-14.1, CA-14.2 y CA-14.3 señalando sus tests; en ADR-002, que las transiciones de la orden ya están en la capa de servicio (hoy dice que faltan).

**Acceptance Criteria:**
- Given el servicio nuevo, when se corre `go build ./...`, `go vet ./...` y `go test ./...` en `services/stock-service`, then compila y los unitarios pasan sin base de datos.
- Given `go list -f '{{join .Imports "\n"}}' ./internal/service`, when se inspecciona la salida, then no aparece ni `jackc/pgx` ni `net/http`: la capa de negocio no conoce el motor de la base ni el transporte con el que se consulta a `proveedores-service` (el cliente se le inyecta como interfaz, igual que el repositorio).
- Given un ADR o `SPEC.md` modificado, when el revisor automático lo compara con el código, then cada afirmación apunta a un archivo que existe y a un test que la respalda, y no queda ninguna mención de `StockBajoMinimo` como decisión vigente.

## Implementation Notes

## Plan Change Log

## Review Triage Log

### Pase 1 — 2026-11-06

El agente implementador murió por límite de sesión después de escribir todo el código y ningún documento. El conductor del workflow terminó las 7 tareas de configuración y documentación que faltaban, y revisó por inspección del diff más la ejecución real de las suites. Recuento: 1 medium, 1 low, 1 rechazado.

| Verdicto | Ruta | Hallazgo y evidencia |
|---|---|---|
| **medium** | patch (aplicado) | La implementación quedó trunca: faltaban `docker-compose.yml` (sin `PROVEEDORES_URL`, `PROVEEDORES_API_KEY` ni `INTERVALO_REPOSICION`, así que el ciclo habría arrancado apagado en la demo), `.env.example`, el retiro de `StockBajoMinimo` de ADR-005 y `eventos.md`, `SPEC.md` (HU-14 y RN-14/15/16), ADR-002 y el README. Verificado con `git status` y búsquedas por las variables: ninguno aparecía modificado. Completadas las 7 tareas. |
| **low** | patch (aplicado) | `SPEC.md` citaba el test de transiciones como `TestTransiciones`; el nombre real es `TestTransicionesDeLaOrden`. Corregido. Los números que afirma (25 combinaciones, 5 válidas) sí se verificaron contra el código del test. |
| **false** | rechazado | "El código introduce `ORDEN_NO_ENCONTRADA`, un `code` que el plan no pedía." Es correcto y necesario: `PRODUCTO_NO_ENCONTRADO` habría mezclado "no existe el SKU" con "no existe la orden", que son recursos distintos del mismo servicio. Se documentó en el README en lugar de quitarlo. |

**Desvío del plan, aceptado:** las rutas de órdenes se registran sólo si `cfg.Ordenes != nil`. Verificado que `main.go` siempre pasa el servicio (`Ordenes: svcOrdenes`, creado aun con el cliente en `nil`), así que en producción las rutas existen siempre y la afirmación del README —sin `PROVEEDORES_URL` el ciclo se apaga pero las consultas y transiciones siguen— se sostiene. El `nil` es para los tests del handler.

**Lo que no se pudo verificar:** `docker compose up`, por el `.env` 15 variables atrás del `.env.example` (ya anotado en `deferred-work.md`). El bloque nuevo del compose se revisó a ojo: `PROVEEDORES_URL` apunta a `proveedores-service-1`, no al gateway, y `INTERVALO_REPOSICION` tiene valor por defecto.

## Design Notes

**Por qué el índice único es parcial y no un `UNIQUE (sku)` común.** Un producto tiene muchas órdenes a lo largo del tiempo; lo que no puede haber es más de una *sin cerrar*. `WHERE estado <> 'CONFIRMADA'` deja pasar el histórico y bloquea la segunda abierta. Y resuelve gratis la carrera entre dos réplicas corriendo el ciclo a la vez: la que pierde recibe `23505` y saltea el SKU, en vez de crear una orden duplicada.

**`VENCIDA` y `EN_REVISION` cuentan como abiertas a propósito.** ADR-009 dice que las dos necesitan que una persona intervenga (confirmar que no se transfirió, o resolver por contacto con FAV). Si el ciclo automático pudiera crear una orden nueva con una `VENCIDA` sin resolver, se podría terminar comprando dos veces lo mismo. La consecuencia aceptada es que un SKU con una orden trabada no se repone solo hasta que alguien la cierra, y eso va escrito en el ADR.

**Cantidad a reponer.** `minimo × 2 − disponible`, que deja el stock en el doble del mínimo. Es una regla simple y explícita: RN-14 no define la cantidad, así que se elige una, se documenta y queda como el punto obvio para cambiar si el negocio lo pide.

**Lo que el ciclo no hace.** No envía nada: la orden queda en `BORRADOR`. Quien la pasa a `SOLICITADA` es el objetivo C, llamando a `POST /v1/pedidos` de FAV. El endpoint manual de transiciones existe para la demo y para que las transiciones sean operables antes de que C exista.

## Verification

**Commands:**
- `cd services/stock-service; go build ./... ; go vet ./...` — esperado: sin errores.
- `cd services/stock-service; go test ./... -count=1 -cover` — esperado: todo en verde, incluidos los tests de la máquina de estados, del ciclo y del cliente.
- `cd services/stock-service; $env:TESTCONTAINERS_RYUK_DISABLED="true"; go test -tags=integration -count=1 ./internal/repository/` — esperado: en verde, incluidos los dos tests nuevos del índice parcial. Docker está disponible en esta máquina (server 27.4.0) y los 8 tests previos pasan, así que este comando tiene que correr de verdad, no solo compilar.
- `go build ./services/stock-service/...` desde la raíz — esperado: el módulo resuelve dentro del workspace. (`go build ./...` desde la raíz **no** funciona en este repo y nunca funcionó: la raíz no es un módulo.)

**Manual checks (if no CLI):**
- `docker compose up` no se puede verificar: el `.env` de la máquina está 15 variables atrás del `.env.example` y el compose aborta en la interpolación. Revisar a ojo que el bloque de `stock-service` tenga `PROVEEDORES_URL` apuntando a `proveedores-service-1` y no al gateway, y que `INTERVALO_REPOSICION` tenga valor por defecto.
