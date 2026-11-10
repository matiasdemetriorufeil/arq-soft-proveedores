# api-gateway

Entrada única al sistema. Todo lo que entra pasa por acá: autentica (API key o
JWT del frontend), aplica el límite de tráfico, resuelve el `X-Request-ID`,
abre el span padre de la traza, reparte la capacidad de proveedores entre las
réplicas de `proveedores-service`, publica `stock-service` y sirve el frontend.

No tiene base de datos ni lógica de dominio: si una respuesta tiene datos, los
puso el servicio.

## Rutas

| Ruta | Qué hace |
|---|---|
| `GET /api/v1/proveedores/recomendados` | Reenvía a `proveedores-service`. Exige `X-API-Key` o JWT. Path, query y cuerpo viajan tal cual. |
| `* /api/v1/stock/…` | Reenvía a `stock-service` (consultar stock, reservar, órdenes). Exige `X-API-Key` o JWT. Una sola instancia, sin balanceo ni reintento: la reserva es un `POST` y reintentarla con su `Idempotency-Key` es decisión del cliente. **`/webhooks/fav` no se publica**: FAV lo llama directo (ADR-009). Solo si está `STOCK_URL`. |
| `POST /api/v1/auth/login` | Cambia `{"usuario","contrasena"}` por `{"access_token","token_type":"Bearer","expires_in","usuario"}`. Limitado por IP. Solo si están `USUARIOS` y `JWT_SECRETO`. |
| `GET /` y `GET /web/…` | El frontend: HTML, CSS y JS embebidos en el binario (`internal/gateway/web/`). Sin build ni framework. |
| `GET /healthz` | 200 siempre que el proceso atienda. No mira a los upstreams: es el chequeo del proceso, no del sistema. |
| `GET /readyz` | 200 si queda alguna réplica sana; 503 si ninguna responde. Es el healthcheck del contenedor. |

Ojo con la consecuencia: la sonda del contenedor usa `/readyz`, así que con las
dos réplicas detenidas Docker reporta `api-gateway (unhealthy)` aunque el proceso
esté perfecto. Es a propósito —un gateway sin réplicas no puede atender la
capacidad— y no lo reinicia nadie: no hay `depends_on` sobre el gateway ni
política de reinicio por salud. Si en algún momento algo sí dependiera de su
salud, la sonda tiene que pasar a `/healthz`.

Cualquier otra ruta devuelve 404 en `application/problem+json`.

## Configuración

Solo por variables de entorno. No hay archivo de configuración ni valores en el
código: la misma imagen sirve para cualquier ambiente.

| Variable | Obligatoria | Default | Qué es |
|---|---|---|---|
| `PORT` | no | `8080` | Puerto en el que escucha. |
| `UPSTREAMS_PROVEEDORES` | **sí** | — | URLs base de las réplicas, separadas por coma: `http://proveedores-service-1:8080,http://proveedores-service-2:8080`. |
| `API_KEYS` | **sí** | — | Claves válidas separadas por coma. Las mismas que acepta el servicio, porque el header se reenvía. |
| `TIMEOUT_UPSTREAM` | no | `3s` | Cuánto espera a una réplica antes de devolver 504. |
| `INTERVALO_SALUD` | no | `5s` | Cada cuánto sondea `/healthz` de cada réplica. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | no | — | Colector OTLP/HTTP (`http://jaeger:4318`). Sin esta variable el gateway arranca igual, sin exportar trazas. |
| `STOCK_URL` | no | — | Base de `stock-service` (`http://stock-service:8080`). Sin ella, `/api/v1/stock/` devuelve 404. |
| `USUARIOS` | no | — | Usuarios del frontend, `usuario:contraseña` separados por coma. |
| `JWT_SECRETO` | no | — | Secreto HS256 de los tokens, 32 caracteres o más. Sin él o sin `USUARIOS`, no hay login. |
| `DURACION_JWT` | no | `1h` | Vigencia del token. |
| `CLAVE_INTERNA` | no | primera de `API_KEYS` | `X-API-Key` con la que el gateway llama a los servicios cuando la request entró con JWT. |
| `LIMITE_POR_SEGUNDO` / `LIMITE_RAFAGA` | no | `50` / `100` | Token bucket por identidad. `0` apaga el límite. |
| `LOGIN_POR_SEGUNDO` / `LOGIN_RAFAGA` | no | `0.2` / `5` | Token bucket del login, por IP. |
| `PUERTO_METRICAS` | no | `9091` | Puerto de `/metrics` (Prometheus). El compose no lo publica. |

