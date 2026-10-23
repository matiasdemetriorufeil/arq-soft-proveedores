# ADR-013 — Capacidad y costos (D13)

- **Estado:** aceptado. Test de carga ejecutado sobre el sistema real; la alternativa de escalado y la estimación de costos son propuestas para el despliegue en la nube (Entrega 3).
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D13. Resultados de las pruebas de carga, principal límite identificado, alternativa de escalado y estimación de costos.

## Contexto

La consigna (2.11) pide tests de carga con resultados documentados y analizados, y la defensa pide el **principal límite identificado**. Hasta hoy solo teníamos mediciones sueltas: 1 a 3 ms por request ([ADR-002](ADR-002-arquitectura-interna.md)) y un p95 de 4,92 ms con 23 requests ([ADR-011](ADR-011-observabilidad.md)).

Lo que se mide es la capacidad publicada, `GET /api/v1/proveedores/recomendados`, a través del gateway, con las dos réplicas y PostgreSQL, tal como la consume el grupo de paneles solares. Las trazas se exportan al 100 % a Jaeger, igual que en la configuración real.

## Opciones evaluadas

Para medir:

1. **`hey` o `ab`.** Simples, pero con modelo cerrado: si el sistema se pone lento, mandan menos requests y esconden la saturación.
2. **k6 con modelo abierto** (`ramping-arrival-rate`). Mantiene la tasa pedida aunque el sistema se ponga lento, permite escalones y umbrales, y es la herramienta del stack del complemento.
3. **Locust.** Equivalente, pero en Python, un lenguaje más para el grupo.

Para escalar, una vez encontrado el límite:

1. **Más réplicas de `proveedores-service`** en la misma máquina.
2. **Arreglar el pool de conexiones del gateway** y separar los componentes en máquinas distintas.
3. **Caché delante de PostgreSQL** ([ADR-007](ADR-007-cache.md)).

## Qué elegimos

**k6 con modelo abierto**, script en [`tests/carga/recomendados.js`](../../tests/carga/recomendados.js). Corre en un contenedor dentro de la red del compose. Objetivo: **p95 < 300 ms y menos de 1 % de errores**. SKU al azar entre los 30 del seed y `cantidad` entre 1 y 50.

### Resultados (23/10)

Notebook Ryzen 5 7235HS (8 hilos), Docker con 8 CPU y 7,6 GB. **Todo en la misma máquina, k6 incluido.** Una corrida de 35 s por tasa. Tabla completa en [`tests/carga/README.md`](../../tests/carga/README.md).

| Tasa pedida | Atendidas | p95 | p99 | Errores |
|---:|---:|---:|---:|---:|
| 200 req/s | 200 | 4,1 ms | 13,7 ms | 0 % |
| 600 req/s | 600 | 9,5 ms | 27,0 ms | 0 % |
| 1000 req/s | 998 | 49,8 ms | 95,0 ms | 0 % |
| 1400 req/s | 1389 | 50,0 ms | 113 ms | 0 % |
| 1600 req/s | 1543 | 166 ms | 396 ms | 0 % |
| 2000 req/s | **1151** | **3,16 s** | 3,96 s | **1,81 %** |

**Capacidad: unas 1 500 req/s sostenidas dentro del objetivo.** Por encima de ~1 600 req/s el sistema no solo deja de crecer, sino que **colapsa**: a 2 000 req/s pedidas atiende menos (1 151) que a 1 600, con `504 NO_DISPONIBLE` del gateway.

### Principal límite identificado

**La CPU de la máquina, y dentro de ella el gateway, que es el componente que más consume por request y el que colapsa.**

- **CPU por contenedor a 1 000 req/s** (`docker stats`): gateway **142 %**, cada réplica ~58 %, PostgreSQL 42 %, Jaeger 4 %. Sumando, unos **3 ms de CPU por request**, casi la mitad en el gateway, que no tiene lógica de negocio. A 2 000 req/s el total supera los 5 núcleos más lo que usa k6: la máquina se queda sin CPU.
- **Por qué el gateway gasta tanto:** el proxy usa `http.DefaultTransport` de Go ([`proxy.go`](../../services/api-gateway/internal/gateway/proxy.go)), que guarda **solo 2 conexiones ociosas por réplica** (`MaxIdleConnsPerHost`). Con cientos de requests concurrentes, casi todas abren y cierran una conexión TCP nueva hacia la réplica. Bajo sobrecarga eso termina en timeouts y en `504`.
- **Experimento de confirmación** (local, sin commitear): con `MaxIdleConnsPerHost = 256`, a 2 000 req/s pedidas el sistema atendió **1 766 req/s con 0 % de errores y p95 de 601 ms**, contra 1 151 req/s, 1,81 % de errores y p95 de 3,16 s sin el cambio. A 1 000 req/s el p95 bajó de 49,8 a 27,2 ms. **El techo (~1 550 req/s) no se movió**, porque lo que se termina es la CPU compartida; lo que cambia es que, sin el arreglo, la sobrecarga colapsa en errores en lugar de solo agregar latencia.
- **No es PostgreSQL:** la mediana de punta a punta (gateway, réplica y consulta) se mantiene en ~2 ms hasta 1 400 req/s, y la base no pasó de ~1,8 núcleos de 8 en ningún momento.

