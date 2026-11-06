# proveedores-service

Servicio en Go que publica la capacidad **proveedores recomendados** que consume el grupo de paneles solares. Para un producto (SKU) y una cantidad, rankea las ofertas de los proveedores por **costo total** y devuelve las más convenientes.

- Arquitectura hexagonal (puertos y adaptadores). Ver [ADR-002](../../docs/adr/ADR-002-arquitectura-interna.md).
- Almacenamiento: PostgreSQL (pgx v5). Las migraciones están embebidas en el binario y se aplican al arrancar.
- Consume `ListaPreciosActualizada` de RabbitMQ ([`eventos.md`](../../docs/contracts/eventos.md)): así entran las ofertas de FAV al ranking.
- Caché de ofertas en Redis, como decorador del repositorio ([ADR-007](../../docs/adr/ADR-007-cache.md)).
- Búsqueda de productos en Meilisearch, un índice derivado de PostgreSQL ([ADR-006](../../docs/adr/ADR-006-busqueda.md)).
- **Solo PostgreSQL es obligatorio.** Si RabbitMQ, Redis o Meilisearch no están, el servicio arranca, atiende la capacidad y se conecta cuando vuelven. `/healthz` y `/readyz` no dependen de ellos.
- Logs JSON (`log/slog`), correlacionados con `X-Request-ID` y con `trace_id`.
- Trazas OpenTelemetry: continúa la del gateway y abre un span por consulta a Postgres. Ver [`docs/observabilidad/README.md`](../../docs/observabilidad/README.md).

## Estructura

```
cmd/server/                 arranque, configuración, trazas y apagado ordenado
cmd/sonda/                  healthcheck del contenedor (pregunta por /readyz)
cmd/reindexar/              reconstruye el índice de búsqueda desde PostgreSQL
internal/domain/            reglas de negocio puras (Oferta, Recomendacion, Rankear)
internal/ports/             interfaces que el núcleo necesita (OfertasRepo, Salud, los de eventos y búsqueda)
internal/indexacion/        mantiene el índice al día: drena los SKU pendientes y reconstruye
internal/adapters/http/     endpoints, API key, request log, problem+json, health checks
internal/adapters/postgres/ pool pgx, ofertas, aplicación de eventos, documentos, migraciones
internal/adapters/rabbitmq/ consumidor de ListaPreciosActualizada, con reconexión
internal/adapters/redis/    caché: decorador de OfertasRepo e invalidación
internal/adapters/meilisearch/ índice de productos, con cliente HTTP propio
migrations/                 esquema, datos de demo, proveedor FAV y tablas de eventos, embebidos
```

`domain` solo usa la biblioteca estándar. Los adaptadores dependen de `ports` y `domain`, nunca al revés.

## Configuración

Todo se configura por variables de entorno; no hay secretos en el repo.

| Variable | Obligatoria | Descripción |
|---|---|---|
| `DATABASE_URL` | sí | URL de PostgreSQL, por ejemplo `postgres://postgres:dev@localhost:5432/postgres?sslmode=disable` |
| `API_KEYS` | sí | Claves aceptadas en `X-API-Key`, separadas por coma. Si está vacía, el servicio no arranca. |
| `PORT` | no | Puerto HTTP. Por defecto `8080`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | Colector al que exportar las trazas, por ejemplo `http://jaeger:4318`. Vacío: el servicio arranca igual y las trazas quedan en no-op. |
| `RABBITMQ_URL` | no | Broker del que consume los eventos. Vacía: no consume. |
| `REDIS_URL` | no | Redis de la caché, por ejemplo `redis://:clave@redis:6379/0`. Vacía: sin caché. |
| `CACHE_HABILITADA` | no | `true` por defecto. En `false`, la capacidad lee directo de PostgreSQL aunque haya `REDIS_URL`. |
| `MEILI_URL` | no | Meilisearch. Vacía: la búsqueda responde 503. |
| `MEILI_MASTER_KEY` | no | Clave de Meilisearch. |
| `MEILI_INDICE` | no | Nombre del índice. Por defecto `productos`. |

## Cómo correrlo

### Local con Go

```bash
docker run -d --name pg-prov -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:16

cd services/proveedores-service
export DATABASE_URL="postgres://postgres:dev@localhost:5432/postgres?sslmode=disable"
export API_KEYS="dev"
go run ./cmd/server
```