Sin `UPSTREAMS_PROVEEDORES` o sin `API_KEYS` el proceso no arranca: es mejor que
quede claro en el arranque que descubrirlo con un 502 en la demo.

## Autenticación

Dos formas de entrar, y los servicios de atrás no se enteran de cuál se usó:

- **`X-API-Key`**, para otros sistemas (el grupo de paneles solares). Igual que
  en la Entrega 2: el header viaja tal cual y el servicio lo vuelve a validar.
- **`Authorization: Bearer <JWT>`**, para el frontend. El token es HS256, lo
  firma el gateway en el login y vale `DURACION_JWT`. Hecho con la biblioteca
  estándar (`jwt.go`): el header tiene que ser exactamente
  `{"alg":"HS256","typ":"JWT"}`, así que un `alg: none` no se acepta. Al
  reenviar, el gateway **saca el `Authorization` y pone `CLAVE_INTERNA`**: el
  token no sale del gateway y el usuario nunca ve una API key.

Si viene una API key inválida es 401 aunque venga también un token válido.

## Límite de tráfico

Token bucket en memoria (`limite.go`, [ADR-010](../../docs/adr/ADR-010-resiliencia.md)):

- **Por identidad**: cada API key (identificada por un prefijo de su SHA-256,
  nunca en claro) y cada usuario tienen su propio balde. Se aplica **después**
  de autenticar, así que una request sin credenciales no gasta el balde de
  nadie.
- **Por IP en el login**, para frenar la fuerza bruta.
- Lo que excede recibe **`429 LIMITE_EXCEDIDO`** con **`Retry-After`** en
  segundos, sin llegar al upstream, y suma a `gateway_limite_rechazos_total`.

## Métricas

`GET /metrics` en `PUERTO_METRICAS` (otro puerto, no publicado):

| Métrica | Tipo | Etiquetas |
|---|---|---|
| `gateway_http_requests_total` | counter | `ruta` (patrón del mux, nunca el path con el SKU), `metodo`, `status` |
| `gateway_http_request_duration_seconds` | histograma | `ruta`; incluye el corte de 0,3 s del objetivo de p95 |
| `gateway_limite_rechazos_total` | counter | — |
| `gateway_replicas_sanas` | gauge | — |

Prometheus, las alertas y el tablero de Grafana están en [`infra/`](../../infra) ([ADR-011](../../docs/adr/ADR-011-observabilidad.md)).

## Balanceo

Round-robin sobre las réplicas de `UPSTREAMS_PROVEEDORES`, salteando las caídas:

- **Sondeo.** Cada `INTERVALO_SALUD` pregunta por `/healthz` de cada réplica.
  Arranca dándolas por caídas y hace una primera vuelta antes de empezar a
  escuchar, así `/readyz` nunca dice que está listo sin haberlo comprobado.
- **Caída entre dos sondeos.** Si falla la conexión, la réplica queda marcada y
  el `GET` se reintenta **una sola vez** en otra sana. El `GET` de la capacidad
  no tiene efectos, así que repetirlo es seguro.
- **Recuperación.** El sondeo devuelve la réplica al reparto cuando vuelve a
  responder; no hace falta reiniciar el gateway.
- **Timeout.** Un `TIMEOUT_UPSTREAM` vencido devuelve 504 y **no** marca la
  réplica: está lenta, no caída, y sacarla del reparto sería castigar a la
  única que quizá queda.

## Errores

En `application/problem+json`, con la misma forma que los del servicio
(`type`, `title`, `status`, `detail`, `code`, `instance`, `request_id`).

