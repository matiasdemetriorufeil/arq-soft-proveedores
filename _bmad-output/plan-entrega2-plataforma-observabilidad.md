---
title: 'Plataforma: paquete de observabilidad, api-gateway con balanceo y arranque con un comando (Entrega 2, Integrante 2)'
type: 'feature'
ticket: ''
created: '2026-10-22'
status: 'built'
baseline_revision: 'd8f9e4749d137c34fef00cc7f0e0ffab1937f9d8'
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

**Problem:** El repo no tiene compose, ni `.env.example`, ni `.gitignore`, ni gateway, ni paquete de observabilidad: `proveedores-service` es el único código y se arranca a mano. La consigna 2.12 exige levantar el sistema "mediante un procedimiento único, automatizado y documentado", y la 6.3 pide logs estructurados y correlacionados para esta entrega.

**Approach:** `pkg/observabilidad` (logger JSON que agrega `trace_id`/`span_id` desde el contexto + init de OpenTelemetry), `services/api-gateway` como entrada única con API key, `X-Request-ID`, span padre y round-robin sobre dos réplicas, y un `docker-compose.yml` que levanta postgres, jaeger, gateway y las dos réplicas con un comando. Tres partes; al cerrar cada una, commit sugerido al usuario, sin commitear ni pushear.

**Decisiones del usuario al aprobar:**
- El balanceo se implementa ahora (dos réplicas con health checks), para que el ADR D12 describa algo que existe.
- Instrumentar `proveedores-service` va en un plan y commit aparte, inmediatamente después: son archivos del Integrante 1. Acá la traza llega hasta la llamada de salida del gateway, no hasta Postgres.
- **Renegociado después de la revisión (pass 1, hallazgo 6):** el gateway puede definir `code` propios para errores que el contrato de la capacidad no describe. Queda ratificado `RUTA_NO_ENCONTRADA` para el 404 de una ruta que el gateway no expone, porque ninguno de los cuatro códigos del contrato la describe y `PRODUCTO_NO_ENCONTRADO` le mentiría al cliente. Hay que avisarle al Integrante 3 para que lo incluya en el OpenAPI.

## Boundaries & Constraints

**Always:**
- Biblioteca estándar (`net/http`, `httputil.ReverseProxy`, `log/slog`); únicas dependencias nuevas: `otel`, `otel/sdk` y `otlptracehttp` v1.47.0 más `otelhttp` v0.72.0.
- `pkg/observabilidad` es un módulo aparte, declarado con los servicios en un `go.work`; no importa nada de los servicios. Su API (`NuevoLogger`, `IniciarTrazas`) tiene que servir tal cual cuando el servicio la adopte.
- Sin endpoint OTLP, `IniciarTrazas` es no-op: los binarios arrancan igual y `go test ./...` no necesita Docker.
- El gateway replica los criterios del servicio: propagación W3C `traceparent`, `X-Request-ID` entrante respetado si es válido y si no 32 hex nuevos, siempre devuelto y puesto como atributo del span; `X-API-Key` comparada en tiempo constante y reenviada al upstream; errores en `application/problem+json` con los `code` del contrato existente, más los propios del gateway que la renegociación de arriba autoriza.
- Round-robin sobre upstreams por env, con sondeo de `/healthz`, marcado de instancia caída, reintento único del `GET` en la siguiente, y el upstream elegido como atributo del span.
- Configuración solo por variables de entorno, `.env.example` versionado y `.env` ignorado. `docker compose up` sola deja los cinco contenedores sanos sin pasos manuales.
- Pausar al final de cada parte (A paquete · B gateway · C compose) con el commit sugerido.

**Never:**
- No tocar ningún archivo de `services/proveedores-service`: su contexto de build y su Dockerfile quedan como están (eso es del plan siguiente). Tampoco el OpenAPI ni el README raíz (Integrante 3), ni los ADR.
- Sin rate limiting ni control de tráfico, sin métricas, tablero ni alertas: la consigna los pide para la Presentación y la defensa, no para esta entrega.
- Sin frameworks web, sin service mesh, sin Prometheus/Grafana, sin caché.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Feliz | `GET /api/v1/proveedores/recomendados?producto=PAN-450W-MONO&cantidad=10`, API key válida | 200, cuerpo del servicio intacto, `X-Request-ID` en la respuesta | — |
| Request-ID entrante | `X-Request-ID: abc123` | se respeta y vuelve igual, y aparece en los logs | — |
| Sin Request-ID | ausente o inválido | se genera de 32 hex y vuelve | — |
| API key ausente o mala | `X-API-Key` falta o no coincide | 401 `NO_AUTORIZADO`, sin llamar al upstream | problem+json |
| Reparto | 4 requests, dos réplicas sanas | 2 y 2; el atributo `upstream` del span lo muestra | — |
| Una réplica caída | réplica 1 detenida | 200 por la 2; la 1 queda marcada y deja de recibir | reintento único |
| Las dos caídas | servicio detenido | 502 `NO_DISPONIBLE`, log error con `request_id` y `trace_id` | sin panic |
| Upstream lento | tarda más que `TIMEOUT_UPSTREAM` | 504 `NO_DISPONIBLE` | — |
| Ruta desconocida | `GET /nope` | 404 problem+json | — |
| Salud propia | `/healthz`, `/readyz` | `healthz` 200 siempre; `readyz` 503 si ninguna réplica responde | — |
| Jaeger caído | sin colector OTLP | responde 200 igual; el exportador no bloquea | log warn |

