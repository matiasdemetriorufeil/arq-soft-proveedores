# ADR-007 — Caché (D7)

- **Estado:** **aceptado**, con la medición de abajo. Implementado en [`internal/adapters/redis`](../../services/proveedores-service/internal/adapters/redis/). La caché **saca a PostgreSQL del camino**; en estas tasas no se puede afirmar que suba la capacidad máxima, porque ninguna serie llegó a su límite.
- **Fecha:** 2026-10-23 · actualizado 2026-11-06
- **Entrega:** 2 (23/10); implementación y medición en la 3 (06/11)
- **Decisión de la consigna:** D7. Información almacenada temporalmente, invalidación, vigencia, comportamiento ante fallas y criterios de medición.

## Contexto

La consigna (2.7) pide caché en un flujo de lectura relevante, **justificada por una necesidad observable** y con métricas que muestren su impacto. La cátedra pidió **no inventar una caché**: usar una estándar (Redis).

El flujo de lectura más relevante es la capacidad publicada, `GET /api/v1/proveedores/recomendados`: es lo que consume el grupo de paneles solares y lo que más tráfico va a recibir. Cada request hace una consulta con dos `JOIN` a PostgreSQL.

**Lo que decía la medición del 23/10, antes de construirla:** el test de carga ([ADR-013](ADR-013-capacidad-y-costos.md)) muestra que con el seed actual (30 productos, 170 ofertas) **PostgreSQL no es el límite**: lo que se satura es la CPU, sobre todo la del gateway, y la base consume menos CPU que cada réplica a 1 000 req/s; cerca de la saturación se iguala con ellas, pero sigue muy por debajo del gateway. Con estos datos, una caché casi no movería la capacidad máxima.

La necesidad que sí es observable es otra: **PostgreSQL es la única dependencia que comparten las dos réplicas y un punto único de falla** ([ADR-003](ADR-003-persistencia.md)): hoy cada request de la capacidad hace una consulta, así que la carga sobre la base crece igual que el tráfico. La caché corta esa relación y prepara el sistema para cuando crezcan los datos importados.

## Opciones evaluadas

1. **Caché en memoria de cada réplica** (un `map` con TTL). Sin dependencias, pero cada réplica tiene su copia, la invalidación tiene que llegar a todas y la cátedra pidió no hacer una caché casera.
2. **Redis, guardando la respuesta final** (clave = SKU + cantidad + límite). Máximo ahorro por acierto, pero la cantidad varía en cada pedido y la tasa de aciertos sería baja; invalidar un SKU obliga a borrar muchas claves.
3. **Redis, guardando las ofertas del SKU** (clave = SKU). El ranking (filtro por stock, descuento por volumen, orden) se sigue calculando en memoria, que cuesta microsegundos. Una clave por producto, fácil de invalidar.
4. **Caché HTTP** (`Cache-Control`) para que la guarde el consumidor o un proxy. Gratis para nosotros, pero no controlamos al consumidor ni podemos invalidarla.

## Qué elegimos

**La opción 3: Redis con las ofertas de cada SKU, en `proveedores-service`.**

- **Dónde va en el código:** un decorador del puerto `OfertasRepo` ([ADR-002](ADR-002-arquitectura-interna.md)) en `internal/adapters/redis`, que envuelve al repositorio de PostgreSQL. Ni el dominio ni el handler cambian.
- **Qué se guarda:** `ofertas:v1:{sku}` → JSON con la lista de ofertas tal como las devuelve el repositorio. Un SKU inexistente se guarda como marca de "no existe" con vigencia corta, para que un SKU mal escrito repetido no vaya siempre a la base.
- **Vigencia (TTL):** **60 s** para las ofertas; **10 s** para la marca de "no existe".
- **Invalidación:** explícita al recibir `ListaPreciosActualizada` ([ADR-005](ADR-005-comunicacion.md)): se borran las claves de los SKU afectados. El TTL queda como red de seguridad si se pierde un evento.
- **Comportamiento ante fallas:** la caché es **opcional**. Si Redis no responde en **50 ms** o devuelve error, se lee de PostgreSQL y se loguea; nunca se devuelve un error al consumidor por culpa de Redis. Una escritura fallida en Redis se ignora.
- **Estampida:** si muchas requests piden el mismo SKU vencido a la vez, solo una va a la base (`singleflight` por réplica).
- **Apagado:** `CACHE_HABILITADA=false` deja la capacidad leyendo directo de PostgreSQL.

