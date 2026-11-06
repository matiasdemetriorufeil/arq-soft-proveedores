-- 002_seed: datos de demostracion. Son los mismos 30 SKUs del seed de
-- proveedores-service (services/proveedores-service/migrations/002_seed.sql)
-- para que la demo sea coherente: el SKU que se consulta en el ranking es el
-- mismo que se reserva acá.
--
-- Valores deterministas, elegidos a mano: los consumibles (cable, conectores,
-- grampas) tienen stock y minimo altos; los equipos caros, bajos. PROT-DC-1000V
-- arranca por debajo de su minimo a propósito, para tener un caso de faltante a
-- la vista cuando se construya la reposicion (HU-14).
--
-- Idempotente: ON CONFLICT DO NOTHING no duplica filas si ya existen.

INSERT INTO stock_productos (sku, disponible, reservado, minimo) VALUES
    ('PAN-450W-MONO',  120, 0,  30),
    ('PAN-550W-MONO',  200, 0,  50),
    ('PAN-600W-BIF',    40, 0,  10),
    ('PAN-330W-POLI',   90, 0,  20),
    ('PAN-100W-FLEX',   60, 0,  15),
    ('INV-3KW-ONG',     25, 0,   8),
    ('INV-5KW-ONG',     18, 0,   6),
    ('INV-5KW-HIB',     12, 0,   4),
    ('INV-10KW-TRI',     6, 0,   2),
    ('MIC-800W',        80, 0,  20),
    ('BAT-LFP-5KWH',    15, 0,   5),
    ('BAT-LFP-10KWH',    8, 0,   3),
    ('BAT-GEL-200AH',   22, 0,   8),
    ('REG-MPPT-60A',    35, 0,  10),
    ('REG-PWM-30A',     70, 0,  20),
    ('CAB-SOLAR-4MM', 5000, 0, 800),
    ('CAB-SOLAR-6MM', 4200, 0, 800),
    ('CON-MC4-PAR',   1500, 0, 300),
    ('CON-MC4-Y',      400, 0, 100),
    ('EST-ALU-TECHO',   65, 0,  20),
    ('EST-ALU-PISO',    55, 0,  20),
    ('RIEL-ALU-2M',    320, 0,  80),
    ('GRAP-MED',       900, 0, 200),
    ('GRAP-FIN',       850, 0, 200),
    ('PROT-DC-1000V',    4, 0,  10),
    ('FUS-DC-15A',     260, 0,  60),
    ('SECC-DC-32A',     95, 0,  25),
    ('TAB-STRING-2E',   30, 0,  10),
    ('MED-BIDIR',       45, 0,  12),
    ('MON-WIFI',        75, 0,  20)
ON CONFLICT DO NOTHING;