</frozen-after-approval>

## Code Map

Todo es nuevo. De `proveedores-service` solo se lee, para que gateway y compose encajen con lo que existe.

- `cmd/server/main.go:30-56` -- `DATABASE_URL` y `API_KEYS` obligatorias (sin ellas no arranca), `PORT` default 8080: el compose las pasa. `main.go:132-152` aplica migraciones y seed al arrancar con 15 reintentos cada 2 s → el contenedor de Postgres no necesita scripts de inicio.
- `internal/adapters/http/handler.go:29-36` -- rutas `GET /api/v1/proveedores/recomendados`, `/healthz` (200 fijo, no toca la base) y `/readyz` (ping a la base). `readyz` es el healthcheck del compose; `healthz`, el que sondea el balanceador.
- `internal/adapters/http/problem.go:17-18,39-52` -- constantes `X-Request-ID`/`X-API-Key` y la forma exacta del problem+json (`type`, `title`, `status`, `detail`, `code`, `instance`, `request_id`), que el gateway replica.
- `internal/adapters/http/middleware.go:40-56` -- request-id = 16 bytes de `crypto/rand` en hex, validado por longitud y ASCII imprimible: mismo criterio en el gateway.
- `Dockerfile:4-20` y `go.mod:1-9` -- build `golang:1.26-alpine` → runtime `distroless/static-debian12:nonroot`, `EXPOSE 8080`, contexto = directorio del servicio; module `github.com/matiasdemetriorufeil/arq-soft-proveedores/services/proveedores-service`, `go 1.26.1`. El gateway imita ambos y usa el mismo prefijo de módulo.
- `migrations/002_seed.sql` -- `PAN-450W-MONO` tiene 3 ofertas: ese es el SKU de prueba y de la demo.

## Tasks & Acceptance

**Execution:**
- [x] **Parte A** `go.work`, `pkg/observabilidad/go.mod` -- workspace con los tres módulos -- importar el paquete sin publicarlo
- [x] **Parte A** `pkg/observabilidad/logger.go` -- `NuevoLogger(servicio)`: handler que envuelve al JSON y agrega `trace_id`/`span_id` del contexto -- es la correlación que pide 6.3
- [x] **Parte A** `pkg/observabilidad/trazas.go` -- `IniciarTrazas(ctx, servicio)`: exportador `otlptracehttp`, `resource` con `service.name`, propagador W3C, `shutdown`; no-op sin endpoint
- [x] **Parte A** `pkg/observabilidad/logger_test.go`, `trazas_test.go` -- `trace_id` presente con span y ausente sin él; el no-op no falla
- [x] **Parte B** `services/api-gateway/go.mod`, `cmd/server/main.go` -- env (`PORT`, `UPSTREAMS_PROVEEDORES`, `API_KEYS`, `TIMEOUT_UPSTREAM`, `INTERVALO_SALUD`, `OTEL_EXPORTER_OTLP_ENDPOINT`), logger y trazas del paquete, apagado de 10 s
- [x] **Parte B** `internal/gateway/balanceador.go` -- round-robin con estado por instancia, sondeo de `/healthz` en segundo plano, reintento único ante fallo de conexión
- [x] **Parte B** `internal/gateway/proxy.go`, `middleware.go`, `problem.go` -- `ReverseProxy` con `Rewrite`, transporte `otelhttp`, request-id, API key, `ErrorHandler` a 502/504, `/healthz` y `/readyz` propios
- [x] **Parte B** `internal/gateway/gateway_test.go`, `balanceador_test.go` -- un test por fila de la matriz, con `httptest`
- [x] **Parte B** `services/api-gateway/Dockerfile`, `README.md` -- multi-stage distroless; contexto de raíz, porque el módulo depende de `pkg/observabilidad`. Sin `.dockerignore` propio: ver las notas
- [x] **Parte C** `docker-compose.yml`, `.env.example`, `.gitignore`, `.dockerignore` raíz -- postgres 16-alpine, `jaegertracing/all-in-one:1.76.0`, gateway y dos réplicas con la misma imagen; healthchecks (`pg_isready`, `/readyz`), `depends_on: service_healthy`, red interna, y solo gateway y UI de Jaeger publicados al host

