---
title: 'proveedores-service: capacidad de proveedores recomendados (Entrega 2, Integrante 1)'
type: 'feature'
ticket: ''
created: '2026-10-22'
status: 'built'
baseline_revision: '1ce7748200ea0d7a473179f68c0508ff95294954'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/docs/consigna-practico-integrador-2026.md'
  - '{project-root}/docs/TP-Integrador-Complemento de Consigna.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** Para la Entrega 2 (23/10) la consigna exige al menos un servicio operativo con la capacidad compartida funcionando (ya sin mock) y un almacenamiento integrado. El grupo de paneles solares consume nuestra recomendación de proveedores con precios, y hoy no existe nada de código.

**Approach:** Crear `services/proveedores-service` en Go con arquitectura hexagonal (domain / ports / adapters). Expone `GET /api/v1/proveedores/recomendados`, que rankea las ofertas leídas de PostgreSQL por costo total. Incluye tests unitarios del dominio, logs JSON, Dockerfile y el ADR D2. El trabajo se entrega en cuatro partes; al terminar cada una se le dan al usuario el mensaje de commit sugerido y los comandos, sin commitear ni pushear.

## Boundaries & Constraints

**Always:**
- Go con biblioteca estándar (`net/http` ServeMux 1.22+, `log/slog` JSON) y `github.com/jackc/pgx/v5` como única dependencia de runtime.
- `internal/domain` no importa nada fuera de la stdlib. Los adaptadores dependen de `ports` y de `domain`, nunca al revés.
- Regla: `costo_total = precio_unitario × cantidad × (1 − descuento_pct/100) + flete`. El descuento solo aplica si `cantidad ≥ cantidad_minima_descuento`; se excluyen las ofertas con `stock_disponible < cantidad`. Orden por `costo_total` asc, desempate por nombre de proveedor asc y después por `proveedor_id`. `costo_total` se redondea a 2 decimales.
- Parámetros: `producto` (SKU) obligatorio, `cantidad` por defecto 1, `limite` por defecto 5 y máximo 50; ambos tienen que ser enteros > 0.
- Autenticación con el header `X-API-Key`, validado contra la variable de entorno `API_KEYS` (lista separada por comas). Si `API_KEYS` está vacía, el servicio no arranca.
- Errores en `application/problem+json` con `type`, `title`, `status`, `detail` y `code` ∈ {PARAMETRO_INVALIDO, NO_AUTORIZADO, PRODUCTO_NO_ENCONTRADO, NO_DISPONIBLE}.
- Las consultas a la base usan un timeout de 2 s. Una falla de la base o un timeout devuelven 503 NO_DISPONIBLE.
- Las migraciones van embebidas (`embed`) y se aplican al arrancar: el DDL es idempotente y el seed usa `ON CONFLICT DO NOTHING`.
- Sin secretos en el repo: toda la configuración viene de variables de entorno (`DATABASE_URL`, `API_KEYS`, `PORT` con default 8080).
- Pausar al final de cada parte (A dominio · B Postgres · C HTTP + main + Docker · D ADR) y darle al usuario el commit sugerido y los comandos git. No commitear ni pushear nunca.

**Never:**
- No tocar `docker-compose.yml`, el api-gateway, el paquete compartido de observabilidad ni el OpenAPI: son de los Integrantes 2 y 3.
- Sin frameworks web ni ORM, sin trazas OpenTelemetry (lo integra el Integrante 2), sin caché, búsqueda ni mensajería.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Feliz | SKU con 3 ofertas válidas, cantidad=10 | 200, lista ordenada por costo_total asc | — |
| Umbral descuento | cantidad == cantidad_minima | se aplica el descuento | — |
| Sin stock suficiente | stock < cantidad en todas | 200 con `[]` | — |
| Límite | 8 ofertas válidas, limite=3 | las 3 más baratas | — |
| SKU inexistente | producto=NOPE | 404 PRODUCTO_NO_ENCONTRADO | problem+json |
| Parámetro inválido | sin producto, cantidad=0, limite=abc o limite=51 | 400 PARAMETRO_INVALIDO | problem+json |
| Sin o con mala key | X-API-Key ausente o incorrecta | 401 NO_AUTORIZADO | problem+json |
| Base caída o lenta | Postgres no responde | 503 NO_DISPONIBLE | log error con request_id |

</frozen-after-approval>

## Code Map

- `services/proveedores-service/go.mod` -- ya existe (module `github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service`, go 1.26.1); reutilizarlo y agregar pgx.
- `docs/adr/` -- no existe; D2 es el primer ADR. Los campos salen de la sección 6.1 del complemento.
- El resto del repo no tiene código; no hay convenciones previas que respetar.

## Tasks & Acceptance

