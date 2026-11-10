-- 004_pedidos_fav: el pedido a FAV de una orden de reposición (ADR-009).
-- Idempotente: el binario reaplica todos los .sql en cada arranque.

-- clave_pedido es la Idempotency-Key del intento en curso, `<id>-<intento>`.
-- Mientras no es NULL, los reintentos usan esa misma clave, así un timeout
-- después de que FAV creó el pedido no termina en un pedido duplicado. Se
-- vuelve NULL cuando FAV da una respuesta definitiva que no es el pedido
-- (un 4xx): el intento siguiente usa una clave nueva.
ALTER TABLE ordenes_reposicion ADD COLUMN IF NOT EXISTS clave_pedido  TEXT;
ALTER TABLE ordenes_reposicion ADD COLUMN IF NOT EXISTS pedido_id     TEXT;
ALTER TABLE ordenes_reposicion ADD COLUMN IF NOT EXISTS pedido_estado TEXT;
ALTER TABLE ordenes_reposicion ADD COLUMN IF NOT EXISTS pedido_total  NUMERIC(14, 2);
ALTER TABLE ordenes_reposicion ADD COLUMN IF NOT EXISTS pago_vence_en TIMESTAMPTZ;

-- Un pedido de FAV corresponde a una sola orden: el aviso y la consulta de
-- respaldo la buscan por acá.
CREATE UNIQUE INDEX IF NOT EXISTS ux_ordenes_pedido_id
    ON ordenes_reposicion (pedido_id)
    WHERE pedido_id IS NOT NULL;

-- Los avisos de FAV ya recibidos. La entrega es al menos una vez: el
-- mismo eventoId puede llegar varias veces y la PRIMARY KEY es la que
-- garantiza que se procesa una sola, aunque lleguen dos a la vez.
CREATE TABLE IF NOT EXISTS avisos_fav_procesados (
    evento_id   UUID PRIMARY KEY,
    pedido_id   TEXT        NOT NULL,
    estado      TEXT        NOT NULL,
    recibido_en TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Prendas de FAV que llevamos en stock (SKU = FAV-<varianteId>, igual que en
-- import-service). Dos arrancan bajo el mínimo a propósito: el ciclo de
-- reposición les crea la orden y el pedido a FAV sale solo, para la demo.
INSERT INTO stock_productos (sku, disponible, reservado, minimo) VALUES
    ('FAV-var-remera-basica-blanca-m', 2, 0, 5),
    ('FAV-var-jean-mom-celeste-38',    1, 0, 4),
    ('FAV-var-buzo-canguro-gris-m',    8, 0, 5)
ON CONFLICT DO NOTHING;
