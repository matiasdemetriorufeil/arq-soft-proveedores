# ADR-006 — Búsqueda (D6)

- **Estado:** **propuesto**. No hay motor de búsqueda en el código ni en el compose; hoy la única consulta es por SKU exacto contra PostgreSQL.
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D6. Información indexada, criterios de consulta, actualización o reconstrucción del índice y retraso aceptable.

## Contexto

La consigna (2.6) pide que una entidad del dominio tenga búsqueda **con un motor de búsqueda** (un `LIKE` en SQL no cuenta), con paginación, filtros relevantes para el dominio, al menos un criterio de orden, un índice sincronizado con los cambios y un **retraso máximo tolerable** definido. El profe pidió que esas demoras sean cortas.

La capacidad que publicamos exige conocer el SKU (`PAN-550W-MONO`). Un comprador real no empieza por el SKU: empieza por "panel 550 W", "inversor híbrido" o "baterías con stock en al menos tres proveedores". La búsqueda es el paso previo natural a pedir el ranking, y es lo que va a usar el frontend.

## Opciones evaluadas

1. **Full-text de PostgreSQL** (`tsvector` + índice GIN). Sin componentes nuevos y siempre consistente, pero el complemento pide un motor de búsqueda real, y la tolerancia a errores de tipeo ("bateria litio", "invesor") requiere extensiones extra (`pg_trgm`).
2. **Elasticsearch.** El estándar de la industria, con agregaciones potentes. Necesita una JVM con 1 a 2 GB de memoria solo para arrancar: en nuestras notebooks y en una VM chica de la nube es caro para 30 productos.
3. **Meilisearch.** Un binario con tolerancia a errores de tipeo de fábrica, filtros, orden, paginación y facetas; corre con unos cientos de MB y tiene cliente oficial en Go. Menos potente en agregaciones y sin clúster en la versión libre.

## Qué elegimos

**Meilisearch**, con un índice de **productos** alimentado por `proveedores-service`, que es el dueño de `productos` y `ofertas` hoy ([ADR-001](ADR-001-limites-de-los-servicios.md)).

**Qué se indexa:** un documento por producto, con lo que se busca y lo que se filtra ya calculado (valores ilustrativos):

```json
{
  "sku": "PAN-550W-MONO",
  "nombre": "Panel solar 550 W monocristalino",
  "categoria": "paneles",
  "unidad": "unidad",
  "precio_min": 118500.00,
  "proveedores_con_stock": 2,
  "stock_total": 412,
  "proveedores": ["Andes Solar SRL", "Helios Importaciones SRL"],
  "actualizado_en": "2026-10-23T14:02:11Z"
}
```

- Campos buscables: `nombre`, `sku`, `proveedores`.
- Filtros: `categoria`, `proveedores`, `precio_min` por rango y `proveedores_con_stock > 0` (solo con stock).
- Orden: `precio_min` ascendente o descendente, y relevancia por defecto.
- Paginación: `pagina` y `tamanio` (máximo 50, igual que el `limite` de la capacidad).

**Consulta propuesta** (a publicar en el gateway, no en el contrato v1 de la capacidad):

```
GET /api/v1/productos/buscar?q=panel 550&categoria=paneles&con_stock=true&orden=precio_asc&pagina=1&tamanio=20
```

La búsqueda **no reemplaza al ranking**: devuelve productos con su precio mínimo orientativo; el ranking con descuento por volumen y flete sigue saliendo de `GET /api/v1/proveedores/recomendados`, que lee la fuente de verdad.

**Actualización del índice:** por evento. Cuando cambian las ofertas de un proveedor (`ListaPreciosActualizada`, [ADR-005](ADR-005-comunicacion.md)), el consumidor de `proveedores-service` recalcula los documentos de los SKU afectados y los envía a Meilisearch en lote. La operación es idempotente: reindexar el mismo SKU dos veces deja el mismo documento.

**Reconstrucción:** un comando `reindexar` que lee todo PostgreSQL, carga un índice nuevo (`productos_tmp`) y lo intercambia con el vigente (`swap-indexes`), así la búsqueda nunca queda vacía. Se corre al arrancar si el índice no existe, y a mano si se sospecha desincronización.

**Retraso máximo tolerable:** **5 segundos** entre que se confirma el cambio de una oferta en PostgreSQL y su aparición en los resultados.

## Por qué

- **Meilisearch cumple la consigna con el menor costo operativo** para nuestro volumen, y la tolerancia a errores de tipeo es justo lo que necesita una búsqueda de productos por nombre.
- **Un documento por producto con agregados precalculados** responde la pregunta del comprador ("¿qué productos hay y desde cuánto?") sin tener que juntar ofertas en el momento de la consulta.
- **El índice es derivado y reconstruible**: si se corrompe o se pierde, se rehace desde PostgreSQL. Por eso no necesita backup ni consistencia fuerte.
- **5 s** es corto para un humano que busca, y alcanzable con un consumidor de eventos; no exige escritura sincrónica en dos lugares.

## Consecuencias aceptadas

- **Un contenedor más** (Meilisearch) y su volumen en el compose.
- **Los resultados pueden estar hasta 5 s desactualizados**, y el `precio_min` de la búsqueda no incluye flete ni descuento por volumen: puede no coincidir con el primer puesto del ranking.
- **La búsqueda depende de la mensajería** para mantenerse al día; si los eventos se pierden, el índice se desfasa hasta la próxima reconstrucción.
- **Si Meilisearch cae, la búsqueda devuelve `503`**, pero la capacidad publicada sigue funcionando porque no depende del índice.
- **Hoy los datos solo cambian por el seed**, así que la sincronización por evento no se va a poder demostrar hasta que exista `import-service` o un endpoint de carga de precios.

## Con qué lo medimos

Todavía no hay mediciones: no está implementado. Cuando lo esté:

- **Retraso de indexación:** diferencia entre `actualizado_en` de la oferta y el momento en que aparece en la búsqueda, objetivo **p95 < 5 s**.
- **Latencia de búsqueda** en el test de carga, objetivo p95 < 100 ms.
- **Tiempo de reconstrucción completa** del índice con el seed (30 productos, 170 ofertas) y con un volumen sintético de 10 000 productos.

## Qué nos haría cambiar de idea

- **Si necesitamos agregaciones complejas** (por ejemplo, histogramas de precios por categoría y proveedor), evaluar Elasticsearch u OpenSearch.
- **Si el retraso medido supera los 5 s** de forma sostenida, pasar a indexación sincrónica desde el mismo servicio, a costa de acoplar la escritura al motor.
- **Si la cátedra acepta el full-text de PostgreSQL** como motor de búsqueda, sería más simple y sin desincronización posible.
