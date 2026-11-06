# ADR-003 — Persistencia (D3)

- **Estado:** aceptado e implementado para `proveedores-service` (PostgreSQL), `import-service` (MongoDB) y `stock-service` (PostgreSQL, base `stock` propia)
- **Fecha:** 2026-10-23; actualizado el 2026-11-06 con `stock-service`
- **Entrega:** 1 (no se presentó en su fecha) y validación en la 2 (23/10)
- **Decisión de la consigna:** D3. Tipo de almacenamiento elegido para cada servicio, patrones de acceso que debe soportar y limitaciones aceptadas.

## Contexto

La consigna (2.8) exige **al menos dos tipos de almacenamiento diferentes**, uno relacional y otro no relacional, elegidos según el patrón de acceso, el volumen, la necesidad de consulta y las garantías de consistencia. El profe además pidió expresamente una base relacional y una no relacional.

Para la Entrega 2 alcanza con **un** almacenamiento integrado al flujo (6.3); el segundo se exige en la presentación del 11 o 13/11. El motor de búsqueda (D6) y la caché (D7) son decisiones separadas y no se tratan acá.

## Opciones evaluadas

Para `proveedores-service`:

1. **PostgreSQL.** Datos con relaciones claras (proveedor ↔ oferta ↔ producto), consulta por SKU con `JOIN`, restricciones de integridad.
2. **MongoDB.** Un documento por producto con sus ofertas incrustadas. La lectura del ranking sería una sola búsqueda, pero actualizar el precio o el stock de un proveedor reescribe el documento de cada producto que ofrece, y las restricciones las tiene que hacer la aplicación.
3. **Archivos o memoria con los datos cargados al arrancar.** Sin dependencias, pero no cumple "almacenamiento integrado" ni sirve cuando hay varias réplicas que tienen que ver los mismos datos.

Para los servicios propuestos (resumen; se confirma al construirlos):

- `stock-service`: PostgreSQL, porque la reserva sin sobreventa necesita transacciones y bloqueo de filas (`SELECT … FOR UPDATE`).
- `import-service`: MongoDB, porque cada proveedor trae su propio formato de lista y el esquema es variable.

## Qué elegimos

**PostgreSQL 16 para `proveedores-service`**, con acceso por `pgx` v5.

| Servicio | Almacenamiento | Patrón de acceso | Estado |
|---|---|---|---|
| `proveedores-service` | **PostgreSQL** | Lectura por SKU en **una sola consulta**: `LEFT JOIN` desde `productos` hacia `ofertas` y `proveedores`, que distingue "el SKU no existe" (0 filas) de "existe pero sin ofertas" (1 fila con NULL). Sin escrituras en el flujo actual. | implementado |
| `stock-service` | **PostgreSQL**, base `stock` con usuario propio en el mismo contenedor | Escrituras transaccionales con bloqueo de fila: `SELECT … FOR UPDATE` sobre la fila del SKU, más el `INSERT` de la reserva, en una sola transacción ([ADR-004](ADR-004-consistencia-y-concurrencia.md)). Lectura por clave primaria (`sku`) y por `clave_idempotencia`, las dos indexadas. | implementado (06/11) |
| `import-service` | **MongoDB 8** | Un documento por intento de importación con la **respuesta cruda del proveedor tal cual llegó**, el catálogo traducido vigente y el evento a publicar. Lecturas: la última vigente por secuencia, la última buena por fecha y los eventos sin publicar. | implementado (03/11) |

Con `import-service` el sistema cumple **"una base relacional y otra no relacional"** en el flujo: el catálogo de FAV pasa por MongoDB antes de llegar como ofertas a PostgreSQL.

**MongoDB en `import-service`** (`internal/adapters/mongo`):

- Colección `importaciones`, un documento por intento (bueno, sin cambios, con error o vencido): así se mide la tasa de éxito de las sincronizaciones.
- `crudo` guarda la respuesta del proveedor como documento, **con los campos que traiga aunque no los usemos**. Si FAV agrega uno, queda registrado sin migrar nada; un segundo proveedor con otro formato entra en la misma colección. Ese es el patrón de acceso que justifica una base documental.
- **Outbox en el mismo documento:** el evento `ListaPreciosActualizada` se guarda dentro de la importación. Una escritura de un documento es atómica en MongoDB, así que no hace falta una transacción multi-documento (que pediría un *replica set*).
- Índices: secuencia única por proveedor (frena dos importaciones con la misma secuencia), por estado y fecha, y uno parcial con los eventos sin publicar.
- Timeout de 3 s por operación. Si MongoDB no responde, `/readyz` de `import-service` pasa a `503`; la capacidad publicada no se entera, porque no depende de él.

