# proveedores-service

Servicio en Go que publica la capacidad **proveedores recomendados** que consume el grupo de paneles solares. Para un producto (SKU) y una cantidad, rankea las ofertas de los proveedores por **costo total** y devuelve las más convenientes.

- Arquitectura hexagonal (puertos y adaptadores). Ver [ADR-002](../../docs/adr/ADR-002-arquitectura-interna.md).
- Almacenamiento: PostgreSQL (pgx v5). Las migraciones están embebidas en el binario y se aplican al arrancar.
- Logs JSON (`log/slog`), correlacionados con `X-Request-ID`.

## Estructura

```
cmd/server/                 arranque, configuración y apagado ordenado
internal/domain/            reglas de negocio puras (Oferta, Recomendacion, Rankear)
internal/ports/             interfaces que el núcleo necesita (OfertasRepo, Salud)
internal/adapters/http/     endpoint, API key, request log, problem+json, health checks
internal/adapters/postgres/ pool pgx, consulta de ofertas, aplicación de migraciones
migrations/                 001_init.sql (esquema) y 002_seed.sql (datos de demo), embebidos
```

`domain` solo usa la biblioteca estándar. Los adaptadores dependen de `ports` y `domain`, nunca al revés.

## Configuración

Todo se configura por variables de entorno; no hay secretos en el repo.

| Variable | Obligatoria | Descripción |
|---|---|---|
| `DATABASE_URL` | sí | URL de PostgreSQL, por ejemplo `postgres://postgres:dev@localhost:5432/postgres?sslmode=disable` |
| `API_KEYS` | sí | Claves aceptadas en `X-API-Key`, separadas por coma. Si está vacía, el servicio no arranca. |
| `PORT` | no | Puerto HTTP. Por defecto `8080`. |

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

```bash
docker build -t proveedores-service services/proveedores-service
docker run --rm -p 8080:8080 \
  -e DATABASE_URL="postgres://postgres:dev@host.docker.internal:5432/postgres?sslmode=disable" \
  -e API_KEYS="dev" \
  proveedores-service
```

La imagen es multi-stage: compila con `golang:1.26-alpine` y corre sobre `distroless/static` como usuario no root.

### Tests

```bash
go vet ./... && go test ./... -cover
```

Estos tests no necesitan Docker ni una base de datos.

**Integración con PostgreSQL.** El adaptador `internal/adapters/postgres` se prueba contra un PostgreSQL 16 real (`postgres:16-alpine`) que levanta [testcontainers-go](https://golang.testcontainers.org/) en un puerto libre del host. No hace falta tener Postgres corriendo ni definir variables de entorno, pero sí **Docker corriendo**:

```bash
go test -tags=integration ./internal/adapters/postgres/ -v
```

- Los tests están en archivos `*_integration_test.go` con el build tag `integration`, así que `go test ./...` no los compila.
- La primera corrida descarga la imagen `postgres:16-alpine` y tarda más.
- Cubren las migraciones embebidas (carga inicial e idempotencia), las consultas de ofertas (SKU con ofertas, producto sin ofertas, SKU inexistente), `Ping` y el comportamiento con la base caída.
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

### `GET /healthz` y `GET /readyz`

Sin autenticación. `/healthz` indica que el proceso está vivo. `/readyz` hace ping a la base (timeout 2 s) y devuelve `503 NO_DISPONIBLE` si no responde.

## Observabilidad

- Cada línea de log es JSON e incluye `service`.
- Cada request registra una línea `request` con `request_id`, método, path, status, bytes y `duration_ms` (nivel WARN para 4xx y ERROR para 5xx).
- Si la request trae `X-Request-ID` (ASCII imprimible, hasta 128 caracteres) se reutiliza; si no, se genera uno. Siempre vuelve en el header `X-Request-ID` de la respuesta y en el campo `request_id` de los errores.
- Las trazas OpenTelemetry quedan para la integración del paquete compartido de observabilidad.
