-- 003_proveedor_fav: FAV (el grupo de ropa) entra como un proveedor más
-- (ADR-009). Sus productos y ofertas no van en el seed: llegan por el evento
-- ListaPreciosActualizada que publica import-service (docs/contracts/eventos.md).
-- El id 11 es el FAV_PROVEEDOR_ID del compose.
-- FAV no nos informó un CUIT: la columna es obligatoria y única, así que va un
-- marcador explícito hasta tenerlo.
-- Idempotente: ON CONFLICT DO NOTHING.

INSERT INTO proveedores (id, nombre, cuit) VALUES
    (11, 'FAV (tienda de ropa)', 'sin-informar-fav')
ON CONFLICT DO NOTHING;