**Esquema implementado** (`services/proveedores-service/migrations/001_init.sql`):

- `proveedores (id, nombre UNIQUE, cuit UNIQUE)`
- `productos (sku PK, nombre, categoria, unidad)`
- `ofertas (proveedor_id, producto_sku, precio_unitario, descuento_pct, cantidad_minima_descuento, flete, stock_disponible, actualizado_en)`, clave primaria `(proveedor_id, producto_sku)`, con `CHECK` que impiden precios, fletes o stock negativos y descuentos fuera de 0–100.
- Índice `idx_ofertas_producto_sku` para la consulta principal, que filtra por SKU.

**Esquema implementado de `stock-service`** (`services/stock-service/migrations/001_init.sql`):

- `stock_productos (sku PK, disponible, reservado, minimo, actualizado_en)`, con `CHECK (disponible >= 0)` y `CHECK (reservado >= 0)`. Ese primer `CHECK` es la invariante de RN-13 escrita en la base: aunque falle la lógica de la aplicación, la sobreventa no entra.
- `reservas (id UUID PK, sku FK → stock_productos, cantidad CHECK > 0, clave_idempotencia UNIQUE, estado, creado_en)`. El `UNIQUE` es lo que hace idempotente a la reserva ante dos reintentos simultáneos.
- Índice `idx_reservas_sku` para auditar el movimiento de una fila de stock.
- **Base separada, usuario separado.** `stock-service` se conecta con `STOCK_DB_USER`, que es OWNER de `stock` y **no puede conectarse a la base `proveedores`** (`infra/postgres/init/10-bases-por-servicio.sh` le quita el `CONNECT` a `PUBLIC`). Un servicio no puede leer los datos del otro ni por accidente: la integración es por API o por evento.

**Decisiones de operación:**

- **Las migraciones están embebidas en el binario y corren al arrancar**, idempotentes (`IF NOT EXISTS`, `ON CONFLICT DO NOTHING`): el contenedor de Postgres no necesita scripts de inicio y `docker compose up` basta.
- **El seed es de demostración**: 10 proveedores, 30 productos y 170 ofertas, determinista.
- **Timeout de 2 s** en las consultas: si la base no responde, el servicio contesta `503 NO_DISPONIBLE` en lugar de colgarse.
- **Las dos réplicas comparten una sola base.** Cada réplica abre su propio pool de conexiones.

## Por qué

- **El ranking es relacional por naturaleza.** Une tres entidades con claves foráneas y filtra por una clave indexada; es lo que Postgres hace mejor.
- **La integridad la garantiza la base**, no solo la aplicación: un precio negativo o un descuento de 150 % no pueden entrar aunque falle una validación.
- **Con dos réplicas del servicio, la base es el punto en común.** Cualquier réplica atiende cualquier request porque ninguna guarda estado propio (ver [ADR-012](ADR-012-balanceo-de-carga.md)).
- **Las migraciones embebidas** hacen que "un solo comando" sea cierto y que la demo no dependa de scripts que solo existen en una máquina.

## Consecuencias aceptadas

- **Dos motores que operar.** MongoDB suma un contenedor, un volumen y sus credenciales; corre en un solo nodo, sin réplicas.
- **Postgres es un punto único de falla.** Si cae, la capacidad responde `503`; el balanceo entre réplicas no lo arregla.
- **El motor de ranking trae todas las ofertas del SKU y filtra en memoria** (ver [ADR-002](ADR-002-arquitectura-interna.md)). Es razonable con decenas de ofertas por producto; con miles habría que mover el filtro a SQL.
- **Importes como `NUMERIC(14,2)` en la base, pero `float64` en el dominio.** El redondeo final a 2 decimales absorbe el ruido, pero no es aritmética decimal exacta.
- **Las ofertas de FAV llegan con retraso.** Entre que cambian en FAV y que se ven en el ranking pasan hasta 5 minutos (la sincronización) más lo que tarde el consumidor del evento; hasta 24 h si FAV está caído ([ADR-009](ADR-009-consumo-proveedor.md)).
- **`productos` queda en `proveedores-service`** ([ADR-014](ADR-014-tres-servicios-sin-catalogo.md)); los de proveedores externos entran por el evento.

