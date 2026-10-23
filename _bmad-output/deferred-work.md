- source_plan: `{project-root}/_bmad-output/plan-entrega2-plataforma-observabilidad.md`
  summary: Instrumentar proveedores-service para cerrar la traza end-to-end (handler otelhttp, QueryTracer de pgx en pkg/observabilidad, logger compartido y su Dockerfile a contexto de raíz) y verificar la traza completa en Jaeger con captura.
  evidence: Se separó por tamaño: el plan completo quedaba en ~3.000 tokens, cerca del doble del rango recomendado para la ruta full. El corte cae en el límite que el usuario ya había pedido como commit aislado, y toca archivos de otro integrante. Se ejecuta inmediatamente después del plan de plataforma.

- source_plan: `{project-root}/_bmad-output/plan-entrega2-plataforma-observabilidad.md`
  summary: El gateway instrumenta sus propios /healthz y /readyz, así que Jaeger se llena de trazas de sondeo (medido: 61 de healthcheck contra 8 de la capacidad) y la UI muestra ruido antes que el flujo real.
  evidence: Real y medido contra el sistema corriendo. No lo levantó la lente de revisión; lo encontró la verificación. No se arregló acá porque el filtro de rutas no sondeables no estaba en el plan y la demo se puede dar filtrando por operación en la UI. El arreglo natural es no abrir span en conObservabilidad para /healthz y /readyz.

- source_plan: none
  summary: Escribir el ADR D11 v1 (observabilidad) y el ADR D12 (balanceo con 2 réplicas de proveedores-service detrás del gateway con health checks), parte del Integrante 2 en la Entrega 2.
  evidence: Se separó del plan de plataforma y observabilidad porque los dos ADR se mergean sin depender del código, y el plan de la Entrega 2 advierte que el revisor automático compara los ADR contra el código: escribirlos después del stack evita describir decisiones que todavía no están implementadas.
