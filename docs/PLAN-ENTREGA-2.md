# Plan de trabajo: Entrega 2 (viernes 23/10)

Reparto del trabajo entre los 4 integrantes para llegar a la Entrega 2 con lo obligatorio de la consigna (sección 6.3) y lo que faltó de la Entrega 1.

- **Alcance:** mínimo obligatorio.
- **Dominio:** comparador de proveedores. El grupo de paneles solares consume nuestra capacidad y nosotros consumimos el catálogo de ropa.
- **Fuentes:** [consigna oficial](consigna-practico-integrador-2026.md) y [complemento](TP-Integrador-Complemento%20de%20Consigna.md). Si algo no coincide, manda la consigna.

## Estado

| Integrante | Parte | Estado |
|---|---|---|
| 1 | Backend del dominio | ✅ Terminado: `proveedores-service` + tests unitarios + tests de integración + ADR D2 |
| 2 | Plataforma y observabilidad | ✅ Terminado: gateway con balanceo, paquete de observabilidad, traza end-to-end en Jaeger, compose + ADR D11 y D12 (en `main`) |
| 3 | Contrato y documentación base | ✅ Terminado: OpenAPI v1, README, SPEC, ARCHITECTURE + ADR D1, D3 y D8 (PR de `docs/base`) |
| 4 | Decisiones, mediciones y entrega | ✅ Plantilla ADR, D5, D6, D7, D9, D10, D13, test de carga con k6 y guion de la demo. D9 diseñado sobre el contrato de FAV v1.1.0. Falta: el tag `entrega-2` |

Cada uno actualiza su fila cuando termina.

## Antes de arrancar

- **La Entrega 1 no se hizo.** Este plan cubre también README, SPEC, ARCHITECTURE con diagramas, el contrato y los ADR D1, D3, D5 y D8. Hay que avisarles hoy a los profes.
- **El revisor automático compara los ADR con el código.** Si un ADR describe algo que todavía no está implementado (caché, búsqueda, balanceo), poné `Estado: Propuesto` y aclará qué falta.
- **Go con poca experiencia:** biblioteca estándar y pocas dependencias (`net/http`, `log/slog`, `pgx`, OpenTelemetry). Nada de frameworks grandes.

## Qué tiene que estar el viernes

**Código (obligatorio, consigna 6.3)**
- [x] Al menos un servicio operativo (`proveedores-service`)
- [x] Capacidad compartida funcionando, ya sin mock
- [x] Un almacenamiento integrado al flujo (PostgreSQL)
- [x] Logs estructurados **y correlacionados** con `trace_id`
- [x] Primera traza distribuida del flujo principal (gateway → servicio → Postgres)
- [x] Un solo comando (`docker compose up`) levanta todo, sin secretos en el repo

**Documentación (obligatorio)**
- [x] ADR nuevos: D2, D6, D7, D9, D10, D12 y D13
- [x] D1 y D3 validados; D5 publicado por primera vez (no se entregó en la 1); D11 en su primera versión; D8
- [x] `README.md` con manual de uso, `SPEC.md` y `docs/ARCHITECTURE.md` con diagramas
- [x] Contrato OpenAPI v1 en `docs/contracts/`
- [x] Anunciar bonus o dejar escrito que no hacemos ninguno (no tomamos ninguno: README y ARCHITECTURE)
- [ ] Tag `entrega-2` y demo de 5 minutos ensayada

## Lo que ya está decidido (no se toca hasta el viernes)

### Capacidad compartida (ya implementada)

```
GET /api/v1/proveedores/recomendados?producto={sku}&cantidad={n}&limite={k}
Header: X-API-Key: <clave>
```

- `producto` es obligatorio. `cantidad` vale 1 por defecto. `limite` vale 5 por defecto y como máximo 50.
- **200**: array JSON ordenado por `costo_total` ascendente. Cada elemento trae:
  `proveedor_id`, `proveedor_nombre`, `precio_unitario`, `descuento_pct`, `descuento_aplicado`, `flete`, `stock_disponible`, `costo_total`.
  Si el producto existe pero no hay ofertas con stock suficiente, devuelve `[]`.
