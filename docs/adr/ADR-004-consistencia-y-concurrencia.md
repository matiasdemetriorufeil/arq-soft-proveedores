# ADR-004 — Consistencia y concurrencia en la reserva de stock (D4)

- **Estado:** aceptado e implementado para `stock-service` (bloqueo pesimista e idempotencia), medido contra PostgreSQL 16 real
- **Fecha:** 2026-11-06
- **Entrega:** 3 (11/11)
- **Decisión de la consigna:** D4. Garantías de consistencia de la operación crítica, cómo se maneja la concurrencia y qué anomalías se aceptan.

## Contexto

La operación crítica del sistema es **reservar unidades de stock** (HU-13). La regla que no se puede violar es RN-13: *el stock reservado nunca supera el disponible, aun con reservas simultáneas; ninguna reserva se pierde ni se duplica.*

Hay dos formas distintas de romperla y las dos tienen que resolverse:

1. **Dos reservas distintas compitiendo por la misma fila.** Con disponible 10, dos pedidos de 6 que leen "hay 10" y los dos descuentan dejan el disponible en −2: eso es sobreventa (CA-13.1).
2. **La misma reserva aplicada dos veces.** El cliente no recibe la respuesta —timeout del gateway, red cortada— y reintenta. Sin protección, el mismo pedido descuenta 6 unidades dos veces (CA-13.2).

Lo que condiciona la solución: `stock-service` corre con **su propia base PostgreSQL** ([ADR-003](ADR-003-persistencia.md)) y puede correr en **varias réplicas** ([ADR-012](ADR-012-balanceo-de-carga.md)), así que la garantía no puede vivir en el proceso. El servicio tiene un **timeout de 2 s** por operación: cualquier esquema que haga esperar más que eso termina en `503`.

## Opciones evaluadas

1. **Bloqueo pesimista con `SELECT … FOR UPDATE`.** La transacción bloquea la fila del SKU antes de decidir. El competidor queda esperando el commit y recién entonces lee el disponible ya descontado. Serializa solo la fila en disputa; el resto de los SKUs no se enteran. Cuesta: hay que acordarse de abrir la transacción, y una fila muy disputada se vuelve un cuello de botella.
2. **Bloqueo optimista por columna de versión.** Se lee, se calcula y se escribe con `WHERE version = $leída`; si no actualizó ninguna fila, alguien ganó y se reintenta. No bloquea nada, pero necesita lógica de reintento en la aplicación y, con contención alta, los reintentos se multiplican. Agrega una columna que no sirve para nada más.
3. **`SERIALIZABLE` con reintentos.** El motor detecta el conflicto y aborta una de las transacciones con `40001`. Es la garantía más fuerte y la más declarativa, pero obliga a envolver **toda** operación en un lazo de reintentos, y los abortos aparecen en cualquier consulta, no solo en la reserva. Demasiado para un servicio con una sola operación de escritura.
4. **`UPDATE … SET disponible = disponible - n WHERE sku = $1 AND disponible >= n` condicional.** Una sola sentencia atómica, sin transacción explícita: si devuelve 0 filas, no había stock. Es elegante y correcto para el descuento, pero la reserva son **dos** escrituras (la fila en `reservas` y el descuento en `stock_productos`) que tienen que ser atómicas entre sí, así que la transacción hace falta igual. Y el patrón en capas queda invisible: no habría regla en la capa de servicio, solo SQL.

Para la idempotencia (problema 2) se evaluó además guardar la respuesta en una caché por clave, y se descartó: la caché puede perder la entrada y el stock no se puede desreservar.

## Qué elegimos

**Bloqueo pesimista con `SELECT … FOR UPDATE` dentro de una transacción, más idempotencia por `Idempotency-Key` con un `UNIQUE` en la base.**

Dónde vive, con la arquitectura en capas de [ADR-002](ADR-002-arquitectura-interna.md):

| Qué | Dónde |
|---|---|
| Abre y cierra la transacción, y es dueño de la regla | `services/stock-service/internal/service/stock.go` (`Reservar`) |
| Ejecuta el `SELECT … FOR UPDATE` y las dos escrituras | `services/stock-service/internal/repository/repositorio.go` (`EnTransaccion`, `BloquearStock`, `InsertarReserva`, `AplicarReserva`) |
| `CHECK (disponible >= 0)` y `clave_idempotencia TEXT NOT NULL UNIQUE` | `services/stock-service/migrations/001_init.sql` |
| Traducción a `409 STOCK_INSUFICIENTE` | `services/stock-service/internal/handler/handler.go` |

La secuencia, dentro de un solo límite transaccional:

```go
return s.repo.EnTransaccion(ctx, func(tx repository.Tx) error {
    st, err := s.repo.BloquearStock(ctx, tx, sku) // SELECT … FOR UPDATE
    if err != nil { return err }
    if st.Disponible < cantidad { return &StockInsuficienteError{…} }
    if err := s.repo.InsertarReserva(ctx, tx, &nueva); err != nil { return err }
    return s.repo.AplicarReserva(ctx, tx, sku, cantidad)
})
```