**Acceptance Criteria:**
- Given un clon limpio y `cp .env.example .env`, when se corre `docker compose up -d`, then los cinco contenedores quedan `healthy` sin pasos manuales y el `curl` con API key devuelve 200.
- Given una request que pasó por el gateway, when se miran sus logs, then cada línea es JSON con `service`, `request_id` y `trace_id`, y el `request_id` es el que volvió en el header.
- Given Jaeger arriba, when se abre la UI en 16686, then está el servicio `api-gateway` con una traza del flujo y el atributo del upstream elegido.
- Given `go test ./...` en los dos módulos nuevos sin Docker, then compila y pasa.
- Given `git grep` sobre el repo, then no hay contraseñas ni API keys fuera de `.env.example`, y `.env` está ignorado.

## Implementation Notes

**`go work sync` no se puede correr.** Empuja la lista de build del workspace a
los `go.mod` de los miembros, y eso reescribe
`services/proveedores-service/go.mod` y su `go.sum` (sube `otel` de 1.44.0 a
1.47.0 y `otelhttp` de 0.69.0 a 0.72.0, que le entran como indirectas de
testcontainers). Tocar esos archivos está prohibido en los Boundaries, así que
gana el Boundary sobre el comando de Verification. No hace falta: el `go.work.sum`
que genera el primer build trae los hashes que el servicio necesita en modo
workspace, y se verificó que `go build`, `go vet` y sus tests unitarios pasan con
el workspace puesto y su `go.mod` sin cambios.

**El gateway no depende del `go.work` para compilar.** `services/api-gateway/go.mod`
tiene un `replace` a `../../pkg/observabilidad`. Así el módulo compila solo, con
el contexto de build de Docker, que no incluye el workspace y por lo tanto no
arrastra el módulo de `proveedores-service` ni sus dependencias de test.

**Healthcheck de las réplicas: busybox por volumen.** La consigna del plan pide
healthchecks y `depends_on: service_healthy` sobre las réplicas, pero la imagen
de `proveedores-service` es `distroless/static` —sin shell ni cliente HTTP— y su
Dockerfile no se puede tocar en esta parte. La salida fue un contenedor de
inicialización (`sonda`, `busybox:1.37.0-musl`) que copia un busybox estático a
un volumen que las réplicas montan de solo lectura, y su healthcheck es
`/sonda/busybox wget .../readyz`. Funciona y deja los cinco contenedores
`healthy`, pero es un andamio: el plan siguiente ya toca el Dockerfile del
servicio para adoptar el logger compartido, y ahí conviene meterle un binario de
sonda propio —como el `cmd/sonda` del gateway— y borrar el servicio `sonda` y su
volumen.

**`cmd/sonda` en el gateway, que no estaba en el plan.** Misma razón: la imagen
del gateway es distroless y necesitaba poder correr su propio healthcheck. Son
40 líneas que preguntan por `/readyz` y salen 0 o 1.

**`RUTA_NO_ENCONTRADA` es un `code` nuevo.** Los Boundaries piden usar los `code`
del contrato existente, pero ninguno de los cuatro describe un 404 de una ruta
que el gateway no expone: `PRODUCTO_NO_ENCONTRADO` le mentiría al cliente. Queda
documentado en el README del gateway como propio del gateway. Hay que avisarle a
Integrante 3 para que lo incluya en el OpenAPI o acuerde otro nombre.

**Sin `services/api-gateway/.dockerignore`.** El contexto de build del gateway es
la raíz del repo, así que el `.dockerignore` que Docker lee es el de la raíz. Un
archivo en `services/api-gateway/` no se usaría nunca y daría a entender que sí.
Las reglas están en el `.dockerignore` de la raíz, que excluye
`services/proveedores-service`, el `go.work`, los `.env` y las herramientas de
agentes.

**Bug encontrado y arreglado en la verificación.** El `X-Request-ID` volvía
duplicado: el gateway lo pone en la respuesta antes de reenviar y
`ReverseProxy` suma los headers del upstream a los que ya están, y el servicio
devuelve el mismo header. Se resolvió con un `ModifyResponse` que lo borra de la
respuesta del upstream, y quedó un test que afirma que vuelve una sola vez.