- **Errores** en `application/problem+json`, con el campo `code`:

  | Status | `code` | Cuándo |
  |---|---|---|
  | 400 | `PARAMETRO_INVALIDO` | falta `producto`, o `cantidad`/`limite` no son enteros válidos |
  | 401 | `NO_AUTORIZADO` | falta `X-API-Key` o no es válida |
  | 404 | `PRODUCTO_NO_ENCONTRADO` | el SKU no existe |
  | 503 | `NO_DISPONIBLE` | la base no responde (timeout de 2 s) |

- Es un GET sin efectos, así que se puede reintentar.
- Ejemplos completos en [`services/proveedores-service/README.md`](../services/proveedores-service/README.md).

### Regla de negocio

`costo_total = precio_unitario × cantidad × (1 − descuento_pct/100) + flete`

- El descuento aplica solo si `cantidad ≥ cantidad_minima_descuento`.
- Se excluyen las ofertas con `stock_disponible < cantidad`.

### Otras decisiones

- **Datos:** PostgreSQL con las tablas `proveedores`, `productos` y `ofertas`. El seed trae 10 proveedores y 30 productos, y se aplica solo al arrancar el servicio.
- **Arquitectura interna:** hexagonal en `proveedores-service` (ver [ADR-002](adr/ADR-002-arquitectura-interna.md)).
- **Observabilidad:** `log/slog` en JSON + OpenTelemetry + Jaeger. El gateway genera `X-Request-ID` si no viene.
- **Ramas:** `main` siempre levanta. Cada uno trabaja en su rama, abre un PR que revisa otro integrante y se hace squash merge.

## Las cuatro partes

### Integrante 1: Backend del dominio ✅

Rama: `feat/proveedores-service` (+ `test/proveedores-integracion`)

- [x] Dominio en Go: `costo_total`, ranking, descuento por volumen y exclusión por stock
- [x] Adaptador PostgreSQL con `pgx`, migraciones y seed embebidos
- [x] Handler HTTP del endpoint, con API key, errores problem+json, `/healthz` y `/readyz`
- [x] Tests unitarios (dominio 100 %, HTTP 95 %) y tests de integración contra Postgres real (83 %)
- [x] Dockerfile
- [x] ADR D2
- [ ] **Viernes:** reemplazar el logger por el paquete compartido de Integrante 2 (para tener `trace_id`) y probar la traza completa

### Integrante 2: Plataforma y observabilidad

Rama: `feat/plataforma`. **Tu parte destraba la traza y el `docker compose up`.**

Código:
- [ ] Estructura del repo, `docker-compose.yml` con postgres, jaeger, api-gateway y proveedores-service, healthchecks, `.env.example` y `.gitignore`
- [ ] `api-gateway` en Go: reverse proxy, API key, `X-Request-ID` y spans
- [ ] Paquete compartido: logger JSON con `trace_id` e inicialización de OpenTelemetry
- [ ] Verificar la traza gateway → servicio → postgres en Jaeger y guardar una captura

Documentación:
- [ ] ADR D11 v1 (observabilidad)
- [ ] ADR D12 (balanceo: 2 réplicas de `proveedores-service` detrás del gateway, con health checks)

Lo que tenés que saber de `proveedores-service`:
- Variables de entorno: `DATABASE_URL`, `API_KEYS` (separadas por coma) y `PORT` (8080 por defecto). Si falta `DATABASE_URL` o `API_KEYS`, no arranca.
- Usá `/healthz` para saber si está vivo y `/readyz` para saber si la base responde.
- Las migraciones corren solas al arrancar: el contenedor de Postgres no necesita scripts de inicio.
- Ya propaga y devuelve `X-Request-ID`.
- Si en tu máquina el puerto 5432 está ocupado, no publiques Postgres en ese puerto del host.

### Integrante 3: Contrato y documentación base

Rama: `docs/base`. **El contrato va primero:** lo necesitan el grupo de paneles solares y la demo.

Primero:
- [ ] `docs/contracts/openapi.yaml` v1, **escrito a partir de lo que ya devuelve el servicio** (ver "Capacidad compartida" arriba)
- [ ] `docs/contracts/README.md`: ejemplos `curl`, errores, autenticación, idempotencia y versionado