**Execution:**
- [x] **Parte A** `internal/domain/oferta.go`, `ranking.go` -- tipos `Oferta` y `Recomendacion`, `Rankear(ofertas, cantidad, limite)` y los errores de dominio (`ErrParametroInvalido`, `ErrProductoNoEncontrado`) -- reglas puras y testeables
- [x] **Parte A** `internal/domain/ranking_test.go` -- tests de tabla para las filas de dominio de la matriz y los desempates -- cobertura ≥ 80 %
- [x] **Parte A** `internal/ports/repositorio.go` -- `OfertasRepo.OfertasPorProducto(ctx, sku) ([]domain.Oferta, error)` -- devuelve `ErrProductoNoEncontrado` si el SKU no existe
- [x] **Parte B** `migrations/001_init.sql` y `migrations/002_seed.sql` -- tablas proveedores, productos y ofertas; seed de 10 proveedores, 30 productos y ofertas variadas (algunas sin stock, con distintos umbrales de descuento) -- datos para la demo
- [x] **Parte B** `internal/adapters/postgres/repo.go` y `migrate.go` -- pool pgx, query con join y aplicación de las migraciones embebidas
- [x] **Parte C** `internal/adapters/http/handler.go`, `middleware.go`, `problem.go` -- endpoint, API key, request log con `X-Request-ID` (generado si falta, devuelto en la respuesta) y `/healthz` + `/readyz` (ping a la base)
- [x] **Parte C** `internal/adapters/http/handler_test.go` -- tests con un repo falso para 400, 401, 404, 503 y 200
- [x] **Parte C** `cmd/server/main.go`, `Dockerfile`, `.dockerignore`, `README.md` del servicio -- arranque, apagado ordenado, imagen multi-stage y cómo correrlo
- [x] **Parte D** `docs/adr/ADR-002-arquitectura-interna.md` -- D2: hexagonal acá y patrón distinto propuesto para otro servicio

**Acceptance Criteria:**
- Given Postgres corriendo y `DATABASE_URL`/`API_KEYS` seteadas, when se ejecuta `go run ./cmd/server`, then las tablas y el seed se crean y una consulta con un SKU del seed devuelve 200 con ofertas rankeadas.
- Given el servicio arrancado dos veces sobre la misma base, then el seed no se duplica.
- Given cualquier request, then cada línea de log es JSON e incluye el `request_id` que vuelve en el header `X-Request-ID`.
- Given `docker build`, then la imagen se construye sin errores.

## Implementation Notes

- Las migraciones viven en `migrations/` (raíz del servicio) con un `embed.go` propio: `go:embed` no puede referenciar directorios padre, así que `internal/adapters/postgres/migrate.go` recibe el `fs.FS` desde `main`.
- Se aplican todas en cada arranque dentro de una transacción con `pg_advisory_xact_lock`, para que dos instancias no corran el seed a la vez. `main` reintenta 15 veces, cada 2 s, si Postgres todavía no está listo.
- La respuesta 200 es un array JSON plano (`[]` si no hay ofertas válidas), tomando la matriz al pie de la letra. Los problem+json agregan `instance` y `request_id` además de los campos obligatorios.
- `redondear2` redondea primero a 6 decimales para que un valor como 1654378.915 no quede en .91 por ruido de punto flotante.
- Seed generado de forma determinista: 10 proveedores, 30 productos y 170 ofertas. `PAN-550W-MONO` tiene 10 ofertas y `MON-WIFI` ninguna.

## Plan Change Log

## Review Triage Log

**Pass 1 (quick):** high 0 · medium 2 · low 5 · false 0 · maybe-false 0

| # | Hallazgo | Veredicto | Ruta | Evidencia / acción |
|---|---|---|---|---|
| 1 | Rutas desconocidas y 405 devuelven text/plain, no problem+json | low | rechazado | Real (ServeMux sin catch-all), pero el contrato documenta método y ruta; arreglarlo agrega un handler y un código fuera de los 4 del contrato. |
| 2 | Cancelación del cliente se loguea como ERROR y responde 503 | medium | patch | Real: `r.Context()` cancelado → pgx devuelve context.Canceled → rama `err != nil`. Infla métricas 5xx. Se chequea `r.Context().Err()` antes. |
| 3 | URL `type` de problem+json no resuelve en GitHub | low | patch | Real: falta `/blob/main/`. Corrección directa de la constante y del ejemplo del README. |
| 4 | Migraciones sin timeout por intento pueden colgar el arranque | medium | patch | Real: `ConnectTimeout` no cubre `Exec`; con base pausada o lock retenido el loop no avanza. `context.WithTimeout` por intento. |
| 5 | Se reintentan errores no transitorios (SQL inválido, credenciales) | low | rechazado | Real, pero solo demora ~30 s el fallo; clasificar errores agrega ramas. |
| 6 | "migración aplicada" se loguea antes del commit | low | patch | Real: el log está dentro de `BeginFunc`. Se mueve después del commit. |
| 7 | Un panic se loguea sin request_id | low | rechazado | Real (no hay recover middleware), pero un panic no se observó en uso normal y el arreglo agrega un middleware. |

## Design Notes

El dominio recibe las ofertas ya cargadas y no conoce la base: el handler llama a `repo.OfertasPorProducto` y después a `domain.Rankear`. Un producto que existe pero no tiene ofertas válidas devuelve 200 con `[]`, no 404: así el consumidor distingue "no existe" de "no hay quién lo provea ahora".

## Verification

**Commands:**
- `cd services/proveedores-service && go vet ./... && go test ./... -cover` -- expected: todo pasa, `domain` ≥ 80 %
- `docker build -t proveedores-service services/proveedores-service` -- expected: build OK
- `docker run -d --name pg-prov -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:16`, después `go run ./cmd/server` y `curl -H "X-API-Key: dev" ".../recomendados?producto=<sku del seed>&cantidad=10"` -- expected: 200 con JSON rankeado
