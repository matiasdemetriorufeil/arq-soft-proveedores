-- 004_eventos_e_indexacion: lo que necesita el consumidor de
-- ListaPreciosActualizada (docs/contracts/eventos.md).
-- Idempotente: se aplica en cada arranque.

-- Deduplicación: la entrega es al menos una vez. El id se guarda como texto
-- porque el sobre no exige formato de UUID y un id raro no es un motivo para
-- romper la transacción.
CREATE TABLE IF NOT EXISTS eventos_procesados (
    evento_id    TEXT PRIMARY KEY,
    procesado_en TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Última secuencia aplicada por proveedor: un evento con una secuencia menor o
-- igual se descarta.
CREATE TABLE IF NOT EXISTS secuencias_proveedor (
    proveedor_id     BIGINT PRIMARY KEY REFERENCES proveedores (id),
    ultima_secuencia BIGINT      NOT NULL,
    actualizado_en   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- SKU cuyo documento de búsqueda hay que recalcular. Se registra en la misma
-- transacción que el cambio, así que un SKU no se pierde si Meilisearch falla.
-- `version` sube si el SKU vuelve a cambiar mientras se indexa: la fila se borra
-- solo si sigue en la versión que se leyó antes de armar el documento.
CREATE TABLE IF NOT EXISTS skus_por_reindexar (
    sku           TEXT PRIMARY KEY,
    version       BIGINT      NOT NULL DEFAULT 1,
    registrado_en TIMESTAMPTZ NOT NULL DEFAULT now()
);