Al arrancar crea las tablas y carga el seed (10 proveedores, 30 productos, 170 ofertas). Si se reinicia sobre la misma base, el seed no se duplica. Si Postgres todavía no está listo, reintenta las migraciones hasta 15 veces, cada 2 s.

### Con Docker

El contexto del build es la **raíz del repo**, no este directorio: el módulo depende de `pkg/observabilidad` y `pkg/mensajeria` por ruta relativa y las tres carpetas tienen que entrar en el contexto.

```bash
docker build -f services/proveedores-service/Dockerfile -t proveedores-service .
docker run --rm -p 8080:8080 \
  -e DATABASE_URL="postgres://postgres:dev@host.docker.internal:5432/postgres?sslmode=disable" \
  -e API_KEYS="dev" \
  proveedores-service
```

La imagen es multi-stage: compila con `golang:1.26-alpine` y corre sobre `distroless/static` como usuario no root. Trae otros dos binarios: `/sonda`, que es el healthcheck del contenedor porque la imagen no tiene shell ni cliente HTTP con el que preguntar por `/readyz`, y `/reindexar`.

### Reconstruir el índice

El servicio lo reconstruye solo al arrancar si no existe. Para hacerlo a mano, si se sospecha que está desfasado:

```bash
docker compose exec proveedores-service-1 /reindexar
```

Carga un índice temporal y lo intercambia con el vigente: la búsqueda sigue respondiendo mientras tanto. Un candado de PostgreSQL hace que una sola réplica por vez escriba el índice; lo que cambie durante la reconstrucción queda pendiente y se indexa después del intercambio.

### Tests

```bash
go vet ./... && go test ./... -cover
```

Estos tests no necesitan Docker ni una base de datos.

