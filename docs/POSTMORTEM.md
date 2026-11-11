# Game Day y postmortem de las caídas (10/11/2026)

Informe del Game Day que pide la consigna para la defensa individual: se tiró abajo
cada dependencia, propia y ajena, y se midió qué hizo el sistema. No es un
documento de diseño: todo lo que está acá se observó corriendo.

- **Cuándo:** noche del 10/11/2026.
- **Dónde:** `docker compose up` completo en una máquina (14 contenedores, todos
  *healthy*), con las imágenes de la rama `chore/cierre-entrega-3`.
- **Evidencia cruda:** [`tests/carga/resultados/2026-11-10-gameday/`](../tests/carga/resultados/2026-11-10-gameday/),
  un archivo por escenario, con los comandos y las respuestas tal como salieron.
- **Cómo se leen los tiempos:** `curl -w %{time_total}` de punta a punta, entrando
  por el gateway como cualquier consumidor.

## Resumen

| # | Qué se tiró abajo | Qué hizo el sistema | ¿Aguantó? |
|---|---|---|---|
| 1 | Redis (caché) | Sigue respondiendo correcto desde PostgreSQL; la latencia pasa de 6,5 ms a 56,6 ms | ✅ con un costo medido |
| 2 | Una réplica de `proveedores-service` | 20 de 20 llamadas en `200`; el gateway la saca del reparto | ✅ |
| 3 | PostgreSQL | `503 NO_DISPONIBLE` en 2,0 s, constante; no se cuelga | ✅ pero más lento de lo documentado |
| 4 | FAV (proveedor externo) | Ranking y búsqueda siguen con la última importación válida; el breaker abre en la 3ª vuelta; se recupera solo | ✅ |
| 5 | RabbitMQ | El cambio queda retenido en el outbox y se publica cuando el broker vuelve; nada se pierde | ✅ |
| 6 | 20 reservas simultáneas del mismo SKU | 5 `201` y 15 `409`; el disponible llega a 0 y nunca a negativo | ✅ |
| 7 | 400 llamadas concurrentes con una clave | 178 `200` y 222 `429` con `Retry-After: 1` | ✅ |

**Ninguna caída dejó el sistema colgado y ninguna dio una respuesta incorrecta.**
Los tres problemas que sí aparecieron están abajo, en *Qué hay que arreglar*.

## Escenario 1 · Redis caído

`docker compose stop redis`, con la caché ya caliente.

| | Latencia de la capacidad |
|---|---|
| Con Redis | 6,5 ms (5 llamadas: 6,6 / 6,5 / 6,7 / 5,8 / 4,9) |
| Sin Redis | 56,6 ms (5 llamadas: 58,6 / 56,9 / 56,6 / 56,6 / 56,7) |

Las 10 llamadas devolvieron `200` con el mismo cuerpo correcto: la lectura cae a
PostgreSQL, como dice [ADR-007](adr/ADR-007-cache.md).

**Lo que se aprendió:** el salto de 50 ms no es el costo de ir a PostgreSQL —es el
**timeout de 50 ms del decorador**, que se paga completo en *cada* request mientras
Redis está caído. La desviación es de 0,8 ms entre llamadas: es un timeout, no una
consulta. PostgreSQL responde en los ~6 ms restantes.

Esto **no es un hallazgo nuevo**: [ADR-007](adr/ADR-007-cache.md) ya lo tenía escrito
como consecuencia aceptada, con "unos 56 ms" medidos el 06/11 y la aclaración de que
"no hay un corte que deje de intentar durante un rato". El valor del ejercicio es que
el número se reprodujo igual en otra corrida y en otra máquina (56,6 ms contra
56 ms), así que la consecuencia es estable y no un artefacto de aquella medición.

## Escenario 2 · Una réplica caída

`docker compose stop proveedores-service-2`, y 20 llamadas seguidas por el gateway:
**20 de 20 en `200`**, cero errores. El `readyz` del gateway siguió en `200`.
[ADR-012](adr/ADR-012-balanceo-de-carga.md) se comporta como dice.

## Escenario 3 · PostgreSQL caído

`docker compose stop postgres`. Es la dependencia sin la cual no hay respuesta
posible, así que acá lo que importa es **cómo** falla.

- 5 de 5 llamadas: `503` con `code: NO_DISPONIBLE` y `request_id`, en **2,0 s**
  (2,010 / 2,007 / 2,007 / 2,006 / 2,007).