## Por qué

- **Saca a PostgreSQL del camino de la mayoría de las requests**, que es lo que la vuelve un punto único de falla y el límite probable cuando crezcan los datos.
- **Guardar las ofertas y no la respuesta** mantiene la tasa de aciertos alta con cualquier `cantidad` y `limite`, y una sola clave por SKU hace que invalidar sea trivial.
- **Como decorador del puerto**, se puede apagar con una variable de entorno y medir antes y después con el mismo test de carga, que es lo que pide la consigna.
- **Fallar abierto** (ir a la base si Redis no está) evita que la caché se convierta en un punto de falla nuevo ([ADR-010](ADR-010-resiliencia.md)).

## Consecuencias aceptadas

- **El stock puede estar desactualizado hasta 60 s** si se pierde la invalidación: el ranking podría recomendar un proveedor que ya no tiene stock suficiente. Es aceptable porque la capacidad **recomienda**, no reserva; la reserva sin sobreventa la hace `stock-service` contra su base.
- **Un contenedor más** (Redis) y una dependencia más que operar.
- **La primera request de cada SKU después de una invalidación** paga la consulta a la base más la escritura en Redis.
- **No resuelve la caída de PostgreSQL** salvo que se decida servir ofertas vencidas como respaldo, que queda abierto en [ADR-010](ADR-010-resiliencia.md). Con la base caída, un SKU todavía cacheado sigue en 200 hasta que venza su TTL y uno no cacheado da `503`: se midió, pero no es un respaldo diseñado.
- **Una lectura en curso puede volver a escribir un valor viejo:** si una réplica leyó de la base antes del commit y escribe en Redis después de la invalidación, ese valor queda hasta 60 s. La réplica que invalida suelta sus lecturas en curso, pero no las de la otra. El límite es el TTL.
- **Con Redis caído cada request paga la espera de la lectura fallida:** se midieron unos 56 ms por request contra menos de 20 ms con Redis arriba. No hay un corte que deje de intentar durante un rato; a la demanda esperada no pesa. Por eso, si la lectura falló, no se intenta además escribir, que lo llevaba a 108 ms.
- **Un duplicado no invalida:** si el proceso cae entre el commit de un evento y la invalidación, la reentrega se descarta como duplicado y vale el TTL.

## Con qué lo medimos

Con el mismo script de k6 de [ADR-013](ADR-013-capacidad-y-costos.md), con `tests/carga/medir.sh` y con tres series para poder atribuir cada mejora. A: código base (`68a3670`). B: pool del gateway arreglado, `CACHE_HABILITADA=false`. C: pool y caché. Misma máquina el 06/11, con Docker Desktop con 12 CPU y 7,6 GB, otros contenedores ajenos corriendo y el generador k6 adentro. La CPU está en % de un núcleo.

| Tasa | Serie | p50 | p95 | p99 | Máx. | Errores | No lanzadas | CPU gateway | CPU cada réplica | CPU PostgreSQL | CPU Redis | Lecturas de `productos` |
|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 200 | A | 3,2 ms | 5,4 ms | 9,3 ms | 29 ms | 0 % | 0 | 41 % | 19 % | 14 % | — | 6 999 |
| 200 | B | 2,1 ms | 3,8 ms | 5,5 ms | 23 ms | 0 % | 0 | 25 % | 11 % | 8 % | — | 6 999 |
| 200 | C | 1,6 ms | 3,0 ms | 4,3 ms | 12 ms | 0 % | 0 | 25 % | 10 % | 1 % | 4 % | **30** |
| 1 000 | A | 1,9 ms | 6,3 ms | 22,2 ms | 125 ms | 0 % | 0 | 102 % | 52 % | 40 % | — | 34 999 |
| 1 000 | B | 2,2 ms | 5,3 ms | 12,7 ms | 101 ms | 0 % | 0 | 115 % | 60 % | 45 % | — | 34 999 |
| 1 000 | C | 1,7 ms | 3,9 ms | 10,8 ms | 42 ms | 0 % | 0 | 104 % | 48 % | 1 % | 12 % | **30** |
| 1 600 | A | 2,0 ms | 15,3 ms | 42,5 ms | 161 ms | 0 % | 58 | 159 % | 78 % | 63 % | — | 55 940 |
| 1 600 | B | 2,3 ms | 12,0 ms | 29,5 ms | 135 ms | 0 % | 33 | 155 % | 82 % | 70 % | — | 55 964 |
| 1 600 | C | 1,7 ms | 7,5 ms | 24,0 ms | 83 ms | 0 % | 0 | 152 % | 71 % | 1 % | 17 % | **30** |

