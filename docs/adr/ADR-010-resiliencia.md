# ADR-010 — Resiliencia (D10)

- **Estado:** aceptado e implementado para `api-gateway` y `proveedores-service` (timeouts, salida del reparto por salud, reintento acotado, fail-fast ante la base caída, telemetría opcional); **propuesto** para el proveedor externo (circuit breaker), la caché como respaldo y el límite de tráfico en el gateway
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D10. Comportamiento ante fallas y mecanismos seleccionados para limitar su propagación o degradar el sistema de manera controlada.

## Contexto

La consigna (2.9) pide identificar las dependencias de cada servicio, definir qué pasa cuando una falla o responde lento e implementar **al menos un mecanismo de protección**. La caída provocada se documenta en `docs/POSTMORTEM.md` para la defensa. El profe insistió en **aislar los problemas**: si algo se cae, el resto sigue funcionando.

El riesgo particular de nuestro sistema es que la capacidad la consume **otro grupo**: una falla nuestra se propaga a su sistema. Por eso lo que importa no es solo no caernos, sino **fallar rápido y con un error del contrato** (`problem+json` con `code`), para que ellos puedan degradar de su lado.

## Opciones evaluadas

1. **Solo timeouts.** Evitan que un hilo quede colgado, pero no evitan seguir mandando tráfico a algo que está caído.
2. **Timeouts + chequeo de salud + reintento acotado.** Saca del reparto lo que no responde y absorbe la caída de una réplica sin que el consumidor lo note.
3. **Lo anterior + circuit breaker en cada llamada saliente.** Corta las llamadas a una dependencia que viene fallando y la vuelve a probar de a poco. Para la llamada gateway → réplica, el chequeo de salud ya cumple ese papel; donde de verdad aporta es frente a una dependencia que no controlamos.
4. **Bulkheads (pools separados por dependencia).** Útil cuando un mismo servicio llama a varias dependencias lentas; hoy cada servicio tiene una sola.

## Qué elegimos

**La opción 2 hoy, y la 3 solo para el proveedor externo** cuando se integre.

### Dependencias y comportamiento ante su falla

| Servicio | Dependencia | Si falla | Si responde lento | Estado |
|---|---|---|---|---|
| `api-gateway` | réplica de `proveedores-service` | la marca caída en el acto, **reintenta 1 vez en la otra**; el sondeo de `/healthz` cada 5 s la devuelve al reparto | `504 NO_DISPONIBLE` a los 3 s, **sin** sacarla del reparto | implementado |
| `api-gateway` | todas las réplicas | `502 NO_DISPONIBLE` y `/readyz` en 503 | ídem | implementado |
| `proveedores-service` | PostgreSQL | `503 NO_DISPONIBLE` inmediato y `/readyz` en 503; `/healthz` (lo que sondea el gateway) sigue en 200, así que la réplica no sale del reparto y contesta el error del contrato | `503` a los **2 s** (timeout de consulta y de conexión) | implementado |
| `proveedores-service` | PostgreSQL al arrancar | reintenta las migraciones **15 veces cada 2 s** antes de rendirse | timeout de 10 s por intento | implementado |
| todos | Jaeger | **ninguno**: el exportador falla en segundo plano y las requests siguen; Jaeger no es dependencia en el compose | ídem | implementado |
| `proveedores-service` | Redis (caché) | se saltea la caché y se lee de PostgreSQL | timeout corto (50 ms) y misma salida | propuesto ([ADR-007](ADR-007-cache.md)) |
| `import-service` | grupo de ropa | circuit breaker; el ranking sigue con la última lista conocida | timeout de 2 s | propuesto ([ADR-009](ADR-009-consumo-proveedor.md)) |

### Mecanismos implementados

