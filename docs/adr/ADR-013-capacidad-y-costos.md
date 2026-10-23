# ADR-013 — Capacidad y costos (D13)

- **Estado:** aceptado. Test de carga ejecutado sobre el sistema real; la alternativa de escalado y la estimación de costos son propuestas para el despliegue en la nube (Entrega 3).
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D13. Resultados de las pruebas de carga, principal límite identificado, alternativa de escalado y estimación de costos.

## Contexto

La consigna (2.11) pide tests de carga con resultados documentados y analizados, y la defensa pide el **principal límite identificado**. Hasta hoy solo teníamos mediciones sueltas: 1 a 3 ms por request ([ADR-002](ADR-002-arquitectura-interna.md)) y un p95 de 4,92 ms con 23 requests ([ADR-011](ADR-011-observabilidad.md)).

Se mide la capacidad publicada, `GET /api/v1/proveedores/recomendados`, a través del gateway, con las dos réplicas y PostgreSQL, tal como la consume el grupo de paneles solares. Las trazas se exportan al 100 % a Jaeger, igual que en la configuración real.

## Opciones evaluadas

Para medir:

1. **`hey` o `ab`.** Simples, pero con modelo cerrado: si el sistema se pone lento, mandan menos requests y la saturación queda escondida en parte.
2. **k6 con modelo abierto** (`ramping-arrival-rate`). Mantiene la tasa pedida aunque el sistema se ponga lento, permite escalones y umbrales, y es la herramienta del stack del complemento.
3. **Locust.** Equivalente, pero en Python, un lenguaje más para el grupo.

Para escalar, una vez encontrado el límite:

1. **Más réplicas de `proveedores-service`** en la misma máquina.
2. **Bajar el costo por request del gateway**: ajustar su pool de conexiones y muestrear las trazas.
3. **Caché delante de PostgreSQL** ([ADR-007](ADR-007-cache.md)).
4. **Separar componentes en máquinas distintas** y, más adelante, replicar el gateway.

## Qué elegimos

**Para medir, k6 con modelo abierto.** Objetivo: **p95 < 300 ms y menos de 1 % de errores.** Aparte, una corrida solo mide lo que dice si k6 logró lanzar todas las requests; cuando no, se informan las no lanzadas. Script, forma de correrlo, entorno y tablas completas en [`tests/carga/README.md`](../../tests/carga/README.md); evidencia de cada corrida en [`tests/carga/resultados/`](../../tests/carga/resultados/README.md). Todo corrió en una notebook de 8 hilos, **k6 incluido**.

### Resultados (23/10, código de `main`)

| Tasa pedida | Atendidas | p95 | Máx. | Errores |
|---:|---:|---:|---:|---:|
| 200 req/s | 200 | 2,9 ms | 18 ms | 0 % |
| 1 000 req/s | 996 | 42,5 ms | 234 ms | 0 % |
| 1 200 req/s | 1 182 | 125 ms | 456 ms | 0 % |
| 1 600 req/s | 1 550 | 171 ms | 993 ms | 0 % |
| 2 000 req/s | 1 659 | **1,57 s** | 4,38 s | 0,003 % |

**El resultado en sobrecarga no es estable.** La primera rampa del día **colapsó en el escalón de 1 600 req/s** (p95 de 4,03 s, máximo de 15,5 s, 3,58 % de errores: unos 4 000 `504` y 84 `502`), y una corrida exploratoria a 2 000 req/s atendió solo 1 151 req/s con 1,81 % de errores. Con el mismo código, la rampa de la serie de referencia terminó sin errores.

**Capacidad declarada: 1 200 req/s sostenidas.** Todas las corridas aisladas, exploratorias y de referencia, cumplieron el objetivo de latencia y errores hasta ~1 550 req/s atendidas, pero una de las dos rampas colapsó en el escalón de 1 600; declaramos un escalón por debajo, con p95 de 62 a 125 ms según la corrida. En la serie de referencia, desde 1 000 req/s (en una corrida exploratoria, desde 800), k6 ya no logra lanzar todas las requests porque comparte la CPU con el sistema: la medición está en parte limitada por el generador.

