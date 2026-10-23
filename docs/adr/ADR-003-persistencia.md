# ADR-003 — Persistencia (D3)

- **Estado:** aceptado e implementado para `proveedores-service` (PostgreSQL); **propuesto** para `stock-service` (PostgreSQL) e `import-service` (MongoDB), que todavía no existen
- **Fecha:** 2026-10-23
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
| `stock-service` | **PostgreSQL** | Escrituras transaccionales con bloqueo de fila. | propuesto |
| `import-service` | **MongoDB** | Escritura de documentos de forma variable; lectura por proveedor y fecha. | propuesto |

Esto cumple "una base relacional y otra no relacional" **solo cuando exista `import-service`**. Hasta entonces el sistema usa un único tipo de almacenamiento.

**Esquema implementado** (`services/proveedores-service/migrations/001_init.sql`):

- `proveedores (id, nombre UNIQUE, cuit UNIQUE)`
- `productos (sku PK, nombre, categoria, unidad)`
- `ofertas (proveedor_id, producto_sku, precio_unitario, descuento_pct, cantidad_minima_descuento, flete, stock_disponible, actualizado_en)`, clave primaria `(proveedor_id, producto_sku)`, con `CHECK` que impiden precios, fletes o stock negativos y descuentos fuera de 0–100.
- Índice `idx_ofertas_producto_sku` para la consulta principal, que filtra por SKU.

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

- **Un solo tipo de almacenamiento hoy.** El requisito de dos tipos depende de construir `import-service` o de otro servicio con MongoDB.
- **Postgres es un punto único de falla.** Si cae, la capacidad responde `503`; el balanceo entre réplicas no lo arregla.
- **El motor de ranking trae todas las ofertas del SKU y filtra en memoria** (ver [ADR-002](ADR-002-arquitectura-interna.md)). Es razonable con decenas de ofertas por producto; con miles habría que mover el filtro a SQL.
- **Importes como `NUMERIC(14,2)` en la base, pero `float64` en el dominio.** El redondeo final a 2 decimales absorbe el ruido, pero no es aritmética decimal exacta.
- **No hay escrituras en el flujo actual.** Los precios y el stock solo cambian por el seed; cuando `import-service` los actualice, habrá que revisar la consistencia entre lo que se importa y lo que se consulta.
- **El esquema de `productos` es provisorio** (ver la tensión descrita en [ADR-001](ADR-001-limites-de-los-servicios.md)).

## Con qué lo medimos

Mediciones del 23/10 sobre el código de `main` y datos de las entregas anteriores del equipo:

- **Datos de la carga inicial:** **10** proveedores, **30** productos y **170** ofertas (contadas en `002_seed.sql`).
- **Latencia de la consulta:** 1 a 3 ms por request contra PostgreSQL 16 (campo `duration_ms`, medición puntual del integrante 1, [ADR-002](ADR-002-arquitectura-interna.md)); mediana 1,99 ms y p95 4,92 ms medida en el gateway (n=23, [ADR-011](ADR-011-observabilidad.md)). **La prueba de carga de D13 reemplaza estas mediciones.**
- **Comportamiento con la base caída:** `503` en 2 ms; con la base pausada, `503` en 2,0 s (el timeout configurado).
- **Cobertura del adaptador de PostgreSQL:** 83 % con tests de integración contra un PostgreSQL 16 real levantado con testcontainers (plan de la Entrega 2).
- **Un solo paquete accede a la base:** `internal/adapters/postgres`.

## Qué nos haría cambiar de idea

- **Si D13 muestra que el límite del sistema es PostgreSQL** (conexiones saturadas, la consulta se degrada al crecer las ofertas), evaluar réplicas de lectura, mover el filtro de stock a SQL o servir el ranking desde la caché (D7) o el motor de búsqueda (D6).
- **Si el volumen de ofertas pasa de decenas a miles por SKU**, el filtrado en memoria deja de ser aceptable.
- **Si las listas de los proveedores resultan tener una forma estable**, `import-service` podría usar PostgreSQL y no MongoDB; pero entonces habría que cumplir el requisito de dos tipos de almacenamiento con otro servicio.
- **Si se exige exactitud contable**, pasar los importes a centavos enteros o a un tipo decimal.
