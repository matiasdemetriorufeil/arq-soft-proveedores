# ADR-010 — Resiliencia (D10)

- **Estado:** aceptado e implementado para `api-gateway` y `proveedores-service` (timeouts, salida del reparto por salud, reintento acotado, error inmediato ante la base caída, telemetría opcional); **propuesto** para el proveedor externo (circuit breaker), la caché como respaldo y el límite de tráfico en el gateway
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D10. Comportamiento ante fallas y mecanismos seleccionados para limitar su propagación o degradar el sistema de manera controlada.

## Contexto

La consigna (2.9) pide identificar las dependencias de cada servicio, definir qué pasa cuando una falla o responde lento e implementar **al menos un mecanismo de protección**. La caída provocada se documenta en `docs/POSTMORTEM.md` para la defensa. La cátedra insistió en **aislar los problemas**: si algo se cae, el resto sigue funcionando.

El riesgo particular de nuestro sistema es que la capacidad la consume **otro grupo**: una falla nuestra se propaga a su sistema. Por eso no alcanza con no caernos: hay que **fallar rápido y con un error del contrato** (`problem+json` con `code`), para que ellos puedan degradar de su lado.

## Opciones evaluadas

1. **Solo timeouts.** Evitan que una request quede colgada, pero no evitan seguir mandando tráfico a algo que está caído.
2. **Timeouts, chequeo de salud y reintento acotado.** Saca del reparto lo que no responde y absorbe la caída de una réplica sin que el consumidor lo note.
3. **Lo anterior más un circuit breaker en cada llamada saliente.** Corta las llamadas a una dependencia que viene fallando y la vuelve a probar de a poco. Para la llamada gateway → réplica, el chequeo de salud ya cumple ese papel; donde de verdad aporta es frente a una dependencia que no controlamos.
4. **Bulkheads (pools separados por dependencia).** Útiles cuando un mismo servicio llama a varias dependencias lentas; hoy cada servicio tiene una sola.

## Qué elegimos

**La opción 2 hoy, y la 3 solo para el proveedor externo** cuando se integre.

### Dependencias y comportamiento ante su falla

| Servicio | Dependencia | Si falla | Si responde lento | Estado |
|---|---|---|---|---|
| `api-gateway` | una réplica de `proveedores-service` | la marca como caída en el acto y **reintenta 1 vez en la otra**; el sondeo de `/healthz` cada 5 s la devuelve al reparto | `504 NO_DISPONIBLE` a los 3 s, **sin** sacarla del reparto | implementado |
| `api-gateway` | las dos réplicas | `502 NO_DISPONIBLE`, y el `/readyz` del gateway pasa a 503 | `504 NO_DISPONIBLE` a los 3 s | implementado |
| `proveedores-service` | PostgreSQL | `503 NO_DISPONIBLE` inmediato y `/readyz` en 503; `/healthz` (lo que sondea el gateway) sigue en 200, así que la réplica sigue en el reparto y contesta el error del contrato | `503` a los **2 s** (timeout de consulta y de conexión) | implementado |
| `proveedores-service` | PostgreSQL al arrancar | reintenta las migraciones **15 veces cada 2 s** antes de rendirse | timeout de 10 s por intento | implementado |
| todos | Jaeger | **ningún efecto**: el exportador falla en segundo plano y las requests siguen; Jaeger no es dependencia en el compose | ningún efecto | implementado |
| `proveedores-service` | Redis (caché) | se saltea la caché y se lee de PostgreSQL | timeout corto (50 ms) y la misma salida | propuesto ([ADR-007](ADR-007-cache.md)) |
| `import-service` | catálogo de FAV (tienda de ropa) | circuit breaker; el ranking sigue con la última importación válida, hasta 24 h | timeout de 3 s y hasta 3 reintentos | propuesto ([ADR-009](ADR-009-consumo-proveedor.md)) |
| `stock-service` | pedidos de FAV | la orden de reposición queda en `BORRADOR`; los reintentos técnicos repiten su `Idempotency-Key` | timeout de 3 s; un aviso perdido lo recupera la consulta cada 15 min | propuesto ([ADR-009](ADR-009-consumo-proveedor.md)) |

Los timeouts y el reintento están detallados en [ADR-005](ADR-005-comunicacion.md), y la salida del reparto por salud, en [ADR-012](ADR-012-balanceo-de-carga.md).

### Otros mecanismos implementados

