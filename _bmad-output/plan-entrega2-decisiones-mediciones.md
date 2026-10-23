---
title: 'Decisiones, mediciones y entrega: ADR D5, D6, D7, D9, D10 y D13, test de carga y guion de la demo (Entrega 2, Integrante 4)'
type: 'feature'
ticket: ''
created: '2026-10-23'
status: 'built'
baseline_revision: 'e85555fc9b9ce0a4dbaa1d94fdf8e748c84d4f51'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'thorough'
review_source: 'pinned'
lenses_ran: ['adversarial', 'edge-case-hunter', 'structure', 'prose']
review_loop_iteration: 1
context:
  - '{project-root}/docs/PLAN-ENTREGA-2.md'
  - '{project-root}/docs/consigna-practico-integrador-2026.md'
  - '{project-root}/docs/TP-Integrador-Complemento de Consigna.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** La Entrega 2 (23/10) exige los ADR D6, D7, D9, D10 y D13 y la validación de D5 (consigna 6.3), un test de carga documentado y analizado (2.11) y una demo de 5 minutos con tag `entrega-2`. No había plantilla de ADR, ni D5 (la Entrega 1 no se presentó), ni ninguna medición de capacidad.

**Approach:** Plantilla de ADR y seis ADR que describen lo implementado como aceptado y lo que no existe como propuesto, contrastados con el código. Test de carga con k6 (modelo abierto) contra el sistema real levantado con compose, con la evidencia guardada en el repo, y guion de la demo con checklist. Trabajo sobre `docs/base` (Integrante 3), todavía sin mergear.

**Decisiones del usuario:**
- Trabajar como si el PR de `docs/base` estuviera mergeado: la rama `docs/adrs` sale de su commit.
- No se toma ningún bonus; queda anunciado en README y ARCHITECTURE.
- Los cambios de código que surgieron (pool del gateway, `stop_grace_period`, tope de Jaeger, reintento solo ante errores de conexión) son del Integrante 2: se documentan como propuestas y no se commitean.

## Boundaries & Constraints

**Always:**
- Todo lo que un ADR declare como implementado tiene que coincidir con `services/`, `docker-compose.yml` y `.env.example` (el revisor automático los compara).
- Cada número citado tiene que salir de un archivo guardado en `tests/carga/resultados/`.
- Formato de ADR del complemento 6.1, con los siete encabezados obligatorios.

**Never:**
- No modificar código de los servicios ni el compose (son de los Integrantes 1 y 2). El experimento del pool del gateway se hace en local y se revierte.
- No presentar como hecho lo que es una hipótesis o una propuesta.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Serie de carga | `bash tests/carga/medir.sh` con el sistema levantado | `k6-*.json`, `k6-*.txt` y `stats-*.csv` por corrida | si k6 falla, la corrida igual guarda su salida |
| Escalones inválidos | `ESCALONES=,` | k6 aborta con un mensaje claro | `throw` en el script |
| Sobrecarga | 2 000 req/s pedidas | respuestas contadas por código (`respuestas_504`, …) | umbrales en rojo, no un falso verde |
| Demo en PowerShell | comandos del guion | 200, 404, 401 y la misma `trace_id` en dos logs | `curl.exe`, `-o NUL`, id único por corrida |

</frozen-after-approval>

## Code Map

- `docs/adr/TEMPLATE.md` -- plantilla de ADR
- `docs/adr/ADR-005-comunicacion.md` · `ADR-010-resiliencia.md` -- lo implementado en `proxy.go`, `balanceador.go`, `repo.go` y los dos `main.go`
- `docs/adr/ADR-006-busqueda.md` · `ADR-007-cache.md` · `ADR-009-consumo-proveedor.md` -- propuestas
- `docs/adr/ADR-013-capacidad-y-costos.md` -- análisis de la carga
- `tests/carga/` -- `recomendados.js`, `medir.sh`, `resumir.py`, `resultados/`
- `docs/DEMO-ENTREGA-2.md`, `docs/ARCHITECTURE.md` (§10 y §11), `docs/PLAN-ENTREGA-2.md`, `README.md`

## Tasks & Acceptance

**Execution:**
- [x] `docs/adr/TEMPLATE.md` -- plantilla con las reglas del grupo
- [x] `docs/adr/ADR-005…013` -- seis ADR
- [x] `tests/carga/*` -- script, serie, resumen y evidencia
- [x] `docs/DEMO-ENTREGA-2.md` -- checklist, tag, preparación, guion y preguntas
- [x] `docs/ARCHITECTURE.md`, `docs/PLAN-ENTREGA-2.md`, `README.md` -- tabla de decisiones, limitaciones, estado y bonus