**El reintento tiene su propio timeout.** Cada intento abre un contexto con
`TIMEOUT_UPSTREAM`, así que una request que falla la conexión en las dos réplicas
puede tardar hasta el doble antes de devolver el 502. En la práctica una conexión
rechazada falla al instante; se vio tardar ~2,5 s con los contenedores detenidos,
porque en la red de Docker los paquetes se descartan en lugar de rechazarse.

## Plan Change Log

- `go work sync` sacado de la verificación: reescribe archivos de
  `proveedores-service`, que los Boundaries prohíben tocar. Lo reemplaza
  `go build` + `go vet` + `go test` en modo workspace.
- `services/api-gateway/.dockerignore` no se creó: el contexto de build es la
  raíz y el archivo sería inerte.
- Se agregaron tres archivos que el plan no listaba, los tres para que los
  contenedores distroless puedan tener healthcheck:
  `services/api-gateway/cmd/sonda/main.go`, y en el compose el servicio `sonda`
  con su volumen.

## Review Triage Log

**Pass 1 (quick):** high 0 · medium 2 · low 5 · false 1 · maybe-false 0

| # | Hallazgo | Veredicto | Ruta | Evidencia / acción |
|---|---|---|---|---|
| 1 | `go.work.sum` sin el `h1:` de `golang.org/x/crypto v0.57.0` rompería el build con tag `integration` del servicio en modo workspace | low | patch | Mitad refutada: `go vet -tags=integration ./...` dentro del workspace sale 0, no falla. La otra mitad es real y se comprobó: el archivo **se reescribe** en el primer build con tag — pasó de 7 a 8 líneas y agregó el `h1:` de crypto. Deja el árbol sucio para quien corra los tests de integración. Se commitea la versión de 8 líneas ya completa. |
| 2 | `MarcarCaida` loguea con `context.Background()` y la línea de failover pierde `trace_id`/`request_id` | medium | patch | Real: `balanceador.go` `MarcarCaida` → `marcar(context.Background(), …)`, llamado desde `reenviar`, que tiene `r.Context()` con el span y el logger de la request a mano. Es justo la línea que explica por qué el tráfico se movió, y la demo incluye una caída provocada. Se le pasa el contexto que ya está en la mano. |
| 3 | Un path base en `UPSTREAMS_PROVEEDORES` se respeta al sondear y se descarta al proxear | low | rechazado | Real: `sondearUna` usa `URL.JoinPath`, `reescribir` copia solo scheme y host. Pero exige una URL de upstream con path, que nada configura (el compose usa `http://host:8080` pelado) y el arreglo agrega un guard o una rama. |
| 4 | `NuevoHandler` no valida su `Config` (logger o balanceador en nil → panic), a diferencia de `NuevoBalanceador` | low | rechazado | Real, pero los únicos que construyen el handler son `cmd/server/main.go` y los tests, y los dos pasan siempre las dos dependencias. El paquete es `internal`. El arreglo agrega guards. |
| 5 | El compose define seis servicios y el criterio pide cinco `healthy`; un grader vería un contenedor no sano | false | rechazado | Refutado contra el sistema corriendo: `docker compose ps` lista exactamente los cinco servicios de larga vida, todos `healthy`. `sonda` sale con 0 y solo aparece con `ps -a`, así que el criterio se cumple como está escrito. |
| 6 | `RUTA_NO_ENCONTRADA` viola el límite congelado "errores con los `code` del contrato existente" | medium | intent_gap | Real: el contrato tiene cuatro `code` y la implementación agrega un quinto. La matriz congelada pide "404 problem+json" para una ruta desconocida y ninguno de los cuatro la describe, así que la intención congelada quedó incompleta: hay más de una lectura posible (código nuevo, 404 sin `code`, u otro nombre). Solo el humano puede renegociar el bloque congelado. **No se revierte el código antes de que decida** (ver nota al usuario). |
| 7 | El README justifica `/healthz` con que evita que un upstream caído reinicie al gateway, pero el healthcheck del contenedor pega a `/readyz` | low | patch | Confirmado empíricamente: con las dos réplicas detenidas, `docker compose ps` muestra `api-gateway Up 17 minutes (unhealthy)`. Es exactamente lo que el README dice que `/healthz` previene, y se vería en el paso de caída provocada de la demo. Arreglo directo: corregir la frase o apuntar el healthcheck a `/healthz`. |
| 8 | Un POST a la ruta de capacidad devuelve 404 `RUTA_NO_ENCONTRADA` en vez de 405 | low | rechazado | Confirmado con `curl -X POST`: devuelve 404. Pero el contrato es solo GET, ningún cliente hace POST, y el arreglo agrega ramas. El mismo hallazgo se rechazó por la misma razón en la revisión del Integrante 1. |