| Status | `code` | Cuándo |
|---|---|---|
| 400 | `PARAMETRO_INVALIDO` | login con un cuerpo mal formado. |
| 401 | `NO_AUTORIZADO` | falta `X-API-Key` o JWT, o no son válidos. No se llama al upstream. |
| 401 | `CREDENCIALES_INVALIDAS` | login con usuario o contraseña incorrectos (mismo error para los dos casos). |
| 404 | `RUTA_NO_ENCONTRADA` | la ruta no la expone el gateway. |
| 429 | `LIMITE_EXCEDIDO` | se superó el límite de tráfico; trae `Retry-After`. |
| 502 | `NO_DISPONIBLE` | ninguna réplica responde, o `stock-service` no responde. |
| 504 | `NO_DISPONIBLE` | la réplica (o `stock-service`) no contestó dentro de `TIMEOUT_UPSTREAM`. |

Los errores que devuelve el servicio (`PARAMETRO_INVALIDO`,
`PRODUCTO_NO_ENCONTRADO`, su propio `NO_DISPONIBLE`) pasan tal cual: el gateway
no los reinterpreta.

`RUTA_NO_ENCONTRADA` es el único `code` que no está en el contrato de la
capacidad, porque el 404 de una ruta que el gateway no expone no es un producto
inexistente y no podía reusar `PRODUCTO_NO_ENCONTRADO`.

## Observabilidad

- **`X-Request-ID`.** Si entra uno válido —ASCII imprimible, hasta 128
  caracteres— se respeta; si no, se generan 32 hexadecimales. Se devuelve
  siempre, también en los errores, se reenvía al servicio y queda como atributo
  del span. Es con lo que el cliente encuentra su request.
- **Logs.** JSON por stdout, una línea por request, con `service`, `request_id`
  y `trace_id` (ver [`pkg/observabilidad`](../../pkg/observabilidad)).
- **Trazas.** Continúa el `traceparent` W3C que entre, o abre una traza nueva.
  El span padre lleva `request_id` y `upstream`, el host de la réplica que
  atendió: eso es lo que muestra el reparto en Jaeger. La llamada de salida va
  instrumentada con `otelhttp`, que inyecta el `traceparent` hacia el servicio.

## Correr

Con el resto del sistema, que es lo normal:

```powershell
docker compose up -d --build
```

Solo el binario, contra un `proveedores-service` que ya esté levantado:

```powershell
$env:UPSTREAMS_PROVEEDORES = "http://localhost:8081"
$env:API_KEYS = "clave-local"
go run ./cmd/server
```

## Tests

```powershell
go test ./... -cover
```

No necesitan Docker: las réplicas son `httptest.Server` y, sin endpoint OTLP,
las trazas son no-op. Hay un test por fila de la matriz de casos del plan;
lo de la Entrega 3 (JWT, login, límite, stock, métricas y frontend) está en
`entrega3_test.go`.

## Decisiones que afectan a los demás

Si vas a tocar el repo, estas cuatro te van a cruzar. Las de contrato están en
[`docs/contracts/README.md`](../../docs/contracts/README.md).

- **No corras `go work sync`.** Empuja la lista de build del workspace a los
  `go.mod` de los tres módulos y les mete requerimientos que ninguno necesita
  por su cuenta. No hace falta: cada módulo compila solo. Para verificar usá
  `go vet ./...` y `go test ./...` dentro de cada módulo.
- **Las dos imágenes se construyen desde la raíz del repo**, no desde el
  directorio de cada servicio: los dos módulos dependen de `pkg/observabilidad`
  por ruta relativa. El `.dockerignore` que manda es el de la raíz, y cada
  `Dockerfile` copia solo su módulo y el paquete compartido.
- **El healthcheck de cada contenedor es un binario de su propia imagen.** Las
  imágenes son distroless: no traen shell ni cliente HTTP. Los dos servicios
  tienen su `cmd/sonda`, que pregunta por `/readyz` y sale 0 o 1; en el compose
  el test es `["CMD", "/sonda"]`. Si agregás un servicio, copiale el patrón.