**Acceptance Criteria:**
- Given el sistema levantado, when se corre `medir.sh` y `resumir.py`, then se obtienen las tablas de ADR-013.
- Given los ADR, when se comparan con el código, then ninguna afirmación sobre comportamiento implementado lo contradice.
- Given la demo, when se siguen los comandos en PowerShell o en bash, then cada paso da el resultado descrito.

## Implementation Notes

- El límite principal es la CPU. El gateway es el que más gasta (40 % del total por request) y usa `http.DefaultTransport` (`MaxIdleConnsPerHost = 2`). Con el pool en 256, su CPU baja 31 % y 2 000 req/s entran en el objetivo. El cambio se propone para el Integrante 2.
- El resultado en sobrecarga varía entre corridas: una de las dos rampas colapsó a 1 600 req/s. Por eso la capacidad declarada es de 1 200 req/s.
- Jaeger llega a 1,6 GB en una rampa: en memoria y sin tope.
- `go.work.sum` aparece modificado por el editor; no es parte de este trabajo y no se commitea.

## Review Triage Log

**Pasada 1** (bmad-review, cuatro lentes, 63 hallazgos). Todos corregidos salvo los que piden cambiar código ajeno, que quedaron documentados:
- Adversarial (19): números de D13 sin la rampa que falló, contradicción del "techo", explicación falsa de las requests no lanzadas, CPU sin evidencia guardada, garantía de reintento que el código no cumple, memoria de Jaeger, objetivo de aciertos de la caché, `/readyz` sin efecto en compose, ejemplo de D6 contra el seed, D5 "validado", bonus, filas del plan, contrafáctico de k6 y frescura de D9 sin campo en el contrato. → corregidos; la serie de carga se repitió con evidencia guardada.
- Edge-case (13): comandos de la demo en PowerShell, `/dev/null`, `X-Request-ID` repetido, tag antes del ensayo, MSYS en Git Bash, validación de `ESCALONES`, umbral de requests no lanzadas. → corregidos. `TIMEOUT_UPSTREAM ≤ 2 s` y `stop_grace_period` → documentados como consecuencias (código del Integrante 2).
- Structure (15) y prose (16): orden de la demo, redundancias entre D5 y D10, largo de D13, sección extra en D9, plantilla, concordancias y ambigüedades. → aplicados.

**Pasada 2** (adversarial, verificación de hechos contra la evidencia y el código, 7 hallazgos): 0,01 % mal redondeado (era 0,003 %), objetivo con un tercer criterio que las corridas citadas no cumplían, explicación de VU no acotada a la serie de referencia, "desde 1 000" contradicho por una exploratoria, CPU de la base sin acotar a la tasa, intervalo real de `docker stats` y el 40 % atribuido al transporte en vez de al gateway. → corregidos. ADR-005 y ADR-010 coinciden con el código.

## Verification

**Commands:**
- `bash tests/carga/medir.sh && python tests/carga/resumir.py tests/carga/resultados/<fecha>` -- expected: tablas equivalentes a las de ADR-013 (con la variación entre corridas que el ADR documenta)
- `docker run … -e ESCALONES=50 -e DURACION_ESCALON=3s grafana/k6:1.3.0 run /scripts/recomendados.js` -- expected: todos los umbrales en verde

**Manual checks:**
- Comandos de `docs/DEMO-ENTREGA-2.md` en PowerShell y en bash: 200, 404, 401 y el mismo `trace_id` en los logs del gateway y de la réplica (probado el 23/10).

**Pasada 3** (adversarial, D9 reescrito sobre el contrato real de FAV v1.1.0, 9 hallazgos): `A_VERIFICAR` viene de `VENCIDO` (se agregó `VENCIDA → EN_REVISION` en el ADR y en RN-15/CA-14.3); `Idempotency-Key` por intento de envío y límite de 48 h; no hay "siguiente proveedor" para una prenda que solo vende FAV; el webhook entraba por el gateway, contra la consigna 4.1 (ahora lo recibe `stock-service` directo); ADR-001 y ADR-002 sin actualizar; alcance real del mock (sin estado); webhook mezclado con los eventos de nuestra mensajería en ADR-005; `/healthz` y `/readyz` también sin API key; dudas del contrato por issue en su repo. → todos corregidos.