### Principal límite identificado

**La CPU de la máquina, y dentro de ella el gateway, que es el que más gasta por request y el que colapsa.**

- **CPU media a 1 000 req/s:** gateway **139 %**, cada réplica ~68 %, PostgreSQL 54 %, Jaeger 21 % (más 81 % de k6). Son unos **3,5 ms de CPU por request**, el 40 % en el gateway, que no tiene lógica de negocio. A 2 000 req/s el gateway llega a **308 %**.
- **La causa es el transporte hacia las réplicas.** El proxy usa `http.DefaultTransport` de Go ([`proxy.go`](../../services/api-gateway/internal/gateway/proxy.go)), que guarda **solo 2 conexiones ociosas por réplica** (`MaxIdleConnsPerHost`). Con cientos de requests concurrentes, casi todas abren y cierran una conexión TCP nueva.
- **Experimento de confirmación** (con `MaxIdleConnsPerHost = 256`, sin commitear; [evidencia](../../tests/carga/resultados/2026-10-23-experimento-pool/)):

  | | `main` | Con el pool ajustado |
  |---|---:|---:|
  | CPU del gateway a 1 000 req/s | 139 % | **96 %** |
  | p95 a 1 600 req/s | 171 ms | **48 ms** |
  | 2 000 req/s pedidas: atendidas y p95 | 1 659, 1,57 s | **1 949, 120 ms** |

  Con el pool ajustado, 2 000 req/s quedan dentro del objetivo y la rampa completa baja su p95 de 76 a 21 ms. Una corrida exploratoria anterior con el mismo cambio dio una mejora menor (1 766 req/s y p95 de 601 ms a 2 000 pedidas), así que el tamaño de la mejora varía, pero el sentido se repite.
- **No es PostgreSQL:** a 1 000 req/s consume menos CPU que cada réplica.

**Memoria:** Jaeger llegó a **1,6 GB** en una sola rampa arrancando de cero. Guarda las trazas en memoria **sin tope** y exporta el 100 %, así que en una máquina que corre días crece hasta quedarse sin memoria. El resto del sistema está por debajo de 300 MB.

### Alternativa de escalado

Combina las opciones 2, 3 y 4 en este orden, porque cada paso es más barato que el siguiente y ataca lo que se midió. La opción 1 queda descartada: en la misma máquina, una tercera réplica compite por la misma CPU y no mueve el límite.

1. **Pool de conexiones del gateway acorde a la concurrencia** (una línea en `proxy.go`): menos CPU por request y sin colapso a 2 000 req/s en la medición. Es código del gateway ([ADR-012](ADR-012-balanceo-de-carga.md)); se propone como un cambio aparte, con su propio PR.
2. **Tope de memoria y muestreo para Jaeger**: `MEMORY_MAX_TRACES` y `mem_limit` en el compose, y muestreo del 10 % (`parentbased_traceidratio`).
3. **Separar el gateway, las réplicas y la base en máquinas distintas** cuando una sola no alcance.
4. **Caché de ofertas en Redis** ([ADR-007](ADR-007-cache.md)) cuando PostgreSQL pase a ser el límite; hoy no lo es.
5. **Réplicas del gateway** detrás de un balanceador de la nube; obliga a revisar el estado de salud por instancia ([ADR-012](ADR-012-balanceo-de-carga.md)).

### Estimación de costos

**Demanda esperada:** la del grupo de paneles solares en la demo y en la evaluación, del orden de **unas pocas requests por segundo**. La capacidad medida es cientos de veces mayor, así que el costo lo definen la memoria y la disponibilidad, no la CPU.

