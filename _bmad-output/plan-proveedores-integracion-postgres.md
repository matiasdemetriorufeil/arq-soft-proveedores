---
title: 'proveedores-service: tests de integración del adaptador PostgreSQL contra una base real'
type: 'feature'
ticket: ''
created: '2026-10-22'
status: 'built'
baseline_revision: '0cc5ed574d476126cbf8597621c2cd5e792d6bba'
route: 'full'
route_source: 'auto'
risk: 'low'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context: []
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** La consigna (2.11, punto 2) exige tests de integración por servicio contra una dependencia real, y la defensa individual los evalúa. `internal/adapters/postgres` de `proveedores-service` tiene hoy 0 % de cobertura.

**Approach:** Agregar tests con build tag `integration` que levantan un PostgreSQL 16 efímero con testcontainers-go y prueban `Migrar`, `OfertasPorProducto` y `Ping` contra él. Se corren con un único comando documentado y sin configuración manual, y `go test ./...` sigue funcionando sin Docker. No commitear ni pushear: al terminar, darle al usuario el commit sugerido y los comandos.

## Boundaries & Constraints

**Always:**
- Los tests van en archivos `*_integration_test.go` con `//go:build integration` en la primera línea, así `go test ./...` no los compila.
- Usar `github.com/testcontainers/testcontainers-go` y su módulo `modules/postgres`, con imagen `postgres:16-alpine`. Un solo contenedor por corrida del paquete (`TestMain`), que se termina al final aunque los tests fallen.
- Los tests usan las migraciones embebidas reales (`migrations.FS`) y las funciones públicas del paquete: `NuevoPool`, `NuevoRepo`, `Migrar`, `OfertasPorProducto` y `Ping`.
- Con el tag puesto y sin Docker disponible, el test falla con un mensaje claro. No se saltea en silencio.
- El comando se documenta en `services/proveedores-service/README.md`, sección Tests: `go test -tags=integration ./internal/adapters/postgres/ -v`.

**Never:**
- No modificar código de producción salvo que un test revele un bug real; en ese caso, anotarlo en Implementation Notes.
- No depender de un Postgres ya corriendo, de puertos fijos del host (el 5432 está ocupado en la máquina del usuario) ni de variables de entorno.
- No tocar los tests unitarios existentes.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Migración inicial | base vacía | `Migrar` sin error; 10 proveedores, 30 productos, N ofertas > 0 | — |
| Idempotencia | `Migrar` corrido 2 veces | los conteos no cambian | — |
| SKU con ofertas | `PAN-450W-MONO` | 3 ofertas con proveedor, precio, descuento, mínimo, flete y stock iguales al seed | — |
| Producto sin ofertas | `MON-WIFI` | lista vacía no nil, `err == nil` | — |
| SKU inexistente | `NOPE-123` | `errors.Is(err, domain.ErrProductoNoEncontrado)` | — |
| Ping | base arriba | `nil` | — |
| Base caída | contenedor detenido | `OfertasPorProducto` y `Ping` devuelven error distinto de `ErrProductoNoEncontrado`, sin panic, en menos de 5 s | — |

</frozen-after-approval>

## Code Map

- `services/proveedores-service/internal/adapters/postgres/repo.go` -- `NuevoPool(ctx, url)`, `NuevoRepo(pool)`, `OfertasPorProducto`, `Ping`; `TimeoutConsulta = 2s`. No modificar.
- `services/proveedores-service/internal/adapters/postgres/migrate.go` -- `Migrar(ctx, pool, fs.FS, *slog.Logger)`; aplica `*.sql` en una transacción con advisory lock. No modificar.
- `services/proveedores-service/migrations/embed.go` -- `migrations.FS` (embed). Reutilizar.
- `services/proveedores-service/migrations/002_seed.sql` -- 10 proveedores y 30 productos. `PAN-450W-MONO` tiene 3 ofertas (proveedores 2, 6 y 9; la del 9 tiene stock 0). `MON-WIFI` no tiene ofertas.
- `services/proveedores-service/README.md` -- la sección `### Tests` (cerca de la línea 60) solo tiene `go vet ./... && go test ./... -cover`.
- `services/proveedores-service/go.mod` -- go 1.26.1, pgx v5.11.0; hay que agregar testcontainers-go.