Valores: **nivel de aislamiento el default de PostgreSQL (`READ COMMITTED`)** —el `FOR UPDATE` no necesita más—, **timeout de 2 s** por transacción (`repository.TimeoutConsulta`), y el `advisory lock` de las migraciones con ID propio (`727_002`).

**Idempotencia en dos pasos, a propósito.** Antes de abrir la transacción se lee `reservas` por clave: eso resuelve el reintento normal —el que llega después del commit del original— sin tomar el lock. La **garantía** es el `UNIQUE`: si dos reintentos llegan simultáneos y los dos pasan ese atajo, el segundo `INSERT` falla con `23505`, su transacción se deshace **entera** (el stock no se movió) y la capa de servicio responde releyendo la reserva que sí quedó. Sin el `UNIQUE`, dos reintentos simultáneos aplicarían el stock dos veces.

## Por qué

- **La garantía la da el motor, no el proceso.** Con dos réplicas de `stock-service` ([ADR-012](ADR-012-balanceo-de-carga.md)) un lock en memoria no sirve: las réplicas no se ven entre sí. La fila de `stock_productos` es el único punto en común.
- **El rechazo es correcto, no un azar ganado.** El `FOR UPDATE` hace que el segundo competidor lea el disponible **después** del commit del primero. Con una lectura optimista, el "lo vi en 10" sería una decisión tomada sobre datos ya viejos.
- **Contención realista baja.** Se serializan solo las reservas del **mismo SKU**. Dos reservas de SKUs distintos no se tocan, y el caso de uso (un operador de compras reservando material) no tiene miles de pedidos por segundo sobre la misma fila.
- **Una sola sentencia condicional no alcanza.** Las dos escrituras de la reserva tienen que caer o no caer juntas; la transacción no se evita.
- **Dos redes y no una.** Si mañana alguien escribe una ruta nueva que se olvida del `FOR UPDATE`, el `CHECK (disponible >= 0)` aborta la transacción. La invariante está escrita en la base, no solo en Go.
- **Hace reconocible el patrón en capas.** El límite transaccional en `service` y el bloqueo en `repository` son exactamente lo que [ADR-002](ADR-002-arquitectura-interna.md) propuso para el segundo servicio.

## Consecuencias aceptadas

- **Un SKU muy disputado se serializa.** Si alguna vez hubiera que reservar el mismo SKU cientos de veces por segundo, las reservas se encolarían y a los 2 s el servicio devolvería `503`. Hoy no es el caso.
- **El `409` es la respuesta normal bajo contención, no un error del cliente.** Con 20 reservas simultáneas de 6 sobre un disponible de 10, 19 reciben `409 STOCK_INSUFICIENTE`. El cliente tiene que saber reintentar con menos cantidad.
- **Caso de borde del reintento simultáneo:** el que pierde la carrera llega a su `INSERT` solo si antes pasó el chequeo de disponible, porque la validación va primero. Si el ganador dejó el disponible por debajo de la cantidad pedida, el que pierde recibe `409` en vez del `200` idempotente. Es transitorio —el reintento siguiente ya encuentra la reserva en el atajo de lectura— y el stock, que es lo que RN-13 protege, nunca se mueve dos veces.
- **La clave de idempotencia es responsabilidad del cliente.** Dos pedidos genuinamente distintos que manden la misma clave quedan como una sola reserva. El contrato dice que la clave identifica al pedido.
- **No hay cancelación ni expiración de reservas.** Una reserva confirmada se queda en `reservado` para siempre: no hay forma de devolver unidades al disponible. La columna `estado` ya admite `CANCELADA` para cuando se implemente.
- **PostgreSQL es punto único de falla.** Si la base no responde, la reserva devuelve `503` y ninguna réplica puede hacer nada al respecto ([ADR-003](ADR-003-persistencia.md)).
- **Las reservas no se publican como evento.** Nadie fuera del servicio sabe que el stock bajó; la reposición automática (HU-14) todavía no existe.

## Con qué lo medimos

**Medido el 06/11**, sin infraestructura (`go test ./... -cover` en `services/stock-service`):

- **37 casos** en verde (contando subtests): 12 en `internal/service` y 25 en `internal/handler`. Cobertura: `internal/service` **90,0 %** e `internal/handler` **89,3 %**, sin base de datos.
- **La capa de servicio no escribe SQL:** `internal/repository` es el único paquete que importa `jackc/pgx` (`go list -f '{{join .Imports "\n"}}' ./internal/service` no lo muestra; aparece solo de forma transitiva, porque `service` usa el tipo opaco `repository.Tx`).
- Los tests de la carrera de reintentos con un repositorio falso comprueban que el stock se descuenta **una sola vez** y que el camino feliz abre **exactamente una** transacción.

