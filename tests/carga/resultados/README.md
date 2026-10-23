# Resultados del test de carga

Evidencia de las mediciones citadas en [ADR-013](../../../docs/adr/ADR-013-capacidad-y-costos.md).
Cada carpeta tiene, por corrida, `k6-<corrida>.json` (resumen de k6 con
`--summary-export`), `k6-<corrida>.txt` (salida de k6) y `stats-<corrida>.csv`
(`docker stats` cada 4 a 5 s, unas 8 muestras por corrida aislada: hora,
contenedor, CPU, memoria). Las tablas se arman con
`python tests/carga/resumir.py <carpeta>`.

| Carpeta | Qué es |
|---|---|
| [`2026-10-23/`](2026-10-23/) | **Serie de referencia** con el código de `main`: rampa por defecto más una corrida aislada por tasa (200 a 2 000 req/s), generada con `medir.sh`. Incluye la primera rampa del día (`k6-rampa-primera.json`, `stats-rampa-primera.csv`), que colapsó en el escalón de 1 600 req/s; esa corrida usó la versión anterior del script, sin contadores por código ni registro del contenedor de k6. |
| [`2026-10-23-experimento-pool/`](2026-10-23-experimento-pool/) | La misma serie (rampa, 1 000, 1 600 y 2 000 req/s) con el gateway modificado **solo para el experimento**: `MaxIdleConnsPerHost = 256` en el transporte hacia las réplicas. El cambio no está commiteado. |
| [`2026-10-23-exploratorio/`](2026-10-23-exploratorio/) | Primeras corridas aisladas de la mañana, hechas a mano con la versión anterior del script y sin `docker stats` guardado: `k6-base-*` con el código de `main` y `k6-pool-*` con el mismo cambio del experimento. Sirven para ver cuánto varía el resultado entre corridas en sobrecarga. |