Después:
- [ ] `README.md` raíz: dominio, flujo principal, cómo correrlo y **manual de uso**
- [ ] `SPEC.md`: historias de usuario, reglas de negocio y criterios de aceptación
- [ ] `docs/ARCHITECTURE.md`: diagramas de contexto y de contenedores en Mermaid, servicios y propiedad de los datos
- [ ] ADR D1 (límites de los servicios), D3 (persistencia) y D8 (contrato propio)

### Integrante 4: Decisiones, mediciones y entrega

Rama: `docs/adrs`

Hoy:
- [x] `docs/adr/TEMPLATE.md` con la plantilla (contexto, opciones evaluadas, decisión, por qué, consecuencias aceptadas, cómo lo medimos, qué nos haría cambiar de idea). Usá [ADR-002](adr/ADR-002-arquitectura-interna.md) como ejemplo.
- [x] Conseguir el contrato del grupo de ropa (FAV v1.1.0, en `docs/contracts/consumidos/`)
- [ ] Escribirles a los profes (ver abajo)
- [x] ADR D5 (comunicación), D6 (búsqueda), D7 (caché), D9 (consumo de ropa) y D10 (resiliencia)

Viernes:
- [x] Test de carga corto con k6 contra el endpoint: RPS y p95 reales para el ADR D13 (capacidad y costos)
- [x] Guion de la demo y checklist final ([`DEMO-ENTREGA-2.md`](DEMO-ENTREGA-2.md))
- [ ] Tag `entrega-2` (después de mergear los PRs y ensayar)

## Cronograma

| Cuándo | Integrante 1 | Integrante 2 | Integrante 3 | Integrante 4 |
|---|---|---|---|---|
| **Jue · inicio** | Reunión de 30 min entre los cuatro: leer este plan, crear las ramas y confirmar las decisiones. | | | |
| **Jue → 21:00** | ✅ Dominio y tests | Esqueleto, compose, paquete de logs y trazas → `main` | OpenAPI v1 y README del contrato → `main` | Plantilla ADR, D5 y D10, mensajes a profes y grupo de ropa |
| **Jue → 23:30** | ✅ Postgres, HTTP y tests de integración | api-gateway con API key y trazas | README, SPEC y ARCHITECTURE | D6, D7 y D9 |
| **Jue 23:30** | Checkpoint: todo lo que funcione va a `main` y `docker compose up` tiene que levantar en al menos dos computadoras. | | | |
| **Vie · mañana** | Logger compartido, traza y ayuda con ADR | Traza en Jaeger, D11 y D12 | D1, D3, D8 y revisión de que los ADR coincidan con el código | k6 y D13, guion de la demo |
| **Vie · −2 h** | Congelamiento: no entra nada más. Tag `entrega-2`, ensayo de la demo desde un clon limpio. | | | |

## Demo de 5 minutos

1. `git clone` y `git checkout entrega-2`
2. `docker compose up` y mostrar que todos los contenedores quedan sanos
3. Consultar `/api/v1/proveedores/recomendados` por el gateway con la API key y explicar el ranking
4. Mostrar un error del contrato (404 con un producto inexistente)
5. Seguir el mismo `trace_id` desde los logs JSON hasta Jaeger
6. Recorrer rápido `docs/` y los ADR

## Pendiente con gente de afuera (hoy)

**A los profes**
- Avisar que la Entrega 1 quedó incompleta y que la cubrimos en esta entrega.
- Confirmar el dominio: comparador de proveedores con stock y reposición.
- ¿Desde cuándo tiene que estar accesible la capacidad para el grupo de paneles solares: en la Entrega 2 o recién en la nube el 6/11?

**A los grupos de la integración**
- **Grupo de ropa (FAV):** ✅ contrato recibido (v1.1.0). Falta pedirles la clave del grupo y el secreto de avisos, su URL desplegada cuando la tengan y el link a su repo, para registrar las dudas del contrato como issues.
- **Grupo de paneles solares:** pasarles el OpenAPI v1 cuando esté en `main`.

## Queda para después del 23/10

Los otros servicios (stock, importación y catálogo), MongoDB, RabbitMQ, el motor de búsqueda, Redis, el frontend y el consumo real del grupo de ropa. Los ADR de esos temas se entregan ahora como decisiones propuestas.