**Medido el 10/11 contra el sistema entero** (Game Day, entrando por el gateway y no por el servicio directo): 20 `POST /api/v1/stock/reservas` de 1 unidad **en paralelo** sobre un SKU con 5 disponibles devolvieron **5 × `201`** y **15 × `409`**; el `disponible` pasó de 5 a 0 y nunca a negativo. Repetir un `POST` con la misma `Idempotency-Key` devolvió la misma reserva, con el mismo `id`, sin descontar de nuevo. Es la misma garantía que prueba el test de concurrencia, pero con HTTP, gateway y pool de conexiones en medio ([`POSTMORTEM.md`](../POSTMORTEM.md#escenario-6--20-reservas-simultáneas-la-operación-crítica-d4)).

**Medido contra PostgreSQL 16 real** (`postgres:16-alpine` levantado con testcontainers; `TESTCONTAINERS_RYUK_DISABLED=true go test -tags=integration -count=1 ./internal/repository/ -v`, 06/11): **los 8 tests de integración en verde en 3,7 s**. Cobertura de `internal/repository` con ese tag: **62,2 %** (lo que falta es casi todo el tracer de spans, que se prueba aparte en `proveedores-service`).

- **`TestSinSobreventaConReservasSimultaneas` — la evidencia de CA-13.1.** Disponible 10 y **20 reservas de 6 lanzadas a la vez** desde goroutines que salen juntas tras una barrera. Resultado: **exactamente 1 aceptada**, **19 rechazadas** con `ErrStockInsuficiente`, `disponible` final **4**, `reservado` **6** y **una sola fila** en `reservas`. Duración: **0,11 s**. Repetido con `-count=5`: siempre el mismo resultado, sin un solo caso de sobreventa.
- **`TestReintentosSimultaneosAplicanElStockUnaVez` — CA-13.2.** Lo mismo con una **única** clave de idempotencia: las 20 goroutines devuelven **la misma** reserva, ninguna falla y el stock se mueve **una sola vez** (100 → 94). Duración: **0,09 s**.
- **`TestElCheckDeLaBaseImpideElDisponibleNegativo`:** salteando a propósito la validación de la capa de servicio, PostgreSQL aborta la transacción y el disponible queda intacto.

**Medido contra el servicio corriendo** (06/11). Método: el binario de `./cmd/server` compilado y corriendo en el host, con `DATABASE_URL` apuntando a un `postgres:16-alpine` en Docker (`docker run -p 5440:5432`) sobre la base `stock`, sin colector OTLP. Las latencias salen del campo `duration_ms` del log de requests; los tiempos de los `503` se midieron del lado del cliente con cronómetro. **No se midió con `docker compose`**: el `.env` de la máquina donde se escribió esto es anterior a las variables `STOCK_*` y el compose no interpola.

| Caso | Status | Latencia |
|---|---|---|
| Primera reserva (abre la conexión del pool) | `201` | 13,8 ms |
| Reintento de la misma `Idempotency-Key` | `200` | 1,6 ms |
| Consulta de stock por SKU | `200` | 2,4 ms |
| Stock insuficiente | `409` | 4,1 ms |
| SKU inexistente | `404` | 4,7 ms |
| Falta `Idempotency-Key` / `cantidad` no entera | `400` | < 0,1 ms (el log redondea a 0: no toca la base) |
| Falta `X-API-Key` | `401` | < 0,1 ms (idem: el middleware corta antes) |

Los dos modos de falla de la base se comportan distinto, y la diferencia importa para el Game Day:

| Falla | Reserva | `/readyz` | `/healthz` |
|---|---|---|---|
| **Base pausada** (`docker pause`) | `503` en **2,05 s** — el timeout configurado | `503` en 2,0 s | `200` |
| **Base detenida** (`docker stop`) | `503` en **11,9 ms** | `503` en 3,7 ms | `200` |

Detenida, el sistema operativo rechaza la conexión en el acto y el `503` es inmediato; pausada, el socket queda abierto sin contestar y hay que esperar el timeout. Es la misma distinción que [ADR-002](ADR-002-arquitectura-interna.md) ya registra para `proveedores-service`. `/healthz` sigue en `200` en los dos casos, que es lo correcto: el proceso está vivo, es la dependencia la que no está.

El reintento idempotente **no mueve el stock**: después de la reserva de 6 sobre `PAN-450W-MONO` y su reintento, `GET /api/v1/stock/PAN-450W-MONO` devuelve `disponible 114, reservado 6` (seed 120), y las dos respuestas traen el mismo `id` de reserva.

## Qué nos haría cambiar de idea

- **Si el test de carga (D13) muestra que las reservas del mismo SKU se encolan** y la latencia pasa del timeout de 2 s, pasar a bloqueo optimista con reintentos acotados, o partir el stock en varias filas por SKU (*sharding* de la fila) y reservar contra una.
- **Si aparece una segunda escritura** que tenga que ser consistente con la reserva (por ejemplo, descontar stock al confirmar una orden de reposición, HU-14) y el orden de los locks se vuelva difícil de razonar, evaluar `SERIALIZABLE` con reintentos, que mueve la detección al motor.
- **Si hay que reservar contra dos servicios a la vez** (stock propio y stock de un proveedor externo), la transacción de base no alcanza: haría falta una saga con compensación, y ahí la reserva tendría que poder cancelarse.
- **Si los `409` bajo contención resultan inaceptables para el cliente**, evaluar una cola de reservas con espera en vez del rechazo inmediato.
