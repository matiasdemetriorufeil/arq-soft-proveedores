# stock-service

Servicio en Go que administra el **stock propio**. Hace tres cosas:

1. **Reservar unidades sin sobreventa** (HU-13, RN-13). La garantía la da PostgreSQL: la reserva corre dentro de una transacción que bloquea la fila del SKU con `SELECT … FOR UPDATE`, y es idempotente por `Idempotency-Key`.
2. **Reponer lo que baja del mínimo** (HU-14, RN-14 a RN-16). Un ciclo interno busca los SKUs por debajo de su mínimo, le pregunta a `proveedores-service` quién es el más barato y crea una orden de reposición en `BORRADOR`, una sola abierta por producto.
3. **Comprarle a FAV** (HU-15, [ADR-009](../../docs/adr/ADR-009-consumo-proveedor.md)). Cuando la que baja del mínimo es una prenda de FAV (`FAV-<varianteId>`), la orden le hace el pedido a FAV con `Idempotency-Key`, pasa a `SOLICITADA` y sigue su estado por el **webhook firmado** que manda FAV y por una consulta de respaldo. Cuando FAV confirma el pago, la orden pasa a `CONFIRMADA` y las unidades entran al stock.

- **Arquitectura en capas** (handler → service → repository), distinta a propósito de la hexagonal de `proveedores-service`. Ver [ADR-002](../../docs/adr/ADR-002-arquitectura-interna.md).
- Consistencia y concurrencia: [ADR-004](../../docs/adr/ADR-004-consistencia-y-concurrencia.md). Comunicación entre servicios: [ADR-005](../../docs/adr/ADR-005-comunicacion.md).
- Almacenamiento: PostgreSQL, **base `stock` con su propio usuario** ([ADR-003](../../docs/adr/ADR-003-persistencia.md)). Las migraciones están embebidas en el binario y se aplican al arrancar.
- Logs JSON (`log/slog`) correlacionados con `X-Request-ID` y `trace_id`; trazas OpenTelemetry con un span por consulta a Postgres.

Lo que **no** hace (todavía): **no está publicado en el `api-gateway`**: por ahora solo se lo alcanza desde la red interna. Las órdenes de productos que no son de FAV se quedan en `BORRADOR` y se operan a mano por la API, porque esos proveedores no tienen una API para comprarles. Los pedidos a FAV no tienen circuit breaker y prueban contra nuestro mock, porque FAV todavía no publicó su URL (ver *Pendiente* en ADR-009).

## Estructura

```
cmd/server/            arranque, configuración, trazas, migraciones, ciclo de reposición y apagado ordenado
cmd/sonda/             healthcheck del contenedor (pregunta por /readyz)
internal/handler/      HTTP: validación, API key, request log, problem+json, health checks
internal/service/      negocio: dueño de la transacción, de "sin sobreventa" y de las transiciones de la orden
internal/repository/   datos: pool pgx, SELECT … FOR UPDATE, migraciones, span por consulta
internal/proveedores/  cliente HTTP de proveedores-service: timeout 1 s, 2 reintentos, traducción de errores
internal/fav/          cliente de pedidos de FAV (timeout 3 s, 3 reintentos con la misma clave) y verificación de la firma del webhook
migrations/            001_init.sql, 002_seed.sql (30 SKUs de demo), 003_ordenes_reposicion.sql y 004_pedidos_fav.sql (pedido de la orden, avisos recibidos y 3 prendas de FAV), embebidos
```

La dependencia va en un solo sentido: `handler → service → repository`. **`repository` es el único paquete que importa `pgx`** y el único que escribe SQL; `service` solo ve el tipo opaco `repository.Tx`, que recibe y vuelve a pasar sin poder ejecutar nada con él.

Las dos escrituras de una reserva (la fila en `reservas` y el descuento en `stock_productos`) caen dentro del mismo límite transaccional, que abre `service`:

```go
return s.repo.EnTransaccion(ctx, func(tx repository.Tx) error {
    st, err := s.repo.BloquearStock(ctx, tx, sku) // SELECT … FOR UPDATE
    if err != nil { return err }
    if st.Disponible < cantidad { return &StockInsuficienteError{…} }
    if err := s.repo.InsertarReserva(ctx, tx, &nueva); err != nil { return err }
    return s.repo.AplicarReserva(ctx, tx, sku, cantidad)
})
```

