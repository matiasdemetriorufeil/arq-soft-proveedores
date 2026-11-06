-- 001_init: esquema de stock-service. Idempotente (IF NOT EXISTS): el binario
-- reaplica todos los .sql en cada arranque.

-- stock_productos es la fila que se bloquea con SELECT … FOR UPDATE al reservar.
-- El CHECK (disponible >= 0) es la red de seguridad de la base: aunque falle la
-- lógica de la aplicación, la sobreventa no entra (RN-13).
CREATE TABLE IF NOT EXISTS stock_productos (
    sku            TEXT PRIMARY KEY,
    disponible     INTEGER     NOT NULL DEFAULT 0 CHECK (disponible >= 0),
    reservado      INTEGER     NOT NULL DEFAULT 0 CHECK (reservado >= 0),
    minimo         INTEGER     NOT NULL DEFAULT 0 CHECK (minimo >= 0),
    actualizado_en TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- clave_idempotencia es UNIQUE y eso es lo que hace idempotente a la operación:
-- dos reintentos simultáneos de la misma reserva no pueden aplicar el stock dos
-- veces, porque el segundo INSERT falla con violación de clave única y la capa
-- de servicio responde releyendo la reserva ya aplicada (CA-13.2).
CREATE TABLE IF NOT EXISTS reservas (
    id                 UUID PRIMARY KEY,
    sku                TEXT        NOT NULL REFERENCES stock_productos (sku),
    cantidad           INTEGER     NOT NULL CHECK (cantidad > 0),
    clave_idempotencia TEXT        NOT NULL UNIQUE,
    estado             TEXT        NOT NULL DEFAULT 'CONFIRMADA'
                                  CHECK (estado IN ('CONFIRMADA', 'CANCELADA')),
    creado_en          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Las reservas se consultan por SKU para auditar el movimiento de una fila de
-- stock; la clave de idempotencia ya tiene índice por el UNIQUE.
CREATE INDEX IF NOT EXISTS idx_reservas_sku ON reservas (sku);