- La reserva de stock con su propia base caída: también `503` en 2,006 s.
- Nunca se colgó, nunca devolvió un `504` del gateway, nunca dio datos viejos como
  si fueran buenos.

**Lo que se aprendió:** los 2 s coinciden con lo que ya predecía la tabla de
[ADR-010](adr/ADR-010-resiliencia.md) para el caso "responde lento" (timeout de
conexión de pgx), así que la medición **confirma** el ADR. Lo que estaba mal era la
palabra con la que el mismo ADR resumía el caso "si falla": decía *inmediato*, y no
lo es. Importa porque el timeout del gateway es de 3 s: el margen real entre el `503`
claro y un `504` sin diagnóstico es de **1 segundo**. Ya quedó corregido en el ADR.

## Escenario 4 · FAV caído (el proveedor externo)

`POST http://localhost:8090/_mock/modo {"modo":"caido"}` deja a FAV devolviendo
`503` a todo.

**a) La capacidad no se entera.** 5 de 5 llamadas al ranking de un producto de FAV en
`200`, en 5 ms, con el precio de la última importación válida. La búsqueda siguió
devolviendo los 3 productos de ropa. Es exactamente lo que promete
[ADR-009](adr/ADR-009-consumo-proveedor.md): el catálogo importado sobrevive a la
caída de quien lo origina.

**b) El breaker abre donde dice.** Forzando el intervalo de sincronización a 10 s
para no esperar 15 minutos, los logs muestran la secuencia completa:

```
vuelta 1 → error 503, breaker "cerrado"
vuelta 2 → error 503, breaker "cerrado"
vuelta 3 → error 503, breaker "abierto"     ← abre en la tercera, como está configurado
vuelta 4 → "circuit breaker abierto: no se llama a la dependencia"
vuelta 5 → "circuit breaker abierto: no se llama a la dependencia"
```

Desde la cuarta vuelta ya **no sale tráfico hacia FAV**: deja de castigar a un
servicio que está caído.

**c) Se recupera solo.** Al volver a `modo: normal`, sin tocar nada más, las
sincronizaciones siguientes terminan en `estado: sin_cambios` y `error: ""`.

## Escenario 5 · RabbitMQ caído, con un cambio esperando

El más interesante, porque prueba el outbox de punta a punta. Con el broker caído se
cambió un precio en FAV (15.000 → 19.999):

```
broker caído → estado "con_cambios", modificadas=1, publicados=0, pendientes=1
broker caído → pendientes=1   (el cambio sigue guardado, no se reintenta a ciegas)
broker vuelve → pendientes=0  (se publicó lo retenido)
```

Y después, consultando el ranking por el gateway: **`precio_unitario: 19999`**. La
cadena completa —FAV → `import-service` → outbox → RabbitMQ → consumidor idempotente
→ PostgreSQL → invalidación de la caché → ranking— se destrabó sola, sin perder el
evento. [ADR-005](adr/ADR-005-comunicacion.md) y
[ADR-010](adr/ADR-010-resiliencia.md), verificados.

## Escenario 6 · 20 reservas simultáneas (la operación crítica, D4)

No es una caída, pero es lo que más puede romper: 20 `POST /api/v1/stock/reservas` de
1 unidad **en paralelo**, con claves de idempotencia distintas, sobre un SKU con 5
disponibles.

| | |
|---|---|
| Antes | `disponible=5`, `reservado=3` |
| Respuestas | **5 × `201`** y **15 × `409`** |
| Después | `disponible=0`, `reservado=8` |

Se entregaron exactamente las 5 unidades que había. El disponible nunca fue negativo
y nunca se vendió lo que no estaba. Es el `SELECT … FOR UPDATE` dentro de la
transacción de [ADR-004](adr/ADR-004-consistencia-y-concurrencia.md), medido contra
el sistema entero y no solo en el test de integración.

Aparte: repetir el mismo `POST` con la **misma** `Idempotency-Key` devolvió la misma
reserva (`id` idéntico) sin descontar stock de nuevo.

## Escenario 7 · Límite de tráfico

400 llamadas concurrentes (100 hilos) con la misma clave: **178 × `200`** y
**222 × `429`** con `Retry-After: 1`. Coincide con el token bucket configurado
(ráfaga 100 y 50/s de recarga): entran la ráfaga más lo que se recarga durante la
corrida, y el resto se rechaza con el código del contrato 1.1.0.

## Problemas de entorno que aparecieron en el camino

Tres cosas que no eran del diseño sino de la operación, y que bloqueaban levantar el
sistema. Las tres quedaron arregladas:

1. **El `.env` estaba 31 variables atrás de `.env.example`**, así que
   `docker compose config` abortaba en la interpolación y no levantaba **ningún**
   servicio. Pasa en cada máquina que no actualizó su `.env` desde la Entrega 2, y
   no se puede arreglar desde el repo porque `.env` está en `.gitignore`.
   **Si alguien no puede levantar el sistema, es esto.** Se copian las variables
   nuevas de `.env.example` y se corre `docker compose down -v` una vez, porque el
   script que crea la base `stock` solo corre con el volumen de PostgreSQL vacío.
2. **MongoDB 8.0 no arranca sobre un kernel 6.19 o más nuevo** (SERVER-121912: el
   TCMalloc que trae viola el ABI de rseq). Por eso el job de integración de
   `import-service` venía fallando en `main`. Se subió a **9.0** en el compose y en
   el test de integración, que declara como invariante usar la misma imagen.
3. **El puerto de la UI de RabbitMQ puede estar ocupado** por otro stack de Docker
   en la misma máquina, y entonces el contenedor no arranca. Se cambia
   `PUERTO_RABBITMQ_UI` en el `.env`, que existe justamente para eso.

## Qué hay que arreglar

En orden de lo que más duele, con lo medido como fundamento:

1. **La caché no tiene breaker.** Con Redis caído, cada lectura paga los 50 ms del
   timeout (escenario 1). Un breaker que abra tras N timeouts y deje de consultar
   Redis por unos segundos devolvería la latencia a los ~6 ms de PostgreSQL. Es la
   misma pieza que ya existe en `import-service/internal/resiliencia`, hoy no
   reutilizable porque vive en el `internal` de otro módulo: moverla a `pkg/` la
   habilita para la caché y para los pedidos a FAV, que tampoco tienen breaker.
   [ADR-007](adr/ADR-007-cache.md) lo acepta como consecuencia; el Game Day no
   cambia la decisión, la confirma con un segundo dato.
2. **Decidir qué se hace con el margen de 1 segundo** entre el error de la base (2 s)
   y el timeout del gateway (3 s), escenario 3. Hoy alcanza, pero es poco: o se baja
   el timeout de conexión de pgx, o se sube el del gateway. Mientras no se toque,
   queda escrito en [ADR-010](adr/ADR-010-resiliencia.md) junto con la corrección de
   la palabra "inmediato", que no describía lo que hace el código.
3. **El `readyz` del gateway devuelve `200` con PostgreSQL caído.** Es defendible
   —el gateway no tiene base y su trabajo es rutear— pero significa que un
   orquestador no lo sacaría de rotación aunque no pueda servir nada útil. Hoy lo
   cubre la alerta por tasa de errores; conviene decidirlo a propósito y escribirlo.

## Lo que no se probó

Para no dar por verificado lo que no se miró:

- **Meilisearch caído.** El flujo de búsqueda del frontend depende de él; el ranking
  y la reserva no.
- **Jaeger caído.** Solo afecta la telemetría, no las respuestas.
- **El webhook de FAV con firma inválida.** Está cubierto por tests unitarios
  (`internal/fav/aviso_test.go`), no por un ejercicio en vivo.
- **Un pedido a FAV con FAV caído.** Las dos órdenes de reposición de productos de
  ropa ya habían pasado a `SOLICITADA` antes del ejercicio, y la regla de una sola
  orden abierta por producto impide rearmar el caso sin tocar la base a mano.
- **Las tres alertas disparando.** La de "sin réplicas sanas" ya se verificó aparte
  (dispara en 48 s); las de p95 y tasa de errores no se ejercitaron acá.
- **Nada en la nube.** El Game Day se corrió el 10/11 contra el stack local; la VM
  se creó el 11/11 ([`NUBE.md`](NUBE.md)). Los escenarios son reproducibles ahí por
  SSH, con los mismos comandos.

## Cómo repetirlo

Los comandos de cada escenario están en los archivos de
[`tests/carga/resultados/2026-11-10-gameday/`](../tests/carga/resultados/2026-11-10-gameday/).
Con el sistema levantado, el ciclo de cada uno es siempre el mismo:

```bash
docker compose stop <servicio>     # o: curl -X POST localhost:8090/_mock/modo -d '{"modo":"caido"}'
# ... medir con curl entrando por el gateway (localhost:8080) ...
docker compose start <servicio>    # o: {"modo":"normal"}
```