## Esquema

| Tabla | Columnas | Para qué |
|---|---|---|
| `stock_productos` | `sku` PK, `disponible` ≥ 0, `reservado` ≥ 0, `minimo`, `actualizado_en` | es la fila que se bloquea al reservar. El `CHECK (disponible >= 0)` es la última red: aunque falle la lógica, la sobreventa no entra |
| `reservas` | `id` UUID PK, `sku` FK, `cantidad` > 0, `clave_idempotencia` **UNIQUE**, `estado`, `creado_en` | el `UNIQUE` es lo que hace idempotente a la operación ante dos reintentos simultáneos |
| `ordenes_reposicion` | `id` UUID PK, `sku` FK, `cantidad` > 0, `estado`, `proveedor_id`/`proveedor_nombre`/`costo_total` (los tres anulables), `motivo`, `intento`, `creado_en`, `actualizado_en`, y el pedido a FAV: `clave_pedido`, `pedido_id` (único), `pedido_estado`, `pedido_total`, `pago_vence_en` | el índice **parcial** `ux_ordenes_abierta_por_sku ON (sku) WHERE estado <> 'CONFIRMADA'` garantiza una sola orden abierta por producto (CA-14.2), incluso si dos réplicas corren el ciclo a la vez. `clave_pedido` es la `Idempotency-Key` del intento en curso, `<id>-<intento>`: los reintentos la repiten y se renueva después de un rechazo definitivo |
| `avisos_fav_procesados` | `evento_id` UUID PK, `pedido_id`, `estado`, `recibido_en` | la PRIMARY KEY deduplica los avisos del webhook: el mismo `eventoId` se procesa una sola vez aunque llegue dos veces a la vez |

### Máquina de estados de la orden (RN-15)

```
BORRADOR ──→ SOLICITADA ──→ CONFIRMADA        (único estado final)
                   │
                   ├──────→ VENCIDA ──→ EN_REVISION
                   └──────→ EN_REVISION
```

Las otras 20 combinaciones se rechazan con `409 TRANSICION_INVALIDA`. **`VENCIDA` y `EN_REVISION` cuentan como abiertas**: las dos necesitan que una persona intervenga ([ADR-009](../../docs/adr/ADR-009-consumo-proveedor.md)), así que con una sin resolver el ciclo no crea otra orden para ese producto. Es a propósito: si pudiera, se podría terminar comprando dos veces lo mismo.

## Configuración

| Variable | Obligatoria | Descripción |
|---|---|---|
| `DATABASE_URL` | sí | URL de la base `stock`, por ejemplo `postgres://stock:stock-local@postgres:5432/stock?sslmode=disable` |
| `API_KEYS` | sí | Claves aceptadas en `X-API-Key`, separadas por coma. Si está vacía, el servicio no arranca. |
| `PORT` | no | Puerto HTTP. Por defecto `8080`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | Colector al que exportar las trazas, por ejemplo `http://jaeger:4318`. Vacío: el servicio arranca igual y las trazas quedan en no-op. |
| `PROVEEDORES_URL` | no | Base de `proveedores-service`, por ejemplo `http://proveedores-service-1:8080`. **Va directo a una réplica, no al gateway** ([ADR-005](../../docs/adr/ADR-005-comunicacion.md)). **Si falta, el ciclo de reposición queda apagado** con un aviso en el log: la reserva y las consultas de órdenes siguen funcionando. |
| `PROVEEDORES_API_KEY` | no | Clave con la que se consulta esa capacidad. Tiene que estar en el `API_KEYS` de `proveedores-service`. |
| `INTERVALO_REPOSICION` | no | Cada cuánto corre el ciclo. Por defecto `1h`, **mínimo `10s`** (para la demo). Un valor que no sea una duración de Go, o menor al mínimo, impide el arranque. |
| `FAV_URL` | no | Base de la API de FAV (en el compose, `http://fav-mock:8090` si no se define). **Va directo a FAV, nunca por el gateway.** Sin ella, los pedidos quedan apagados y las órdenes de prendas de FAV esperan en `BORRADOR`. |
| `FAV_API_KEY` | si hay `FAV_URL` | Clave que nos dio FAV (`X-API-Key`). |
| `FAV_SECRETO_AVISOS` | si hay `FAV_NOTIFICACION_URL` | Secreto con el que se verifica la firma del webhook. **Sin él, la ruta `/webhooks/fav` no existe.** |
| `FAV_NOTIFICACION_URL` | no | Nuestro receptor tal como lo alcanza FAV; va en cada pedido como `notificacionUrl`. Con el mock, `http://stock-service:8080/webhooks/fav`. Vacía: los pedidos salen sin avisos y el estado se sigue solo con la consulta de respaldo. |
| `FAV_PROVEEDOR_ID` | no | Id de FAV en la tabla de proveedores. Por defecto `11`. |
| `INTERVALO_PEDIDOS_FAV` | no | Cada cuánto se reintentan los pedidos pendientes y se consulta el estado de los abiertos. Por defecto `15m`, mínimo `10s`. |

