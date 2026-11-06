-- 003_ordenes_reposicion: la orden de reposicion de HU-14. Idempotente
-- (IF NOT EXISTS): el binario reaplica todos los .sql en cada arranque.

-- Los cinco estados son los de RN-15 y no hay mas: el CHECK los fija en la base
-- igual que el CHECK (disponible >= 0) fija la regla de sobreventa. Las
-- transiciones permitidas entre ellos NO estan acá: viven en la capa de
-- servicio (internal/service/ordenes.go), que es la dueña de la regla.
--
-- proveedor_id, proveedor_nombre y costo_total son NULL a propósito: una orden
-- puede nacer sin proveedor sugerido si proveedores-service no respondió, y en
-- ese caso `motivo` dice por qué. `intento` cuenta los envíos al proveedor; hoy
-- siempre 0, porque el ciclo no envía nada (eso es del objetivo C).
CREATE TABLE IF NOT EXISTS ordenes_reposicion (
    id               UUID PRIMARY KEY,
    sku              TEXT        NOT NULL REFERENCES stock_productos (sku),
    cantidad         INTEGER     NOT NULL CHECK (cantidad > 0),
    estado           TEXT        NOT NULL DEFAULT 'BORRADOR'
                                 CHECK (estado IN ('BORRADOR', 'SOLICITADA', 'CONFIRMADA',
                                                   'VENCIDA', 'EN_REVISION')),
    proveedor_id     BIGINT,
    proveedor_nombre TEXT,
    costo_total      NUMERIC(14, 2),
    motivo           TEXT,
    intento          INTEGER     NOT NULL DEFAULT 0 CHECK (intento >= 0),
    creado_en        TIMESTAMPTZ NOT NULL DEFAULT now(),
    actualizado_en   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Esta es la invariante de CA-14.2 escrita en el esquema, no solo en la lógica:
-- un producto puede tener muchas ordenes a lo largo del tiempo, pero una sola
-- sin cerrar. El indice es parcial porque CONFIRMADA es el unico estado final:
-- VENCIDA y EN_REVISION cuentan como abiertas a proposito (ADR-009 dice que las
-- dos necesitan que una persona intervenga, y crear otra orden mientras una
-- VENCIDA sigue sin resolver podria terminar comprando dos veces lo mismo).
--
-- Resuelve gratis la carrera entre dos replicas corriendo el ciclo a la vez: la
-- que pierde recibe 23505 y saltea el SKU, en lugar de duplicar la orden.
CREATE UNIQUE INDEX IF NOT EXISTS ux_ordenes_abierta_por_sku
    ON ordenes_reposicion (sku)
    WHERE estado <> 'CONFIRMADA';

-- La consulta del listado filtra por estado y ordena por fecha descendente.
CREATE INDEX IF NOT EXISTS idx_ordenes_estado_creado
    ON ordenes_reposicion (estado, creado_en DESC);