## Tasks & Acceptance

**Execution:**
- [x] `services/proveedores-service/go.mod`, `go.sum` -- `go get github.com/testcontainers/testcontainers-go/modules/postgres` (una versión estable, publicada hace más de dos semanas) -- dependencia solo de tests
- [x] `services/proveedores-service/internal/adapters/postgres/main_integration_test.go` -- `TestMain`: levanta el contenedor (esperando a que la base acepte conexiones), arma el pool compartido, corre los tests y termina el contenedor
- [x] `services/proveedores-service/internal/adapters/postgres/repo_integration_test.go` -- un test por fila de la matriz. El caso "Base caída" usa su propio contenedor, para no romper el compartido.
- [x] `services/proveedores-service/README.md` -- documentar el comando, el requisito de Docker y que la primera corrida descarga la imagen

**Acceptance Criteria:**
- Given Docker corriendo, when se ejecuta `go test -tags=integration ./internal/adapters/postgres/ -v`, then todos los tests pasan y no queda ningún contenedor de test vivo al terminar.
- Given cualquier entorno, when se ejecuta `go test ./...`, then compila y pasa igual que antes, sin requerir Docker.
- Given la corrida con tag, then la cobertura de `internal/adapters/postgres` es ≥ 70 %.

## Implementation Notes

- testcontainers-go y `modules/postgres` v0.44.0 (publicada 2026-08-07). `go mod tidy` además subió indirectas de pgx (`golang.org/x/sync` 0.17→0.22, `golang.org/x/text` 0.29→0.40); pgx sigue en v5.11.0.
- Ningún test reveló bugs; no se tocó código de producción.
- `levantarPostgres` convierte un posible panic de testcontainers en error, para que la falta de Docker termine en un mensaje claro y `exit 1`. Esa ruta no se probó apagando Docker: con `DOCKER_HOST` inválido testcontainers cae al socket de Docker Desktop y los tests pasan igual.
- El test de SKU con ofertas ordena por `proveedor_id` antes de comparar, porque la consulta no fija un orden.

## Plan Change Log

## Review Triage Log

**Pass 1 (quick):** high 0 · medium 0 · low 3 · false 1 · maybe-false 1

| # | Hallazgo | Veredicto | Ruta | Evidencia / acción |
|---|---|---|---|---|
| 1 | `go.sum` falta en el diff | false | rechazado | El diff de revisión excluyó `go.sum` a propósito por su tamaño; `git status` lo muestra modificado con las entradas de testcontainers y el commit sugerido lo incluye. |
| 2 | testcontainers queda en `require` y sube x/sync y x/text en el binario | low | rechazado | Real: un módulo Go no tiene requires solo de test, y MVS sube esas dos indirectas de pgx (versiones menores). El binario no importa testcontainers (`docker build` OK). Evitarlo exige un módulo aparte. |
| 3 | `TestMigrar_BaseVacia` no comprueba que la base estuviera vacía | low | rechazado | Real solo con `-shuffle`/`-run`/`-count>1`; en la corrida documentada es el primer test sobre un contenedor nuevo. |
| 4 | El camino "sin Docker" no se probó | maybe-false | rechazado | Para comprobarlo hay que detener Docker Desktop, lo que afecta a otros contenedores del usuario. El código convierte error y panic en exit 1 con mensaje; en el peor caso sería low. |
| 5 | Ryuk queda vivo unos segundos después de `go test` | low | rechazado | Real y cosmético: a los 15 s `docker ps -a --filter label=org.testcontainers=true` dio vacío. |

## Verification

**Commands:**
- `cd services/proveedores-service && go vet -tags=integration ./... && go test ./... -cover` -- expected: pasa sin Docker; postgres sigue en 0 % sin el tag
- `go test -tags=integration ./internal/adapters/postgres/ -v -cover` -- expected: todos PASS, cobertura ≥ 70 %
- `docker ps -a --filter label=org.testcontainers=true` -- expected: vacío poco después de terminar