Con ~3,5 ms de CPU por request, **una VM de 2 vCPU** atiende en teoría ~570 req/s al 100 %; planificando al 60 %, unas **340 req/s**. Memoria: el sistema usa menos de 300 MB, y Jaeger necesita un tope (por ejemplo 1 GB) → **4 GB** con margen.

| Opción | Qué incluye | Costo mensual de referencia |
|---|---|---|
| **A. Una VM con `docker compose`** (2 vCPU, 4 GB) | todo el compose tal cual, con Postgres y su volumen en la VM | Hetzner CX22 ≈ **4 €**; DigitalOcean ≈ **24 USD**; AWS `t3.medium` ≈ **30 USD** más el disco |
| **B. VM más PostgreSQL administrado** | backups y reinicio automático de la base | **15 a 30 USD más** que la opción A |
| **C. Capa gratuita** (Oracle Cloud Always Free, créditos de estudiante) | igual que A, con los límites de la cuenta | **0 USD**, con riesgo de que suspendan la instancia |

Son precios de lista de referencia; **se verifican al contratar** en la Entrega 3. **Recomendamos la opción A, con el tope de memoria de Jaeger aplicado**: el compose ya es el procedimiento único de la consigna (2.12), así que se despliega igual que en local, y la demanda esperada no justifica una base administrada.

## Por qué

- **k6 con modelo abierto** sostiene la tasa pedida y por eso deja ver el colapso; un modelo cerrado habría bajado la tasa por su cuenta y escondido parte de la saturación.
- **El orden de escalado sigue a la evidencia:** el recurso que se termina es la CPU, el que más la consume es el gateway, y el experimento muestra que su pool es lo que más rinde arreglar.
- **Una sola VM** alcanza con mucho margen para la demanda real y mantiene el despliegue igual al entorno local.

## Consecuencias aceptadas

- **La medición está contaminada por k6**, que corre en la misma máquina y usa más de un núcleo a partir de 1 400 req/s. La capacidad con el generador en otra máquina sería algo mayor, y la de una VM de 2 vCPU, bastante menor.
- **Es un solo flujo con datos chicos** (30 productos, 170 ofertas): PostgreSQL tiene todo en memoria. Con miles de ofertas por SKU, el límite podría pasar a la base ([ADR-003](ADR-003-persistencia.md)).
- **Una corrida por tasa, sin repeticiones sistemáticas.** La variación entre las dos rampas y entre las corridas exploratorias y las de referencia muestra que, en sobrecarga, los números tienen mucho ruido. Por eso declaramos 1 200 req/s y no 1 550.
- **El colapso bajo sobrecarga sigue en `main`** hasta que se aplique el ajuste del pool del gateway.
- **Una sola VM es un punto único de falla** para todo el sistema, como ya lo es la base en local.

## Con qué lo medimos

Este ADR **es** la medición: tablas de arriba, [`tests/carga/README.md`](../../tests/carga/README.md) y la evidencia en [`tests/carga/resultados/`](../../tests/carga/resultados/README.md). Se repite con `tests/carga/medir.sh`:

- después de aplicar el ajuste del pool del gateway;
- en la Entrega 3, con la caché apagada y prendida ([ADR-007](ADR-007-cache.md));
- contra el despliegue en la nube, desde una máquina distinta.

## Qué nos haría cambiar de idea

- **Si en la nube el p95 a la demanda real supera los 300 ms**, el problema no es de capacidad sino de red o del tamaño de la VM: medir antes de escalar.
- **Si la mediana de punta a punta deja de estar en ~2 ms** a medida que crecen los datos, el límite pasa a la base y la caché sube al primer lugar de la lista.
- **Si con el pool ajustado la sobrecarga vuelve a terminar en `504`**, el siguiente paso es un límite de concurrencia en el gateway ([ADR-010](ADR-010-resiliencia.md)).
- **Si el grupo de paneles solares anuncia una demanda de cientos de req/s**, pasar a la opción B de costos (VM más PostgreSQL administrado) y separar el gateway de las réplicas.
