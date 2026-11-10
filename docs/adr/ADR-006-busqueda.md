# ADR-006 — Búsqueda (D6)

- **Estado:** **aceptado**, con la medición de abajo. Implementado en [`internal/adapters/meilisearch`](../../services/proveedores-service/internal/adapters/meilisearch/), [`internal/indexacion`](../../services/proveedores-service/internal/indexacion/) y [`cmd/reindexar`](../../services/proveedores-service/cmd/reindexar/).
- **Fecha:** 2026-10-23 · actualizado 2026-11-06
- **Entrega:** 2 (23/10); implementación y medición en la 3 (06/11)
- **Decisión de la consigna:** D6. Información indexada, criterios de consulta, actualización o reconstrucción del índice y retraso aceptable.

## Contexto

La consigna (2.6) pide que una entidad del dominio tenga búsqueda **con un motor de búsqueda** (un `LIKE` en SQL no cuenta), con paginación, filtros relevantes para el dominio, al menos un criterio de orden, un índice sincronizado con los cambios y un **retraso máximo tolerable** definido. La cátedra pidió que esas demoras sean cortas.

La capacidad que publicamos exige conocer el SKU (`PAN-550W-MONO`). Un comprador real no empieza por el SKU: empieza por "panel 550 W", "inversor híbrido" o "baterías con stock en al menos tres proveedores". La búsqueda es el paso previo natural a pedir el ranking, y es lo que va a usar el frontend.

## Opciones evaluadas

1. **Full-text de PostgreSQL** (`tsvector` + índice GIN). Sin componentes nuevos y siempre consistente, pero el complemento pide un motor de búsqueda real, y la tolerancia a errores de tipeo ("bateria litio", "invesor") requiere extensiones extra (`pg_trgm`).
2. **Elasticsearch.** El estándar de la industria, con agregaciones potentes. Necesita una JVM con 1 a 2 GB de memoria solo para arrancar: en nuestras notebooks y en una VM chica de la nube es caro para 30 productos.
3. **Meilisearch.** Un binario con tolerancia a errores de tipeo de fábrica, filtros, orden, paginación y facetas; corre con unos cientos de MB y se habla con él por HTTP. Menos potente en agregaciones y sin clúster en la versión libre.

## Qué elegimos

**Meilisearch**, con un índice de **productos** alimentado por `proveedores-service`, que es el dueño de `productos` y `ofertas` hoy ([ADR-001](ADR-001-limites-de-los-servicios.md)).

**Qué se indexa:** un documento por producto, con lo que se busca y lo que se filtra ya calculado. Así queda `PAN-550W-MONO` con los datos del seed (`002_seed.sql`): `precio_min` es el menor precio unitario entre las ofertas **con stock**, por eso es el del proveedor 3 y no el de 189 497,29 del proveedor 1, que tiene stock 0. `proveedores` y `proveedores_con_stock` también cuentan solo las ofertas con stock. El documento guardado lleva además un `id` derivado del SKU que no se devuelve en la búsqueda: el motor solo acepta letras, números, `-` y `_` como id, y un SKU de un proveedor externo no tiene por qué cumplirlo.

```json
{
  "sku": "PAN-550W-MONO",
  "nombre": "Panel solar 550 W monocristalino",
  "categoria": "paneles",
  "unidad": "unidad",
  "precio_min": 189573.99,
  "proveedores_con_stock": 9,
  "stock_total": 1667,
  "proveedores": [
    "Bornes y Cables SA", "Cordoba Energia SA", "Distribuidora del Sol SRL",
    "Electro Mayorista Norte SA", "Fotovoltaica Pampeana SRL", "Grupo Litio SA",
    "Helios Importaciones SRL", "Ingenieria Renovable SA", "Juarez Materiales Electricos SRL"
  ],
  "actualizado_en": "2026-10-23T14:02:11Z"
}
```

- Campos buscables: `nombre`, `sku`, `proveedores`.
- Filtros: `categoria`, `proveedores`, `precio_min` por rango y `proveedores_con_stock > 0` (solo con stock).
- Orden: `precio_min` ascendente o descendente, y relevancia por defecto. La regla `sort` va primera en el índice para que pedir un orden por precio ordene de verdad también con texto; los productos sin stock quedan al final.
- Paginación: `pagina` y `tamanio` (máximo 50, igual que el `limite` de la capacidad).

**Consulta** (publicada en el gateway, no en el contrato v1 de la capacidad). Los valores de los filtros se escapan: un parámetro no puede alterar la expresión de filtro. Sin SDK: cliente HTTP propio con la biblioteca estándar.

```
GET /api/v1/productos/buscar?q=panel 550&categoria=paneles&con_stock=true&orden=precio_asc&pagina=1&tamanio=20
```

La búsqueda **no reemplaza al ranking**: devuelve productos con su precio mínimo orientativo; el ranking con descuento por volumen y flete sigue saliendo de `GET /api/v1/proveedores/recomendados`, que lee la fuente de verdad.

