# Test de carga de la capacidad

[`recomendados.js`](recomendados.js) es un script de [k6](https://k6.io) que llama a
`GET /api/v1/proveedores/recomendados` a través del gateway, con un SKU al azar entre
los 30 del seed y una `cantidad` al azar entre 1 y 50. Usa un modelo abierto
(`ramping-arrival-rate`): mantiene la tasa de requests aunque el sistema se ponga
lento, como haría un consumidor real.

Objetivo con el que se evalúa: **p95 < 300 ms y menos de 1 % de errores**. El script
además marca en rojo cualquier request que k6 no haya llegado a lanzar: no es parte
del objetivo, sino una condición para que la corrida mida la tasa que dice. El análisis de los resultados está en
[ADR-013](../../docs/adr/ADR-013-capacidad-y-costos.md).

| Archivo | Qué es |
|---|---|
| [`recomendados.js`](recomendados.js) | El escenario de k6. Cuenta las respuestas por código (`respuestas_200`, `respuestas_504`, …). |
| [`medir.sh`](medir.sh) | Corre la serie completa (rampa más una corrida por tasa) y guarda el resumen de k6 y `docker stats` de cada corrida. |
| [`resumir.py`](resumir.py) | Arma las tablas de abajo a partir de una carpeta de resultados. |
| [`resultados/`](resultados/) | La evidencia de cada medición, por fecha. |

## Cómo correrlo

Con el sistema levantado (`docker compose up -d --build`), desde la raíz del repo.
k6 corre en un contenedor dentro de la red interna del compose: no hace falta
instalarlo y no se mide el reenvío de puertos de Docker Desktop.

**La serie completa**, como en ADR-013 (en Windows, desde Git Bash; tarda unos
10 minutos):

```bash
bash tests/carga/medir.sh                          # guarda en tests/carga/resultados/<fecha>
python tests/carga/resumir.py tests/carga/resultados/<fecha>
```

**Una corrida suelta**, por ejemplo 1 000 req/s durante 30 s:

```bash
# bash en Linux o macOS
docker run --rm --network arq-soft-proveedores_interna \
  -v "$PWD/tests/carga:/scripts" \
  -e BASE_URL=http://api-gateway:8080 -e ESCALONES=1000 -e DURACION_ESCALON=30s \
  grafana/k6:1.3.0 run /scripts/recomendados.js

# Git Bash en Windows: sin MSYS_NO_PATHCONV, /scripts se convierte en una ruta de Windows
MSYS_NO_PATHCONV=1 docker run --rm --network arq-soft-proveedores_interna \
  -v "$(pwd -W)/tests/carga:/scripts" \
  -e BASE_URL=http://api-gateway:8080 -e ESCALONES=1000 -e DURACION_ESCALON=30s \
  grafana/k6:1.3.0 run /scripts/recomendados.js
```

```powershell
docker run --rm --network arq-soft-proveedores_interna -v "${PWD}\tests\carga:/scripts" -e BASE_URL=http://api-gateway:8080 -e ESCALONES=1000 -e DURACION_ESCALON=30s grafana/k6:1.3.0 run /scripts/recomendados.js
```

| Variable | Por defecto | Qué es |
|---|---|---|
| `BASE_URL` | `http://localhost:8080` | URL del gateway |
| `API_KEY` | `clave-de-desarrollo` | la de `.env.example` |
| `ESCALONES` | `50,100,200,400,800,1200,1600` | req/s de cada escalón, separados por coma |
| `DURACION_ESCALON` | `30s` | cuánto se sostiene cada escalón |

Con varios escalones, cada uno sube en 5 s desde el anterior y se sostiene
`DURACION_ESCALON`. Con uno solo, la corrida arranca directamente en esa tasa: los
primeros segundos incluyen el calentamiento.

## Cómo leer los resultados

- **Atendidas** es la tasa real de requests completadas. Si queda por debajo de la
  pedida, k6 no llegó a lanzar todas (**No lanzadas**).
- **En las corridas aisladas de la serie de referencia, por debajo de 2 000 req/s,
  las requests no lanzadas no vienen de que falten usuarios virtuales**: se usaron
  como mucho 349 de los 2 000 disponibles. Vienen de que **k6 comparte la CPU con el
  sistema** (llegó a usar más de un núcleo) y no alcanza a generar la tasa exacta.
  Por eso el umbral `dropped_iterations` marca en rojo desde 1 000 req/s: la medición
  ya está limitada en parte por el generador.
- **Cuando el sistema se traba, la causa es otra:** a 2 000 req/s, y en el escalón de
  1 600 de la rampa que colapsó, se agotaron los 2 000 VU porque cada request quedaba
  esperando segundos. Ahí las no lanzadas sí son un síntoma del colapso.
- **Por código** cuenta las respuestas que no fueron `200`.

## Resultados del 23/10/2026

Notebook AMD Ryzen 5 7235HS (8 hilos), 16 GB de RAM, Docker Desktop con 8 CPU y
7,6 GB asignados. Todo en la misma máquina: los cinco contenedores **y k6**. Código
de `main` del 23/10. Evidencia en
[`resultados/2026-10-23/`](resultados/2026-10-23/).

**Corridas aisladas** (una tasa, 35 s cada una, en este orden y con Jaeger recreado
antes de cada una):

| Tasa pedida | Atendidas | p50 | p95 | p99 | Máx. | Errores | No lanzadas |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 200 req/s | 200 | 2,1 ms | 2,9 ms | 4,4 ms | 18 ms | 0 % | 0 |
| 600 req/s | 600 | 2,0 ms | 10,5 ms | 27,4 ms | 64 ms | 0 % | 0 |
| 1 000 req/s | 996 | 2,7 ms | 42,5 ms | 78,7 ms | 234 ms | 0 % | 81 |
| 1 200 req/s | 1 182 | 9,1 ms | 125 ms | 200 ms | 456 ms | 0 % | 607 |
| 1 400 req/s | 1 361 | 17,3 ms | 175 ms | 305 ms | 735 ms | 0 % | 1 359 |
| 1 600 req/s | 1 550 | 18,6 ms | 171 ms | 306 ms | 993 ms | 0 % | 1 731 |
| 2 000 req/s | 1 659 | 83,1 ms | **1,57 s** | 2,65 s | 4,38 s | 0,003 % (2 × `504`) | 11 627 |

**Rampas** (escalones por defecto, 50 → 1 600 req/s, 4 minutos):

| Corrida | p50 | p95 | p99 | Máx. | Errores | No lanzadas |
|---|---:|---:|---:|---:|---:|---:|
| Primera del día (`k6-rampa-primera`) | 35,6 ms | **4,03 s** | 7,62 s | 15,5 s | **3,58 %** | 32 477 |
| Serie de las 16:30 (`k6-rampa`) | 2,4 ms | 76,5 ms | 191 ms | 912 ms | 0 % | 1 217 |

La primera rampa colapsó en el escalón de 1 600 req/s, con el gateway arriba del
600 % de CPU: según sus logs, devolvió unas 4 000 respuestas `504` y 84 `502`, y
marcó dos veces una réplica como caída. La segunda, con el mismo código, no. La diferencia
entre las dos se analiza en ADR-013.

**CPU media** (en % de un núcleo) **durante cada corrida aislada**, de `docker stats`:

| Tasa | gateway | réplica 1 | réplica 2 | PostgreSQL | Jaeger | k6 |
|---:|---:|---:|---:|---:|---:|---:|
| 200 | 25 % | 11 % | 11 % | 9 % | 1 % | 25 % |
| 600 | 80 % | 38 % | 38 % | 27 % | 7 % | 57 % |
| 1 000 | 139 % | 66 % | 69 % | 54 % | 21 % | 81 % |
| 1 400 | 212 % | 101 % | 93 % | 82 % | 16 % | 111 % |
| 2 000 | 308 % | 86 % | 79 % | 86 % | 11 % | 106 % |

Memoria máxima: Jaeger llegó a **1,6 GB** en la rampa arrancando de cero (guarda las
trazas en memoria, sin tope); el gateway, a 286 MB en la rampa que colapsó; cada
réplica y PostgreSQL, por debajo de 100 MB.