## Cómo correrlo

### Con docker compose (lo normal)

```bash
docker compose up -d --build
```

El servicio queda en la red interna, **sin puerto publicado al host**: se lo alcanza como `http://stock-service:8080` desde otro contenedor. Depende de que `postgres` esté sano y usa la base `stock` con `STOCK_DB_USER` (ver `.env.example`). Si venías de un volumen de Postgres anterior a que existiera la base `stock`, hay que recrearlo una vez: `docker compose down -v`.

### Local con Go

```bash
docker run -d --name pg-stock -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:16

cd services/stock-service
export DATABASE_URL="postgres://postgres:dev@localhost:5432/postgres?sslmode=disable"
export API_KEYS="dev"
go run ./cmd/server
```

Al arrancar crea las tablas y carga el seed (los mismos 30 SKUs del seed de `proveedores-service`, con stock y mínimo deterministas). Si se reinicia sobre la misma base, el seed no se duplica. Si Postgres todavía no está listo, reintenta las migraciones hasta 15 veces, cada 2 s.

### Con Docker

El contexto del build es la **raíz del repo**, no este directorio: el módulo depende de `pkg/observabilidad` por ruta relativa.

```bash
docker build -f services/stock-service/Dockerfile -t arq-soft/stock-service .
```

## Tests

```bash
go vet ./... && go test ./... -cover
```

No necesitan Docker ni base de datos: `internal/service` se prueba con un repositorio falso (idempotencia, stock insuficiente, SKU inexistente, parámetros inválidos, una sola transacción en el camino feliz) y `internal/handler` con un servicio falso (traducción de cada error a su `problem+json` y 401 sin API key).

**Integración con PostgreSQL** (archivos `*_integration_test.go`, build tag `integration`, así que `go test ./...` no los compila):

```bash
go test -tags=integration -count=1 ./internal/repository/ -v
```