## Con qué lo medimos

Mediciones del 23/10 sobre el código de `main` y datos de las entregas anteriores del equipo:

- **Datos de la carga inicial:** **10** proveedores, **30** productos y **170** ofertas (contadas en `002_seed.sql`).
- **Latencia de la consulta:** 1 a 3 ms por request contra PostgreSQL 16 (campo `duration_ms`, medición puntual del integrante 1, [ADR-002](ADR-002-arquitectura-interna.md)); mediana 1,99 ms y p95 4,92 ms medida en el gateway (n=23, [ADR-011](ADR-011-observabilidad.md)). **La prueba de carga de D13 reemplaza estas mediciones.**
- **Comportamiento con la base caída:** `503` en 2 ms; con la base pausada, `503` en 2,0 s (el timeout configurado).
- **Cobertura del adaptador de PostgreSQL:** 83 % con tests de integración contra un PostgreSQL 16 real levantado con testcontainers (plan de la Entrega 2).
- **Un solo paquete accede a la base:** `internal/adapters/postgres`.

Mediciones del 06/11 sobre `stock-service`:

- **Un solo paquete accede a la base:** `internal/repository`; es el único que importa `jackc/pgx`.
- **Datos de la carga inicial:** **30** SKUs en `stock_productos`, los mismos del seed de `proveedores-service` (contados en `002_seed.sql`).
- **Tests de integración contra PostgreSQL 16 real** (testcontainers, `go test -tags=integration ./internal/repository/`): **8 en verde en 3,7 s**, con 62,2 % de cobertura del paquete. Cubren migraciones y seed (idempotentes: se reaplican enteros), lectura por SKU, `Ping`, el camino completo de la reserva, el `UNIQUE` de la clave de idempotencia, el `CHECK (disponible >= 0)` y los dos tests de concurrencia ([ADR-004](ADR-004-consistencia-y-concurrencia.md)). También aceptan `STOCK_TEST_DATABASE_URL` para correr contra un PostgreSQL ya levantado, sin Docker.
- **Comportamiento con la base caída:** detenida, la reserva devuelve `503 NO_DISPONIBLE` en **11,9 ms** (la conexión se rechaza en el acto); pausada, en **2,05 s**, que es el timeout configurado. `/readyz` acompaña en los dos casos y `/healthz` sigue en `200`. Medido con el binario corriendo en el host contra un `postgres:16-alpine` en Docker ([ADR-004](ADR-004-consistencia-y-concurrencia.md)).

Mediciones del 03/11 sobre `import-service`:

- **Un solo paquete accede a MongoDB:** `internal/adapters/mongo`.
- **Tests de integración contra MongoDB 8 real** (testcontainers, `go test -tags=integration ./internal/adapters/mongo/`): guardar y leer la vigente con su evento, outbox en orden, índice de secuencia única y ping. **Escritos y compilados; no se pudieron correr en el entorno donde se escribieron**, que no tiene Docker: hay que correrlos en una máquina del grupo antes del 06/11.
- **La respuesta cruda conserva campos desconocidos:** el test guarda un catálogo con un campo que no está en el contrato de FAV (`campoNuevoDeFAV`).

## Qué nos haría cambiar de idea

- **Si D13 muestra que el límite del sistema es PostgreSQL** (conexiones saturadas, la consulta se degrada al crecer las ofertas), evaluar réplicas de lectura, mover el filtro de stock a SQL o servir el ranking desde la caché (D7) o el motor de búsqueda (D6).
- **Si el volumen de ofertas pasa de decenas a miles por SKU**, el filtrado en memoria deja de ser aceptable.
- **Si las listas de los proveedores resultan tener una forma estable**, `import-service` podría usar PostgreSQL y no MongoDB; pero entonces habría que cumplir el requisito de dos tipos de almacenamiento con otro servicio.
- **Si se exige exactitud contable**, pasar los importes a centavos enteros o a un tipo decimal.