## Design Notes

La correlación vive en el handler de logs, no en cada llamada: `slog.Handler.Handle` recibe el `context.Context`, así que un handler que envuelve al JSON puede leer el span y agregar los ids a toda línea emitida con `InfoContext`. Es lo que después deja al servicio correlacionado cambiando una línea.

```go
func (h *handlerTrazas) Handle(ctx context.Context, r slog.Record) error {
    if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
        r.AddAttrs(slog.String("trace_id", sc.TraceID().String()),
                   slog.String("span_id", sc.SpanID().String()))
    }
    return h.siguiente.Handle(ctx, r)
}
```

Jaeger `all-in-one:1.76.0` y no la línea 2.x: acepta OTLP en 4317/4318 sin archivo de configuración. `otlptracehttp` y no gRPC, para no arrastrar ese árbol de dependencias.

Dos servicios nombrados (`proveedores-service-1` y `-2`) con la misma `image`, no `deploy.replicas`: el gateway necesita nombres DNS distintos para repartir de verdad, porque un `http.Client` de Go se queda con la primera dirección que resuelve.

## Verification

**Resultado (corrido el 2026-10-22, Docker 27.4.0 / compose v2.31.0, Go 1.26.1):**

| Qué | Resultado |
|---|---|
| `go vet` + `go test -cover` en los dos módulos nuevos, sin Docker | pasa. `pkg/observabilidad` 71,9 %; `internal/gateway` 93,2 % |
| `proveedores-service` en modo workspace (`go build`, `go vet`, tests de dominio y HTTP) | pasa, con su `go.mod` y `go.sum` sin cambios |
| `gofmt -l` sobre lo nuevo | limpio |
| `docker compose build` desde cero (sin imágenes previas) | las dos imágenes se construyen |
| `docker compose up -d` tras `cp .env.example .env` | los cinco contenedores `healthy`, sin pasos manuales |
| `curl` con API key | 200, cuerpo del servicio intacto, un solo `X-Request-ID` |
| `X-Request-ID: abc123` entrante | se respeta, vuelve igual, viaja al upstream y aparece en los logs |
| sin `X-Request-ID` | 32 hex |
| sin API key | 401 `NO_AUTORIZADO` en problem+json, sin llamar al upstream |
| `GET /nope` | 404 `RUTA_NO_ENCONTRADA` en problem+json |
| producto inexistente | 404 `PRODUCTO_NO_ENCONTRADO` del servicio, tal cual |
| 4 requests, dos réplicas sanas | 2 y 2, medido en los logs de cada réplica y en el atributo `upstream` de los spans |
| `docker compose stop proveedores-service-1` | 200 por la 2 (4/4); `readyz` del gateway 200 |
| las dos detenidas | 502 `NO_DISPONIBLE`, log en ERROR con `request_id` y `trace_id`; `readyz` 503 y `healthz` 200 |
| réplicas de vuelta | el sondeo las recupera sin reiniciar el gateway |
| `docker compose stop jaeger` | 200 igual, y `WARN` del exportador cuando agota los reintentos |
| traza en Jaeger | servicio `api-gateway`, span padre `GET /api/v1/proveedores/recomendados` con `request_id` y `upstream`, más el span hijo de la llamada de salida |
| `request_id` → `trace_id` del log → traza en Jaeger | el mismo id de punta a punta |
| `git grep` de credenciales | solo en `.env.example`; `.env` ignorado |

Sin verificar: `go test -race` (el toolchain de cgo de esta máquina es de 32 bits)
y los tests de integración de `proveedores-service` con testcontainers, que son
de otro integrante.

**Commands:**
- `go vet ./...; go test ./... -cover` en los dos módulos nuevos -- expected: pasa sin Docker (no correr `go work sync`: ver las notas)
- `docker compose up -d --build; docker compose ps` -- expected: los cinco en `healthy`
- `curl -i -H "X-API-Key: $env:API_KEY" "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-450W-MONO&cantidad=10"` -- expected: 200 con `X-Request-ID`
- `docker compose logs api-gateway` -- expected: JSON con `service`, `request_id` y `trace_id`
- `curl -s "http://localhost:16686/api/traces?service=api-gateway"` -- expected: al menos una traza
- 4 requests y `docker compose logs` de las réplicas -- expected: 2 y 2
- `docker compose stop proveedores-service-1`, repetir -- expected: 200 por la 2; con las dos detenidas, 502 `NO_DISPONIBLE`
- `docker compose stop jaeger`, repetir -- expected: 200 igual