Levanta `postgres:16-alpine` con [testcontainers-go](https://golang.testcontainers.org/) en un puerto libre del host. **Si no hay Docker en la máquina**, se le pasa un PostgreSQL ya levantado:

```powershell
$env:STOCK_TEST_DATABASE_URL = "postgres://postgres:test@localhost:5432/stock_test?sslmode=disable"
go test -tags=integration -count=1 ./internal/repository/ -v
```

Esa base se usa de verdad: las migraciones crean las tablas y los tests borran las filas de los SKUs `TEST-*`. No apuntarla a una base con datos que importen.

Lo que cubren:

- `TestSinSobreventaConReservasSimultaneas` — **la evidencia de CA-13.1**: disponible 10 y 20 reservas de 6 lanzadas a la vez; exactamente 1 aceptada, 19 rechazadas con `ErrStockInsuficiente` (es un test de la capa de datos: el `409` lo pone el handler, que acá no participa), disponible final 4 y reservado 6.
- `TestReintentosSimultaneosAplicanElStockUnaVez` — 20 reintentos simultáneos de la **misma** clave de idempotencia devuelven la misma reserva y mueven el stock una sola vez (CA-13.2).
- Migraciones y seed, `StockPorSKU`, `Ping`, el camino completo de la reserva, la detección del `UNIQUE` de la clave de idempotencia y el `CHECK (disponible >= 0)` de la base.
- Pedidos a FAV (`pedidos_integration_test.go`): la clave `<id>-<intento>` se mantiene en los reintentos y cambia después de un rechazo; 20 entregas simultáneas del mismo aviso registran 1; 10 confirmaciones simultáneas de la misma orden (webhook y respaldo a la vez) suman el stock **una sola vez**; un fallo después de mover la orden deshace también el stock.

**Test de contrato con FAV** (`internal/fav/contrato_test.go`, corre con `go test ./...`): valida contra la copia del OpenAPI de FAV v1.1.0 (`docs/contracts/consumidos/`) que nuestro `POST /v1/pedidos` y nuestro `GET /v1/pedidos/{id}` cumplen el contrato, que el cliente entiende sus ejemplos y sus errores, y que el receptor acepta su vector de prueba de la firma. Las respuestas y los avisos del mock se validan contra el mismo contrato en `services/import-service/internal/fav/contrato_pedidos_test.go`.

Si falla con `reaper: ... "Started" matched 0 times`, es el contenedor Ryuk de testcontainers y no los tests: `TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration ./internal/repository/ -v`.

## API

Las dos rutas de negocio requieren el header `X-API-Key`.

### `POST /api/v1/stock/reservas`

Reserva unidades de un SKU. Requiere además el header **`Idempotency-Key`**: es lo que hace que un reintento no reserve dos veces.

```bash
curl -i -X POST http://stock-service:8080/api/v1/stock/reservas \
  -H "X-API-Key: dev" \
  -H "Idempotency-Key: pedido-1001" \
  -H "Content-Type: application/json" \
  -d '{"sku":"PAN-450W-MONO","cantidad":6}'
```

```json
{"id":"8f1c…","sku":"PAN-450W-MONO","cantidad":6,"estado":"CONFIRMADA","creado_en":"2026-11-06T18:22:01Z"}
```

- `201` si la reserva es nueva.
- `200` con **la misma** reserva si la `Idempotency-Key` ya se había aplicado: el reintento no mueve el stock (CA-13.2).
- `cantidad` tiene que ser un entero > 0: `1.5`, `"6"` entre comillas o `0` dan `400`.

### `GET /api/v1/stock/{sku}`

```bash
curl -H "X-API-Key: dev" http://stock-service:8080/api/v1/stock/PAN-450W-MONO
```

```json
{"sku":"PAN-450W-MONO","disponible":114,"reservado":6,"minimo":30}
```

SKUs útiles del seed: `PAN-450W-MONO` (120 unidades), `INV-10KW-TRI` (6, sirve para provocar un `409`), `CAB-SOLAR-4MM` (5000), `PROT-DC-1000V` (4 con mínimo 10: arranca por debajo del mínimo a propósito).

**Errores** (`application/problem+json`)

| Status | `code` | Cuándo |
|---|---|---|
| 400 | `PARAMETRO_INVALIDO` | el cuerpo no es JSON válido, falta `sku`, `cantidad` no es un entero > 0, o falta `Idempotency-Key` |
| 401 | `NO_AUTORIZADO` | falta `X-API-Key` o no es válida |
| 404 | `PRODUCTO_NO_ENCONTRADO` | el SKU no está en `stock_productos` |
| 404 | `ORDEN_NO_ENCONTRADA` | no hay ninguna orden de reposición con ese id |
| 409 | `STOCK_INSUFICIENTE` | no hay unidades suficientes; el `detail` dice cuántas había |
| 409 | `TRANSICION_INVALIDA` | RN-15 no permite ese cambio de estado; la orden no cambió |
| 503 | `NO_DISPONIBLE` | la base falla o no responde en 2 s; reintentar más tarde |

```json
{"type":"https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#STOCK_INSUFICIENTE","title":"Stock insuficiente","status":409,"detail":"se pidieron 6 unidades de 'INV-10KW-TRI' y hay 4 disponibles","code":"STOCK_INSUFICIENTE","instance":"/api/v1/stock/reservas","request_id":"demo-1"}
```

### `GET /api/v1/stock/ordenes`

Lista las órdenes de reposición, la más nueva primero. Filtro opcional `?estado=`.

```bash
curl -H "X-API-Key: dev" "http://stock-service:8080/api/v1/stock/ordenes?estado=BORRADOR"
```

```json
[{"id":"3c1a…","sku":"PROT-DC-1000V","cantidad":16,"estado":"BORRADOR",
  "proveedor_id":7,"proveedor_nombre":"Sur Solar SRL","costo_total":184320.50,
  "creado_en":"2026-11-06T19:40:12Z"}]
```

Una orden creada cuando `proveedores-service` no respondió viene con `proveedor_id` en `null` y el `motivo` explicando por qué. Un `estado` que no es uno de los cinco da `400`, no una lista vacía.

### `POST /api/v1/stock/ordenes/{id}/transiciones`

Mueve la orden a otro estado, si RN-15 lo permite. Existe para operar a mano las órdenes que no son de FAV: las de FAV las mueven el pedido y sus avisos.

```bash
curl -i -X POST http://stock-service:8080/api/v1/stock/ordenes/3c1a…/transiciones \
  -H "X-API-Key: dev" -H "Content-Type: application/json" \
  -d '{"estado":"SOLICITADA"}'
```

`200` con la orden actualizada. Si la transición no está permitida, `409 TRANSICION_INVALIDA` y la orden **no cambia**; el `detail` dice en qué estado está y qué se permite desde ahí. Si otra request la movió en el medio, se rechaza con el estado nuevo en lugar de pisarlo.

### `POST /api/v1/stock/ordenes/{id}/pedido`

Le hace el pedido a FAV en el momento, sin esperar el ciclo. Sirve para la demo y para reintentar a mano una orden que quedó en `BORRADOR`.

```bash
curl -i -X POST http://stock-service:8080/api/v1/stock/ordenes/3c1a…/pedido -H "X-API-Key: dev"
```

| Status | `code` | Cuándo |
|---|---|---|
| 200 | — | FAV aceptó el pedido: la orden vuelve en `SOLICITADA` con `pedido_id`, `pedido_total` y `pago_vence_en` |
| 409 | `PEDIDO_RECHAZADO` | FAV lo rechazó (por ejemplo, sin stock) o la orden no es de una prenda de FAV. La orden sigue en `BORRADOR` con el `motivo` |
| 409 | `TRANSICION_INVALIDA` | la orden ya no está en `BORRADOR` |
| 503 | `PROVEEDOR_NO_DISPONIBLE` | FAV no respondió; el ciclo reintenta con la misma `Idempotency-Key` |

### `POST /webhooks/fav`

El aviso `pedidoActualizado` de FAV. **No pasa por el gateway ni pide API key**: se autentica con la firma `X-FAV-Firma` (HMAC-SHA256 de `<t>.<cuerpo crudo>` con `FAV_SECRETO_AVISOS`, a lo sumo 5 minutos de antigüedad). Responde enseguida y procesa después: confirma el estado con `GET /v1/pedidos/{pedidoId}` y mueve la orden (`PAGADO` → `CONFIRMADA` y suma el stock, `VENCIDO` → `VENCIDA`, `A_VERIFICAR` o un estado desconocido → `EN_REVISION`).

| Status | `code` | Cuándo |
|---|---|---|
| 200 | — | aviso recibido; `"repetido": true` si ese `eventoId` ya había llegado |
| 400 | `AVISO_INVALIDO` | firma bien pero sin `eventoId` o `pedido`, o `X-FAV-Evento-Id` distinto del cuerpo |
| 401 | `FIRMA_INVALIDA` | firma ausente, mala o vieja. FAV no lo reintenta |
| 503 | `NO_DISPONIBLE` | la base no respondió al registrar el aviso. FAV lo reintenta |

**Para la demo**, con el mock: `POST localhost:8090/_mock/pedidos/<pedido_id>/estado` con `{"estado":"PAGADO"}` simula que FAV verificó la transferencia y manda el aviso. `"duplicar": true` lo manda dos veces y `"firmaInvalida": true` lo firma mal.

### `GET /healthz` y `GET /readyz`

Sin autenticación. `/healthz` indica que el proceso está vivo y responde `200` siempre. `/readyz` hace ping a la base (timeout 2 s) y devuelve `503 NO_DISPONIBLE` si no responde; es lo que consulta `/sonda`, el healthcheck del contenedor.

## Observabilidad

- Cada línea de log es JSON e incluye `service=stock-service`.
- Cada request registra una línea `request` con `request_id`, método, path, status, bytes y `duration_ms` (WARN para 4xx, ERROR para 5xx).
- Si la request trae `X-Request-ID` se reutiliza; si no, se genera uno. Siempre vuelve en el header de la respuesta y en el campo `request_id` de los errores.
- Las trazas continúan la que llega en `traceparent` y abren un span por consulta a Postgres (`db.statement`, `db.sql.table`). Los parámetros **no** se trazan: entre ellos va la clave de idempotencia, que es del cliente. `/healthz` y `/readyz` no se trazan.