**Actualización del índice:** por evento. Cuando cambian las ofertas de un proveedor (`ListaPreciosActualizada`, [ADR-005](ADR-005-comunicacion.md)), el consumidor de `proveedores-service` registra los SKU afectados en `skus_por_reindexar` **dentro de la transacción del evento** y un ciclo los drena: recalcula los documentos y los envía a Meilisearch en lote. El consumidor lo despierta después del commit, y además mira cada 2 s. Si Meilisearch está caído, los SKU esperan en la tabla. Cada fila tiene una versión: si el SKU cambia mientras se indexa, sigue pendiente. La operación es idempotente: reindexar el mismo SKU dos veces deja el mismo documento.

**Reconstrucción:** un comando `reindexar` que lee todo PostgreSQL, carga un índice nuevo (`productos_tmp`) y lo intercambia con el vigente (`swap-indexes`), así la búsqueda nunca queda vacía. El servicio hace lo mismo al arrancar si el índice no existe o está vacío, y se corre a mano si se sospecha desincronización. Un candado de PostgreSQL deja que una sola réplica por vez escriba el índice: lo que cambia durante una reconstrucción queda pendiente y se indexa después del intercambio.

**Retraso máximo tolerable:** **5 segundos** entre que se confirma el cambio de una oferta en PostgreSQL y su aparición en los resultados.

## Por qué

- **Meilisearch cumple la consigna con el menor costo operativo** para nuestro volumen, y la tolerancia a errores de tipeo es justo lo que necesita una búsqueda de productos por nombre.
- **Un documento por producto con agregados precalculados** responde la pregunta del comprador ("¿qué productos hay y desde cuánto?") sin tener que juntar ofertas en el momento de la consulta.
- **El índice es derivado y reconstruible**: si se corrompe o se pierde, se rehace desde PostgreSQL. Por eso no necesita backup ni consistencia fuerte.
- **5 s** es corto para un humano que busca, y alcanzable con un consumidor de eventos; no exige escritura sincrónica en dos lugares.

## Consecuencias aceptadas

- **Un contenedor más** (Meilisearch) y su volumen en el compose.
- **Los resultados pueden estar hasta 5 s desactualizados**, y el `precio_min` de la búsqueda no incluye flete ni descuento por volumen: puede no coincidir con el primer puesto del ranking.
- **La búsqueda depende de la mensajería** para mantenerse al día; si un evento termina en la DLQ, lo que traía no llega ni a PostgreSQL ni al índice ([ADR-005](ADR-005-comunicacion.md)).
- **Si un proveedor no tiene stock de un producto, el filtro `proveedor` no lo encuentra**, porque la lista solo tiene a los que tienen stock.
- **Un ciclo por réplica mira la tabla cada 2 s:** son 1 consulta por segundo a PostgreSQL entre las dos, sin carga de eventos.
- **Si Meilisearch cae, la búsqueda devuelve `503`**, pero la capacidad publicada sigue funcionando porque no depende del índice.

## Con qué lo medimos

Mediciones del 06/11 en el sistema levantado con `docker compose`, en la misma máquina que [ADR-013](ADR-013-capacidad-y-costos.md):

| Qué | Resultado | Objetivo |
|---|---|---|
| **Retraso de indexación** (`actualizado_en` de la oferta → aparece en la búsqueda), 25 eventos reales mock de FAV → `import-service` → RabbitMQ → consumidor → Meilisearch, sondeando cada 20 ms | mediana **129 ms**, **p95 266 ms**, máximo 708 ms; ningún evento perdido | p95 < 5 s |
| **Latencia de búsqueda** por el gateway, 100 req/s durante 30 s, 3 000 requests sin errores | mediana 4,1 ms, **p95 49,4 ms**, máximo 62 ms | p95 < 100 ms |
| **Reconstrucción** con el seed y el catálogo del mock (40 productos) | **0,3 s** | — |
| **Reconstrucción** con 10 000 productos y 60 000 ofertas sintéticas, en una base y un índice aparte | **2,3 s** | — |

La distribución de la latencia de búsqueda es rara: mediana de 4 ms y p90 de 48 ms. Cumple el objetivo, pero no se investigó de dónde sale ese segundo grupo. El retraso se midió con un cambio de precio por evento y sin otra carga; no se midió bajo un test de carga en paralelo.

**Fallas**, en [`tests/carga/resultados/2026-11-06-fallas/`](../../tests/carga/resultados/2026-11-06-fallas/): con Meilisearch detenido la búsqueda responde `503` y la capacidad 200; un evento se aplica y confirma, el SKU queda pendiente y, al volver el motor, se indexa sin otro evento y la tabla queda vacía.

**Tests** contra Meilisearch real en el paquete: filtros, orden, paginación, tolerancia a errores de tipeo, reconstrucción y 12 valores hostiles en los filtros, que no amplían los resultados.

Evidencia: [`2026-11-06-retraso-indice`](../../tests/carga/resultados/2026-11-06-retraso-indice/) y [`2026-11-06-busqueda`](../../tests/carga/resultados/2026-11-06-busqueda/).

## Qué nos haría cambiar de idea

- **Si necesitamos agregaciones complejas** (por ejemplo, histogramas de precios por categoría y proveedor), evaluar Elasticsearch u OpenSearch.
- **Si el retraso medido supera los 5 s** de forma sostenida, pasar a indexación sincrónica desde el mismo servicio, a costa de acoplar la escritura al motor.
- **Si la cátedra acepta el full-text de PostgreSQL** como motor de búsqueda, sería más simple y sin desincronización posible.
