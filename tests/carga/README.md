# Test de carga de la capacidad

[`recomendados.js`](recomendados.js) es un script de [k6](https://k6.io) que le pega a
`GET /api/v1/proveedores/recomendados` a través del gateway, con un SKU al azar entre
los 30 del seed y una `cantidad` al azar entre 1 y 50. Usa un modelo abierto
(`ramping-arrival-rate`): mantiene la tasa de requests aunque el sistema se ponga
lento, como haría un consumidor real.

Objetivo con el que se evalúa: **p95 < 300 ms y menos de 1 % de errores**. El
análisis de los resultados está en [ADR-013](../../docs/adr/ADR-013-capacidad-y-costos.md).

## Cómo correrlo

Con el sistema levantado (`docker compose up -d --build`), desde la raíz del repo.
k6 corre en un contenedor dentro de la red interna del compose, así no hace falta
instalarlo y no se mide el reenvío de puertos de Docker Desktop:

```bash
# Escalones por defecto: 50, 100, 200, 400, 800, 1200 y 1600 req/s, 30 s cada uno.
docker run --rm --network arq-soft-proveedores_interna \
  -v "$PWD/tests/carga:/scripts" \
  -e BASE_URL=http://api-gateway:8080 \
  grafana/k6:1.3.0 run /scripts/recomendados.js

# Una sola tasa, por ejemplo 1000 req/s durante 30 s:
docker run --rm --network arq-soft-proveedores_interna \
  -v "$PWD/tests/carga:/scripts" \
  -e BASE_URL=http://api-gateway:8080 -e ESCALONES=1000 -e DURACION_ESCALON=30s \
  grafana/k6:1.3.0 run /scripts/recomendados.js
```

En PowerShell, reemplazar `$PWD/tests/carga` por `${PWD}\tests\carga` y las `\` del
final de línea por `` ` ``.

| Variable | Por defecto | Qué es |
|---|---|---|
| `BASE_URL` | `http://localhost:8080` | URL del gateway |
| `API_KEY` | `clave-de-desarrollo` | la de `.env.example` |
| `ESCALONES` | `50,100,200,400,800,1200,1600` | req/s de cada escalón |
| `DURACION_ESCALON` | `30s` | cuánto se sostiene cada escalón (más 5 s de subida) |

Para ver qué componente se satura, en otra terminal:
`docker stats --format "{{.Name}} {{.CPUPerc}} {{.MemUsage}}"`.

## Resultados del 23/10/2026

Notebook AMD Ryzen 5 7235HS (8 hilos), 16 GB de RAM, Docker Desktop con 8 CPU y
7,6 GB asignados. Todo en la misma máquina: los cinco contenedores **y k6**. Una
corrida de 35 s por tasa (5 s de subida y 30 s sostenidos), código de `main` del 23/10.

| Tasa pedida (req/s) | Atendidas (req/s) | p50 | p95 | p99 | Errores |
|---:|---:|---:|---:|---:|---:|
| 100 | 100 | 2,5 ms | 4,6 ms | 11,2 ms | 0 % |
| 200 | 200 | 2,4 ms | 4,1 ms | 13,7 ms | 0 % |
| 300 | 300 | 2,4 ms | 4,7 ms | 17,1 ms | 0 % |
| 400 | 400 | 2,2 ms | 6,3 ms | 16,9 ms | 0 % |
| 600 | 600 | 2,0 ms | 9,5 ms | 27,0 ms | 0 % |
| 800 | 797 | 2,1 ms | 17,3 ms | 77,1 ms | 0 % |
| 1000 | 998 | 2,6 ms | 49,8 ms | 95,0 ms | 0 % |
| 1200 | 1184 | 2,2 ms | 62,1 ms | 197 ms | 0 % |
| 1400 | 1389 | 2,3 ms | 50,0 ms | 113 ms | 0 % |
| 1600 | 1543 | 13,2 ms | 166 ms | 396 ms | 0 % |
| 2000 | **1151** | 654 ms | **3,16 s** | 3,96 s | **1,81 %** (`504`) |

"Atendidas" por debajo de la pedida significa que k6 no tuvo usuarios virtuales
libres para lanzar todas las requests: el sistema ya no daba abasto.
