- source_plan: `{project-root}/_bmad-output/plan-entrega2-plataforma-observabilidad.md`
  summary: Instrumentar proveedores-service para cerrar la traza end-to-end (handler otelhttp, QueryTracer de pgx en pkg/observabilidad, logger compartido y su Dockerfile a contexto de raíz) y verificar la traza completa en Jaeger con captura.
  evidence: Se separó por tamaño: el plan completo quedaba en ~3.000 tokens, cerca del doble del rango recomendado para la ruta full. El corte cae en el límite que el usuario ya había pedido como commit aislado, y toca archivos de otro integrante. Se ejecuta inmediatamente después del plan de plataforma.

- source_plan: `{project-root}/_bmad-output/plan-entrega2-plataforma-observabilidad.md`
  summary: El gateway instrumenta sus propios /healthz y /readyz, así que Jaeger se llena de trazas de sondeo (medido: 61 de healthcheck contra 8 de la capacidad) y la UI muestra ruido antes que el flujo real.
  evidence: Real y medido contra el sistema corriendo. No lo levantó la lente de revisión; lo encontró la verificación. No se arregló acá porque el filtro de rutas no sondeables no estaba en el plan y la demo se puede dar filtrando por operación en la UI. El arreglo natural es no abrir span en conObservabilidad para /healthz y /readyz.

- source_plan: none
  summary: Escribir el ADR D11 v1 (observabilidad) y el ADR D12 (balanceo con 2 réplicas de proveedores-service detrás del gateway con health checks), parte del Integrante 2 en la Entrega 2.
  evidence: Se separó del plan de plataforma y observabilidad porque los dos ADR se mergean sin depender del código, y el plan de la Entrega 2 advierte que el revisor automático compara los ADR contra el código: escribirlos después del stack evita describir decisiones que todavía no están implementadas.

- source_plan: none
  summary: Reposición automática en stock-service: punto de reorden que genera la orden en BORRADOR, una sola orden abierta por producto, máquina de estados BORRADOR → SOLICITADA → CONFIRMADA/VENCIDA/EN_REVISION, y consulta a proveedores-service (GET /api/v1/proveedores/recomendados, timeout 1 s y 2 reintentos) por el proveedor de menor costo total. RN-14, RN-15, RN-16; CA-14.1, CA-14.2, CA-14.3.
  evidence: Objetivo B de los tres que detectó el control de alcance en la parte del Integrante 3 de la Entrega 3. Es un entregable aparte —se revisa y mergea solo— pero necesita que exista el servicio del objetivo A, así que se ejecuta inmediatamente después. Se separó porque un plan único para A+B+C queda muy por encima del rango de 900-1600 tokens y el riesgo se concentra en la reserva transaccional de A.

- source_plan: none
  status: hecho el 06/11 (rama feat/stock-service). Lo que quedó afuera está en la sección "Pendiente" de ADR-009.
  summary: Pedidos a FAV desde stock-service: cliente HTTP con Idempotency-Key contra POST /v1/pedidos y GET /v1/pedidos/{id}, receptor del webhook con firma HMAC directo al servicio y fuera del gateway (ADR-009), y los tres endpoints nuevos en el mock de FAV (services/import-service/internal/favmock), que hoy solo sirve el catálogo.
  evidence: Objetivo C de los tres que detectó el control de alcance en la parte del Integrante 3 de la Entrega 3. Depende de las órdenes de reposición del objetivo B. Además toca el mock de otro integrante, cosa que el plan de la Entrega 3 asigna explícitamente al Integrante 3.

- source_plan: `{project-root}/_bmad-output/plan-hu-13-stock-service-reserva.md`
  summary: El .env de la máquina quedó 15 variables atrás del .env.example (faltan STOCK_DB, STOCK_DB_USER, STOCK_DB_PASSWORD y las de RabbitMQ, Mongo, Redis, Meilisearch y FAV), así que `docker compose config` aborta en la interpolación y `docker compose up` no levanta ningún servicio. Además hace falta un `docker compose down -v` una sola vez, porque el script que crea la base `stock` solo corre con el volumen de Postgres vacío.
  evidence: Verificado el 06/11: `docker compose ps` falla con "required variable STOCK_DB is missing a value". Preexistente, lo introdujo la rama feat/import-service al sumar servicios al compose; no lo causó el trabajo de stock-service. No se puede arreglar desde el repo porque .env está en .gitignore: lo tiene que actualizar cada integrante en su máquina copiando las variables nuevas de .env.example. Bloquea la demo de la Entrega 3, que arranca con `docker compose up`.
