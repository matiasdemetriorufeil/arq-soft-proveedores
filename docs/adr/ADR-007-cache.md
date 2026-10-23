# ADR-007 — Caché (D7)

- **Estado:** **propuesto**. No hay Redis en el compose ni código de caché; hoy cada request de la capacidad consulta PostgreSQL. La medición comparativa antes y después corresponde a la Entrega 3 (6/11).
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D7. Información almacenada temporalmente, invalidación, vigencia, comportamiento ante fallas y criterios de medición.

## Contexto

La consigna (2.7) pide caché en un flujo de lectura relevante, **justificada por una necesidad observable** y con métricas que muestren su impacto. El profe pidió **no inventar una caché**: usar una estándar (Redis).

El flujo de lectura más relevante es la capacidad publicada, `GET /api/v1/proveedores/recomendados`: es lo que consume el grupo de paneles solares y lo que más tráfico va a recibir. Cada request hace una consulta con dos `JOIN` a PostgreSQL, que es además un punto único de falla compartido por las dos réplicas ([ADR-003](ADR-003-persistencia.md)).

**Lo que dice la medición hoy, sin maquillaje:** el test de carga del 23/10 ([ADR-013](ADR-013-capacidad-y-costos.md)) muestra que con el seed actual (30 productos, 170 ofertas) **PostgreSQL no es el límite**: la mediana de punta a punta es de ~2 ms hasta 1 400 req/s y lo que se satura es la CPU, sobre todo la del gateway. Con estos datos, una caché casi no movería la capacidad.

La necesidad que sí es observable es otra: **PostgreSQL es la única dependencia que comparten las dos réplicas** y la que más CPU usa después del gateway (hasta ~1,8 núcleos bajo carga). La caché reduce las consultas a la base y prepara el sistema para cuando crezcan los datos importados. Si en la Entrega 3 la medición antes y después no muestra un impacto, lo vamos a decir.

## Opciones evaluadas

1. **Caché en memoria de cada réplica** (un `map` con TTL). Sin dependencias, pero cada réplica tiene su copia, la invalidación tiene que llegar a todas y el profe pidió no hacer una caché casera.
2. **Redis, guardando la respuesta final** (clave = SKU + cantidad + límite). Máximo ahorro por acierto, pero la cantidad varía en cada pedido y la tasa de aciertos sería baja; invalidar un SKU obliga a borrar muchas claves.
3. **Redis, guardando las ofertas del SKU** (clave = SKU). El ranking (filtro por stock, descuento por volumen, orden) se sigue calculando en memoria, que cuesta microsegundos. Una clave por producto, fácil de invalidar.
4. **Caché HTTP** (`Cache-Control`) para que la guarde el consumidor o un proxy. Gratis para nosotros, pero no controlamos al consumidor ni podemos invalidarla.

## Qué elegimos

**La opción 3: Redis con las ofertas de cada SKU, en `proveedores-service`.**

- **Dónde va en el código:** un decorador del puerto `OfertasRepo` ([ADR-002](ADR-002-arquitectura-interna.md)) en `internal/adapters/redis`, que envuelve al repositorio de PostgreSQL. Ni el dominio ni el handler cambian.
- **Qué se guarda:** `ofertas:v1:{sku}` → JSON con la lista de ofertas tal como las devuelve el repositorio. Un SKU inexistente se guarda como marca de "no existe" con vigencia corta, para que un SKU mal escrito repetido no vaya siempre a la base.
- **Vigencia (TTL):** **60 s** para las ofertas; **10 s** para la marca de "no existe".
- **Invalidación:** explícita al recibir `ListaPreciosActualizada` ([ADR-005](ADR-005-comunicacion.md)): se borran las claves de los SKU afectados. El TTL queda como red de seguridad si se pierde un evento.
- **Comportamiento ante fallas:** la caché es **opcional**. Si Redis no responde en **50 ms** o devuelve error, se lee de PostgreSQL y se loguea; nunca se devuelve un error al consumidor por culpa de Redis. Una escritura fallida en Redis se ignora.
- **Estampida:** si muchas requests piden el mismo SKU vencido a la vez, solo una va a la base (`singleflight` por réplica).

## Por qué

- **Saca a PostgreSQL del camino de la mayoría de las requests**: es el recurso compartido por las dos réplicas y el que pasa a ser el límite cuando crezcan los datos ([ADR-003](ADR-003-persistencia.md)).
- **Guardar las ofertas y no la respuesta** mantiene la tasa de aciertos alta con cualquier `cantidad` y `limite`, y una sola clave por SKU hace que invalidar sea trivial.
- **Como decorador del puerto**, se puede apagar con una variable de entorno y medir antes y después con el mismo test de carga, que es lo que pide la consigna.
- **Fallar abierto** (ir a la base si Redis no está) evita que la caché se convierta en un punto de falla nuevo ([ADR-010](ADR-010-resiliencia.md)).

## Consecuencias aceptadas

- **El stock puede estar desactualizado hasta 60 s** si se pierde la invalidación: el ranking podría recomendar un proveedor que ya no tiene stock suficiente. Es aceptable porque la capacidad **recomienda**, no reserva; la reserva sin sobreventa la hace `stock-service` contra su base.
- **Un contenedor más** (Redis) y una dependencia más que operar.
- **La primera request de cada SKU después de una invalidación** paga la consulta a la base más la escritura en Redis.
- **No resuelve la caída de PostgreSQL** salvo que se decida servir ofertas vencidas como respaldo, que queda abierto en [ADR-010](ADR-010-resiliencia.md).

## Con qué lo medimos

Se mide en la Entrega 3 con el mismo script de k6 de [ADR-013](ADR-013-capacidad-y-costos.md), con la caché apagada y prendida:

- **RPS máximo sostenido** con p95 < 300 ms y errores < 1 %, antes y después.
- **p95 de latencia** de la capacidad, antes y después.
- **Consultas por segundo a PostgreSQL** (spans `SELECT` en Jaeger o `pg_stat_statements`), antes y después.
- **Tasa de aciertos** (`cache_hit` / total), con objetivo > 80 % con la distribución de SKU del test.

Línea de base, sin caché: los números de [ADR-013](ADR-013-capacidad-y-costos.md).

## Qué nos haría cambiar de idea

- **Si en la Entrega 3 el límite sigue siendo la CPU del gateway** (como el 23/10), la caché no va a mover el RPS máximo: la mediríamos por la reducción de consultas a la base y lo diríamos así, sin presentarla como mejora de capacidad.
- **Si la tasa de aciertos queda por debajo del 50 %**, la distribución de SKU consultados es demasiado dispersa: revisar el TTL o guardar en la caché solo los SKU más pedidos.
- **Si el grupo de paneles solares necesita stock exacto**, bajar el TTL o sacar `stock_disponible` de lo cacheado y consultarlo aparte.