**Integración con dependencias reales.** Los adaptadores se prueban contra PostgreSQL, RabbitMQ, Redis y Meilisearch reales, con las mismas imágenes que el compose, que levanta [testcontainers-go](https://golang.testcontainers.org/) en puertos libres del host. No hace falta tener nada corriendo ni definir variables de entorno, pero sí **Docker corriendo**:

```bash
go test -tags=integration ./...
```

- Los tests están en archivos `*_integration_test.go` con el build tag `integration`, así que `go test ./...` no los compila.
- La primera corrida descarga la imagen `postgres:16-alpine` y tarda más.
- **Si falla con `reaper: ... "Started" matched 0 times`**, el problema es el contenedor reaper (Ryuk) de testcontainers, no los tests: la espera de readiness de testcontainers-go v0.44.0 no reconoce el arranque de `ryuk:0.14.0` en algunos entornos de Docker Desktop. Los tests pasan desactivándolo, y los contenedores de la corrida se limpian igual al terminar:

  ```bash
  TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration ./internal/adapters/postgres/ -v
  ```
- Cubren las migraciones embebidas (carga inicial e idempotencia), las consultas de ofertas (SKU con ofertas, producto sin ofertas, SKU inexistente), `Ping`, el comportamiento con la base caída, la aplicación de eventos, el consumidor con dos réplicas, la caché y la búsqueda con sus filtros.
- Los contenedores se eliminan al terminar, aunque los tests fallen. Si Docker no está disponible, la corrida falla con un mensaje que lo indica.

## API

### `GET /api/v1/proveedores/recomendados`

Requiere el header `X-API-Key`.

| Parámetro | Obligatorio | Default | Regla |
|---|---|---|---|
| `producto` | sí | — | SKU del producto |
| `cantidad` | no | 1 | entero > 0 |
| `limite` | no | 5 | entero > 0, máximo 50 |

**Regla de negocio**

- `costo_total = precio_unitario × cantidad × (1 − descuento_pct/100) + flete`, redondeado a 2 decimales.
- El descuento solo aplica si `cantidad ≥ cantidad_minima_descuento`.
- Se excluyen las ofertas con `stock_disponible < cantidad`.
- Orden: `costo_total` ascendente; si empatan, nombre del proveedor ascendente y después `proveedor_id`.
- Un producto que existe pero no tiene ofertas válidas devuelve `200` con `[]`. Un SKU inexistente devuelve `404`.

**Ejemplo**

```bash
curl -H "X-API-Key: dev" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

```json
[
  {"proveedor_id":3,"proveedor_nombre":"Cordoba Energia SA","precio_unitario":189573.99,"descuento_pct":15,"descuento_aplicado":true,"flete":43000,"stock_disponible":394,"costo_total":1654378.92},
  {"proveedor_id":6,"proveedor_nombre":"Fotovoltaica Pampeana SRL","precio_unitario":199645.5,"descuento_pct":3,"descuento_aplicado":true,"flete":64500,"stock_disponible":62,"costo_total":2001061.35}
]
```

SKUs útiles del seed: `PAN-550W-MONO` (10 ofertas, sirve para probar `limite`), `INV-5KW-HIB`, `BAT-LFP-10KWH`, `CAB-SOLAR-4MM`, `MON-WIFI` (existe pero no tiene ofertas: devuelve `[]`).

**Errores** (`application/problem+json`)

| Status | `code` | Cuándo |
|---|---|---|
| 400 | `PARAMETRO_INVALIDO` | falta `producto`, o `cantidad`/`limite` no son enteros > 0, o `limite` > 50 |
| 401 | `NO_AUTORIZADO` | falta `X-API-Key` o no es válida |
| 404 | `PRODUCTO_NO_ENCONTRADO` | el SKU no existe |
| 503 | `NO_DISPONIBLE` | la base falla o no responde en 2 s; reintentar más tarde |

```json
{"type":"https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#PRODUCTO_NO_ENCONTRADO","title":"Producto no encontrado","status":404,"detail":"no existe un producto con SKU 'NOPE'","code":"PRODUCTO_NO_ENCONTRADO","instance":"/api/v1/proveedores/recomendados","request_id":"demo-1"}
```

### `GET /api/v1/productos/buscar`

Requiere el header `X-API-Key`. Es una lectura derivada del índice para el frontend: **no es parte del contrato publicado** a paneles solares ni reemplaza al ranking. Su `precio_min` no incluye flete ni descuento por volumen.

| Parámetro | Default | Regla |
|---|---|---|
| `q` | vacío (todos) | texto libre, con tolerancia a errores de tipeo, sobre nombre, SKU y proveedores |
| `categoria` | — | valor exacto |
| `proveedor` | — | nombre exacto de un proveedor **con stock** de ese producto |
| `precio_min`, `precio_max` | — | número ≥ 0, sobre el menor precio con stock |
| `con_stock` | `false` | `true` deja solo productos con algún proveedor con stock |
| `orden` | relevancia | `precio_asc` o `precio_desc`; los productos sin stock quedan al final |
| `pagina` | 1 | entero > 0 |
| `tamanio` | 20 | entero > 0, máximo 50 |

```bash
curl -H "X-API-Key: clave-de-desarrollo" \
  "http://localhost:8080/api/v1/productos/buscar?q=buzo&con_stock=true&orden=precio_asc&tamanio=1"
```

```json
{"resultados":[{"sku":"FAV-var-buzo-canguro-azul-l","nombre":"Buzo canguro · talle L · Azul","categoria":"ropa-buzos","unidad":"unidad","precio_min":38000,"proveedores_con_stock":1,"stock_total":10,"proveedores":["FAV (tienda de ropa)"],"actualizado_en":"2026-11-06T17:46:53.697346Z"}],"total":2,"pagina":1,"tamanio":1}
```

Errores en `application/problem+json`: `400 PARAMETRO_INVALIDO`, `401 NO_AUTORIZADO` y `503 NO_DISPONIBLE` si Meilisearch no responde en 2 s. La capacidad sigue en 200 aunque la búsqueda esté en 503.

### `GET /healthz` y `GET /readyz`

Sin autenticación. `/healthz` indica que el proceso está vivo. `/readyz` hace ping a la base (timeout 2 s) y devuelve `503 NO_DISPONIBLE` si no responde. Ninguno de los dos mira a RabbitMQ, Redis ni Meilisearch.

## Observabilidad

- Cada línea de log es JSON e incluye `service`.
- Cada request registra una línea `request` con `request_id`, método, path, status, bytes y `duration_ms` (nivel WARN para 4xx y ERROR para 5xx).
- Si la request trae `X-Request-ID` (ASCII imprimible, hasta 128 caracteres) se reutiliza; si no, se genera uno. Siempre vuelve en el header `X-Request-ID` de la respuesta y en el campo `request_id` de los errores.
- El consumidor continúa la traza del `traceparent` del evento y abre un span propio. Las consultas de esa aplicación quedan como hijas suyas.