- **Timeouts en cada salto** y decrecientes hacia adentro (3 s → 2 s), más timeouts del servidor HTTP (`ReadHeaderTimeout` 5 s) contra clientes lentos ([ADR-005](ADR-005-comunicacion.md)).
- **Salida del reparto por salud** con detección activa (sondeo) y pasiva (fallo de conexión) ([ADR-012](ADR-012-balanceo-de-carga.md)).
- **Reintento acotado a uno**, solo cuando la conexión falló, para no multiplicar la carga sobre algo que ya está lento.
- **Separación entre `/healthz` (vivo) y `/readyz` (listo)**: si la base cae, el proceso sigue vivo y atendiendo con `503` —el problema no es la réplica— pero deja de declararse listo, y eso es lo que mira el healthcheck del compose.
- **Fail-fast con error del contrato**: todas las fallas de infraestructura salen como `problem+json` con `code: NO_DISPONIBLE`, nunca como un cuelgue o un HTML de error.
- **Apagado ordenado**: ante `SIGTERM` el servidor deja de aceptar conexiones y termina las requests en curso (hasta 10 s).
- **Reinicio automático** de los contenedores (`restart: unless-stopped`).

### Mecanismos propuestos

- **Circuit breaker** sobre la llamada al grupo de ropa: abre con 5 fallas seguidas, se queda abierto 30 s y deja pasar una prueba (medio abierto).
- **Respaldo desde la caché**: si PostgreSQL no responde pero hay un ranking en Redis, servirlo marcado como posiblemente desactualizado. Se decide en la Entrega 3 junto con la caché.
- **Límite de tráfico por API key en el gateway** (token bucket), para que un consumidor no sature al resto. La presentación pide "control de tráfico" en el punto de entrada.

## Por qué

- **Las réplicas existen para esto**: sin la salida del reparto, tener dos réplicas no protege nada porque la mitad de las requests seguirían yendo a la caída.
- **Un timeout no saca la réplica del reparto a propósito**: si las dos están lentas por la base, sacarlas dejaría al sistema sin réplicas sanas y convertiría lentitud en caída total.
- **El circuit breaker vale donde no controlamos la dependencia.** Frente a nuestras réplicas tenemos señales mejores (el sondeo de salud); frente al grupo de ropa no, y una caída suya no puede llenar nuestras goroutines de llamadas colgadas.
- **Que la telemetría sea opcional** evita el peor caso: que la herramienta para diagnosticar la falla sea la que la provoque.

## Consecuencias aceptadas

- **PostgreSQL es un punto único de falla.** Si cae, la capacidad entera responde `503`; ningún mecanismo de hoy lo evita. La caché como respaldo es la mitigación propuesta.
- **Con la base lenta, cada request espera 2 s** antes del `503`; durante ese tiempo ocupa una conexión del pool.
- **La primera request a una réplica recién caída paga el reintento** (latencia extra) hasta que el sondeo la detecta, hasta 5 s.
- **No hay límite de tráfico** hoy: un consumidor con un bug puede saturar el sistema. El test de carga mostró que, por encima de ~1 600 req/s, el gateway colapsa (atiende menos y devuelve `504`) en lugar de rechazar rápido lo que sobra ([ADR-013](ADR-013-capacidad-y-costos.md)).
- **No hay POSTMORTEM todavía**: la caída controlada con informe se hace antes de la defensa.

## Con qué lo medimos

Mediciones del 22/10 ([ADR-012](ADR-012-balanceo-de-carga.md)) y del 23/10 con `docker compose up`:

- **Réplica detenida:** 4 de 4 requests en `200`; detección sin tráfico en **4,8 s**; vuelta al reparto en **2,7 s**.
- **Las dos réplicas detenidas:** `502 NO_DISPONIBLE` y `/readyz` del gateway en 503.
- **Base detenida:** `503` en **2 ms**; base pausada: `503` en **2,0 s** ([ADR-002](ADR-002-arquitectura-interna.md)).
- **Errores bajo carga** (k6, 23/10, [ADR-013](ADR-013-capacidad-y-costos.md)): **0 %** hasta 1 600 req/s; a 2 000 req/s, **1,81 %** de `504 NO_DISPONIBLE`. Ninguna falla salió como cuelgue ni como error fuera del contrato.

## Qué nos haría cambiar de idea

- **Si con el pool del gateway ajustado la sobrecarga sigue terminando en `504`**, hace falta un límite de concurrencia en el gateway (rechazar con `429` o `503` lo que exceda) antes que más réplicas.
- **Si el grupo de paneles solares reintenta agresivamente ante nuestros `503`**, agregar `Retry-After` en las respuestas y el límite de tráfico en el gateway antes de la presentación.
- **Si los sondeos cada 5 s resultan lentos** para lo que pide la demo, bajar el intervalo, sabiendo que suma tráfico de salud a las réplicas.