**Memoria pico** durante la rampa: Jaeger **1,2 GB** (guarda las trazas en memoria y exporta el 100 %), gateway 286 MB, PostgreSQL 88 MB, cada réplica ~50 MB.

### Alternativa de escalado

En este orden, porque cada paso es más barato que el siguiente:

1. **Pool de conexiones del gateway** (`MaxIdleConnsPerHost` acorde a la concurrencia esperada): una línea, quita el colapso por sobrecarga. Es código del gateway ([ADR-012](ADR-012-balanceo-de-carga.md)); se propone como cambio aparte.
2. **Muestreo de trazas** (por ejemplo 10 % con `parentbased_traceidratio`) y límite de memoria para Jaeger: es lo que más memoria usa y crece con el tráfico.
3. **Separar los componentes en máquinas distintas** antes que sumar réplicas: en la misma máquina, una tercera réplica compite por la misma CPU y no mueve el techo.
4. **Caché de ofertas en Redis** ([ADR-007](ADR-007-cache.md)) cuando PostgreSQL pase a ser el límite; hoy no lo es.
5. **Réplicas del gateway** detrás de un balanceador de la nube, cuando un solo gateway no alcance; obliga a revisar el estado de salud por instancia ([ADR-012](ADR-012-balanceo-de-carga.md)).

### Estimación de costos

**Demanda esperada:** la del grupo de paneles solares en la demo y la evaluación, del orden de **unas pocas requests por segundo**. La capacidad medida es cientos de veces mayor, así que el costo lo define la memoria y la disponibilidad, no la CPU.

Con ~3 ms de CPU por request, **una VM de 2 vCPU** atiende en teoría ~650 req/s al 100 %; planificando al 60 %, unas **400 req/s**. Memoria necesaria: ~1 GB para el sistema y hasta 1,2 GB para Jaeger sin muestreo → **4 GB**.

| Opción | Qué incluye | Costo mensual de referencia |
|---|---|---|
| **A. Una VM con `docker compose`** (2 vCPU, 4 GB) | todo el compose tal cual; Postgres en la VM con su volumen | Hetzner CX22 ≈ **4 €**; DigitalOcean ≈ **24 USD**; AWS `t3.medium` ≈ **30 USD** más disco |
| B. VM + PostgreSQL administrado | backups y reinicio automático de la base | **+15 a 30 USD** sobre la opción A |
| C. Capa gratuita (Oracle Cloud Always Free, créditos de estudiante) | igual que A, con límites de cuenta | **0 USD**, con riesgo de que suspendan la instancia |

Son precios de lista de referencia; **se verifican al contratar** en la Entrega 3. **Recomendamos la opción A**: el compose ya es el procedimiento único de la consigna (2.12), así que el despliegue es el mismo que en local, y la demanda esperada no justifica una base administrada.

## Por qué

- **k6 con modelo abierto** es la única de las opciones que muestra el colapso: con un modelo cerrado, el 2 000 req/s se habría visto como "el sistema llega a 1 150" sin errores.
- **El orden de escalado sigue a la evidencia:** el recurso que se termina es la CPU y el que más la consume es el gateway, así que sumar réplicas del servicio o una caché no mueven el número hoy.
- **Una sola VM** alcanza con mucho margen para la demanda real, y mantiene el despliegue igual al entorno local.

## Consecuencias aceptadas

- **La medición está contaminada por k6**, que corre en la misma máquina y usa CPU. La capacidad real con el generador en otra máquina sería algo mayor; la de una VM de 2 vCPU, bastante menor.
- **Es un solo flujo y datos chicos** (30 productos, 170 ofertas): PostgreSQL tiene todo en memoria. Con miles de ofertas por SKU el límite podría pasar a la base ([ADR-003](ADR-003-persistencia.md)).
- **Una corrida de 35 s por tasa**, sin repeticiones: los números tienen ruido; por ejemplo, 1 400 req/s dio mejor p95 que 1 200.
- **Una sola VM es un punto único de falla** para todo el sistema, y también lo era la base en local.
- **El colapso bajo sobrecarga sigue en `main`** hasta que se aplique el ajuste del pool del gateway.

## Con qué lo medimos

Este ADR **es** la medición: tabla de arriba y [`tests/carga/README.md`](../../tests/carga/README.md). Se repite con el mismo script:

- en la Entrega 3, con la caché apagada y prendida ([ADR-007](ADR-007-cache.md));
- después de aplicar el ajuste del pool del gateway;
- contra el despliegue en la nube, desde una máquina distinta.

## Qué nos haría cambiar de idea

- **Si en la nube el p95 a la demanda real supera los 300 ms**, el problema no es de capacidad sino de red o del tamaño de la VM: medir antes de escalar.
- **Si la mediana de punta a punta deja de estar en ~2 ms** a medida que crecen los datos, el límite pasa a la base y la caché sube al primer lugar de la lista.
- **Si el grupo de paneles solares anuncia una demanda de cientos de req/s**, pasar a la opción B y separar el gateway de las réplicas.