- **Separación entre `/healthz` (vivo) y `/readyz` (listo).** Si la base cae, el proceso sigue vivo y contesta `503`, porque el problema no es la réplica. `/readyz` en 503 deja el contenedor `unhealthy`, pero **en compose eso solo informa**: no reinicia el contenedor ni le saca tráfico, y `depends_on` lo mira solo al arrancar. Quien decide el reparto es el sondeo del gateway.
- **Error inmediato con el formato del contrato:** todas las fallas de infraestructura salen como `problem+json` con `code: NO_DISPONIBLE`, nunca como un cuelgue o una página de error.
- **Apagado ordenado:** ante `SIGTERM`, el servidor deja de aceptar conexiones y espera hasta 10 s a que terminen las requests en curso; después vacía las trazas pendientes.
- **Reinicio automático** si el proceso termina (`restart: unless-stopped`).

### Mecanismos propuestos

- **Circuit breaker** sobre las llamadas a FAV, la tienda de ropa: abre con 5 fallas seguidas, se queda abierto 30 s y deja pasar una prueba (medio abierto).
- **Respaldo desde la caché:** si PostgreSQL no responde pero hay ofertas en Redis, servirlas marcadas como posiblemente desactualizadas. Se decide en la Entrega 3, junto con la caché.
- **Límite de tráfico en el gateway**, por API key (token bucket) y de concurrencia total, para rechazar rápido lo que exceda en lugar de colapsar ([ADR-013](ADR-013-capacidad-y-costos.md)). La presentación pide "control de tráfico" en el punto de entrada.

## Por qué

- **Las réplicas existen para esto**: sin la salida del reparto, tener dos réplicas no protege nada, porque la mitad de las requests seguirían yendo a la caída.
- **A propósito, un timeout no saca la réplica del reparto**: si las dos están lentas por la base, sacarlas dejaría al sistema sin réplicas sanas y convertiría una lentitud en una caída total.
- **El circuit breaker vale donde no controlamos la dependencia.** Frente a nuestras réplicas tenemos señales mejores (el sondeo de salud); frente a FAV no, y una caída suya no puede llenar nuestro servicio de llamadas colgadas.
- **Que la telemetría sea opcional** evita el peor caso: que la herramienta para diagnosticar la falla sea la que la provoque.

## Consecuencias aceptadas

- **PostgreSQL es un punto único de falla.** Si cae, la capacidad entera responde `503`; ningún mecanismo de hoy lo evita. La mitigación propuesta es la caché como respaldo.
- **Con la base lenta, cada request espera 2 s** antes del `503`, y mientras tanto ocupa una conexión del pool.
- **Una réplica recién caída le cuesta un reintento** (latencia extra) a la primera request que le toca; si no llega ninguna request, el sondeo la detecta en hasta 5 s.
- **Un error de transporte aislado también saca a una réplica sana** del reparto hasta el próximo sondeo ([ADR-005](ADR-005-comunicacion.md)).
- **No hay límite de tráfico.** Por encima de su capacidad, el gateway puede colapsar (atender menos y devolver `504`) en lugar de rechazar rápido lo que sobra: pasó en una de las dos rampas del test de carga ([ADR-013](ADR-013-capacidad-y-costos.md)).
- **El apagado ordenado puede quedar cortado.** `docker compose stop` espera 10 s antes de matar el proceso, y el gateway puede necesitar hasta 10 s para terminar las requests más el tiempo de vaciar las trazas. Falta `stop_grace_period` en el compose.
- **Todavía no hay POSTMORTEM**: la caída controlada con su informe se hace antes de la defensa.

## Con qué lo medimos

Mediciones del 22/10 ([ADR-012](ADR-012-balanceo-de-carga.md), [ADR-002](ADR-002-arquitectura-interna.md)) con `docker compose up`:

- **Una réplica detenida:** 4 de 4 requests en `200`; detección sin tráfico en **4,8 s**; vuelta al reparto en **2,7 s**.
- **Las dos réplicas detenidas:** `502 NO_DISPONIBLE` y `/readyz` del gateway en 503.
- **Base detenida:** `503` en **2 ms**; base pausada: `503` en **2,0 s**.
- **Bajo sobrecarga:** códigos de respuesta y latencias máximas por tasa en [ADR-013](ADR-013-capacidad-y-costos.md).

## Qué nos haría cambiar de idea

- **Si con el pool del gateway ajustado la sobrecarga sigue terminando en `504`**, hace falta el límite de concurrencia en el gateway antes que más réplicas.
- **Si el grupo de paneles solares reintenta agresivamente ante nuestros `503`**, agregar `Retry-After` a las respuestas y el límite de tráfico antes de la presentación.
- **Si los errores de transporte aislados sacan réplicas sanas con frecuencia**, limitar el reintento y la marca de caída a los errores de conexión.
- **Si los sondeos cada 5 s resultan lentos** para lo que pide la demo, bajar el intervalo, sabiendo que suma tráfico de salud a las réplicas.
