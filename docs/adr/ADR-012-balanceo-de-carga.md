# ADR-012 — Balanceo de carga (D12)

- **Estado:** aceptado, implementado y verificado
- **Fecha:** 2026-10-22
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D12. Servicio seleccionado, estrategia de distribución, verificación de disponibilidad y comportamiento ante la caída de una instancia.

## Contexto

La consigna (2.3) exige balanceo de carga sobre al menos un servicio desplegado en dos o más instancias, que el mecanismo **reconozca cuándo una instancia no está disponible**, y que la distribución y el estado **puedan comprobarse con la información de observabilidad** del sistema.

**El servicio elegido es `proveedores-service`**, por dos razones: es el único que existe hoy, y es el que consume otro grupo, así que es el que no puede caerse por un reinicio o un despliegue. Es además un buen candidato: sus requests son `GET` sin efectos y no guarda estado en memoria, así que cualquier instancia puede atender cualquier request.

## Opciones evaluadas

1. **`deploy.replicas` de compose y DNS round-robin de Docker.** Es lo más corto de escribir, pero no funciona para nosotros: con un solo nombre DNS, el `http.Client` de Go se queda con la primera dirección que resuelve y mandaría todo a la misma réplica. Sería un balanceo de mentira, imposible de demostrar.
2. **Un balanceador dedicado (nginx, Traefik, HAProxy).** Es la respuesta estándar y la más robusta, pero agrega un componente y un lenguaje de configuración más, cuando ya tenemos un proceso propio en el camino de todas las requests.
3. **Round-robin en nuestro `api-gateway`.** El gateway ya es la entrada única obligatoria (2.1) y ya ve todo el tráfico: el reparto es un agregado chico sobre algo que existe.
4. **Balanceo del lado del cliente.** Imposible: al grupo de paneles solares le publicamos una sola URL, y la consigna (2.1) pide entrada única.

## Qué elegimos

**La opción 3: round-robin en el gateway sobre dos réplicas nombradas.**

- El compose declara `proveedores-service-1` y `proveedores-service-2`, **dos servicios con nombre y la misma imagen**, no `deploy.replicas`: el gateway necesita dos nombres DNS distintos para repartir de verdad.
- `UPSTREAMS_PROVEEDORES` es la lista de réplicas. Agregar una tercera es una línea en el compose y una entrada en esa variable; no hay nada compilado.
- **Estrategia de distribución:** round-robin estricto. El turno avanza siempre, también cuando la que toca está caída, para que el reparto entre las que quedan siga siendo parejo.
- **Verificación de disponibilidad:** sondeo activo de `GET /healthz` de cada réplica cada `INTERVALO_SALUD` (5 s por defecto). Arranca dándolas por caídas y hace una primera vuelta **antes** de empezar a escuchar, así `/readyz` del gateway nunca dice que está listo sin haberlo comprobado.
- **Comportamiento ante una caída:** si falla la conexión, la réplica queda marcada fuera del reparto en el acto —sin esperar al próximo sondeo— y el `GET` se reintenta **una sola vez** en otra sana; repetirlo es seguro porque no tiene efectos. El sondeo la devuelve al reparto cuando vuelve a responder. Si no queda ninguna, el gateway devuelve `502 NO_DISPONIBLE` en `problem+json` y su `/readyz` pasa a 503.
- **Un timeout no marca la réplica como caída**: está lenta, no muerta, y sacarla del reparto sería castigar a la única que quizá queda. Devuelve `504 NO_DISPONIBLE`.
- **Observabilidad del reparto:** el span del gateway lleva el atributo `upstream` con la réplica que atendió, y las transiciones se loguean una vez, al cambiar de estado, con el `trace_id` y el `request_id` de la request que las disparó.

Implementación en [`services/api-gateway/internal/gateway/balanceador.go`](../../services/api-gateway/internal/gateway/balanceador.go).

## Por qué

- **Es un balanceo que se puede demostrar**, que es lo que pide la defensa: el reparto se ve en los logs de cada réplica y en el atributo `upstream` de los spans.
- **Reconoce la caída por dos vías independientes**: el sondeo periódico (aunque no haya tráfico) y el fallo de conexión de una request real (sin esperar al sondeo).
- **No agrega un componente al `docker compose up`**, que la consigna 2.12 exige que levante todo con un comando.
- **La recuperación es automática.** No hay que reiniciar el gateway cuando una réplica vuelve.

## Consecuencias aceptadas

- **El round-robin ignora la carga y la latencia.** Con réplicas idénticas sobre la misma base es razonable; si una quedara más lenta, seguiría recibiendo la mitad del tráfico.
- **El estado de salud es por instancia de gateway.** Hoy el gateway es uno solo. Si se replicara, cada uno tendría su propia visión y las transiciones podrían diferir.
- **La detección sin tráfico tarda hasta un intervalo de sondeo** (medido: 4,8 s). En ese lapso, la primera request que caiga en la réplica muerta paga el reintento.
- **Una réplica lenta sigue en el reparto** por decisión explícita: cada request que le toque paga el `TIMEOUT_UPSTREAM` antes del 504.
- **Las dos réplicas comparten una sola base.** El balanceo cubre el servicio, no el almacenamiento: PostgreSQL sigue siendo un punto único de falla, y eso ningún round-robin lo arregla.

## Con qué lo medimos

Mediciones del 22/10 con el sistema levantado por `docker compose up`:

- **Distribución:** 23 requests de capacidad repartidas **12 / 11** entre las dos réplicas, contadas en los logs de cada contenedor. El atributo `upstream` de los spans muestra el mismo reparto en Jaeger.
- **Detección de la caída, sin tráfico:** `docker compose stop proveedores-service-1` → la línea `upstream caído: deja de recibir requests` aparece a los **4,8 s**, consistente con el intervalo de sondeo de 5 s.
- **Comportamiento durante la caída:** con una réplica detenida, **4 de 4 requests respondieron 200** por la que quedó viva, y el `/readyz` del gateway siguió en 200.
- **Recuperación:** al volver a levantarla, vuelve al reparto en **2,7 s**, sin intervención.
- **Caída total:** con las dos detenidas, `502 NO_DISPONIBLE` en `problem+json` y `/readyz` del gateway en 503, mientras `/healthz` sigue en 200 porque el proceso está sano.

## Qué nos haría cambiar de idea

- **Si replicamos el gateway**, el estado de salud por instancia deja de alcanzar: ahí sí conviene un balanceador dedicado (opción 2) o un chequeo compartido.
- **Si las réplicas dejan de ser equivalentes** —distinto hardware, o una con caché caliente— pasar a least-connections o a una estrategia con latencia observada.
- **Si la prueba de carga (D13) muestra que el límite es PostgreSQL** y no el servicio, sumar réplicas no va a mover el número: el problema pasa a ser de la base, no del reparto.
- **Si aparece un endpoint con efectos** (una escritura), el reintento automático deja de ser seguro y hay que limitarlo a los `GET`.