**Consultas a PostgreSQL:** se cuentan como lecturas de la tabla `productos` (`seq_scan + idx_scan` de `pg_stat_user_tables`), que la consulta de la capacidad lee una vez por ejecución. La imagen de PostgreSQL no trae `pg_stat_statements`. Sin caché es exactamente **1 por request** y con caché **30 por corrida**, una por SKU, entre 7 000 y 56 000 requests: 0,001 por request a 1 000 y 1 600 req/s. La CPU de PostgreSQL baja de 40-70 % a **1 %**, y Redis suma 12-17 %.

**Latencia:** entre B y C, que difieren solo en la caché, el p95 baja de 3,8 a 3,0 ms a 200 req/s, de 5,3 a 3,9 ms a 1 000 y de 12,0 a 7,5 ms a 1 600. Es una corrida por tasa.

**Capacidad máxima: no medida.** Las tres series atendieron las tres tasas dentro del objetivo (p95 < 300 ms, 0 % de errores) y esta medición no pasó de 1 600 req/s, así que no hay un límite que comparar. La CPU del gateway casi no cambia con la caché (155 % → 152 % a 1 600 req/s): como se anticipó, **la caché se presenta por la reducción de consultas a la base y no como una mejora de capacidad**.

**Tasa de aciertos a la demanda esperada:** **90,8 %** (1 384 aciertos y 140 fallos, contadores `keyspace_hits` y `keyspace_misses` de Redis) a 5 req/s durante 5 min con SKU sesgados (`SKU_SESGADOS=1`: por la fórmula, los 5 primeros SKU reciben el 55,0 %; en esta corrida, 824 de 1 524 requests, el 54,1 %, según el log de las réplicas). Está por encima del 50 % que se fijó como mínimo. El sesgo casi no mueve el número: una corrida anterior que por un error del script usó SKU uniformes dio 90,2 %. A esta tasa, incluso un reparto parejo pide cada SKU unas 10 veces por TTL. El 99,6-99,9 % de las corridas de carga no dice nada, por lo que se explica arriba. No se midió con la distribución real del grupo de paneles solares, que todavía no existe.

**Comportamiento:** tests unitarios y de integración contra Redis real en el paquete (acierto sin ir a la base, marca de "no existe" con su TTL, invalidación, 40 lecturas simultáneas con una consulta, servidor colgado cortado por timeout, Redis inalcanzable). Simulacro en el sistema levantado, en [`tests/carga/resultados/2026-11-06-fallas/`](../../tests/carga/resultados/2026-11-06-fallas/): con Redis detenido la capacidad responde 200 y un SKU inexistente, 404; un evento sobre un SKU cacheado se ve en la lectura siguiente.

Evidencia: [`2026-11-06-a-base`](../../tests/carga/resultados/2026-11-06-a-base/), [`-b-pool`](../../tests/carga/resultados/2026-11-06-b-pool/), [`-c-pool-cache`](../../tests/carga/resultados/2026-11-06-c-pool-cache/) y [`-aciertos`](../../tests/carga/resultados/2026-11-06-aciertos/).

## Qué nos haría cambiar de idea

- **Si con más datos importados la base pasa a ser el límite**, la caché pasa a defender también la capacidad, y habría que medirlo a una tasa que llegue a saturar.
- **Si la tasa de aciertos a la demanda esperada queda por debajo del 50 %**, la distribución de SKU consultados es demasiado dispersa: revisar el TTL o guardar en la caché solo los SKU más pedidos.
- **Si el grupo de paneles solares necesita stock exacto**, bajar el TTL o sacar `stock_disponible` de lo cacheado y consultarlo aparte.
