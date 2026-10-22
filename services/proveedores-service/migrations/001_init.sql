-- 001_init: esquema de proveedores, productos y ofertas.
-- Idempotente: se aplica en cada arranque.

CREATE TABLE IF NOT EXISTS proveedores (
    id          BIGINT PRIMARY KEY,
    nombre      TEXT        NOT NULL UNIQUE,
    cuit        TEXT        NOT NULL UNIQUE,
    creado_en   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS productos (
    sku         TEXT PRIMARY KEY,
    nombre      TEXT        NOT NULL,
    categoria   TEXT        NOT NULL,
    unidad      TEXT        NOT NULL DEFAULT 'unidad',
    creado_en   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ofertas (
    proveedor_id              BIGINT        NOT NULL REFERENCES proveedores (id),
    producto_sku              TEXT          NOT NULL REFERENCES productos (sku),
    precio_unitario           NUMERIC(14,2) NOT NULL CHECK (precio_unitario >= 0),
    descuento_pct             NUMERIC(5,2)  NOT NULL DEFAULT 0 CHECK (descuento_pct >= 0 AND descuento_pct <= 100),
    cantidad_minima_descuento INTEGER       NOT NULL DEFAULT 0 CHECK (cantidad_minima_descuento >= 0),
    flete                     NUMERIC(14,2) NOT NULL DEFAULT 0 CHECK (flete >= 0),
    stock_disponible          INTEGER       NOT NULL DEFAULT 0 CHECK (stock_disponible >= 0),
    actualizado_en            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    PRIMARY KEY (proveedor_id, producto_sku)
);

-- La consulta principal filtra por SKU.
CREATE INDEX IF NOT EXISTS idx_ofertas_producto_sku ON ofertas (producto_sku);
