# ADR-009 — Consumo del proveedor: catálogo de ropa (D9)

- **Estado:** **propuesto**. No hay código que consuma al grupo de ropa y **todavía no tenemos el link a su contrato publicado**; los nombres de campos de abajo son supuestos y se corrigen cuando lo tengamos. El consumo real corresponde a la Entrega 3 (6/11). Falta para cerrarlo:
  - el link a su contrato publicado y la versión vigente;
  - ajustar el mapeo de campos y los valores por defecto a ese contrato;
  - un entorno accesible (URL y credenciales) para el test de contrato.
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D9. Incorporación de la capacidad externa, adaptación al contrato y comportamiento ante errores, cambios o indisponibilidad.

## Contexto

Consumimos el **catálogo de ropa** de otro grupo. Según lo acordado con la cátedra, la tienda de ropa funciona como **un proveedor más**: sus productos y precios entran a la comparación y se contabiliza el stock de lo que le compramos.

La consigna (4.1) y el complemento ponen condiciones:

- la capacidad tiene que formar parte de un **flujo importante del negocio**; una llamada aislada o una integración sin consecuencias no cuenta;
- la llamada sale **desde el microservicio responsable**, no desde el frontend ni a través de nuestro gateway;
- se trabaja **solo con su contrato publicado**; las dudas se resuelven con un issue en su repo;
- son obligatorios el **test de contrato**, los **timeouts**, el manejo de errores y **qué ve el usuario si el proveedor se cae**, demostrado en el Game Day;
- si el grupo abandona, la cátedra levanta un mock.

## Opciones evaluadas

1. **Llamarlos en cada request de la capacidad.** Siempre datos frescos, pero nuestra disponibilidad pasa a depender de la suya: si se caen, el grupo de paneles solares recibe errores por un tercero que no conoce. Además suma su latencia a cada request.
2. **Sincronización periódica a nuestra base (pull).** `import-service` trae su catálogo cada cierto tiempo, lo traduce a nuestro modelo y lo guarda como ofertas de un proveedor más. Su caída solo congela sus precios.
3. **Que nos notifiquen cambios (push / webhook).** Lo más fresco y eficiente, pero depende de que su contrato lo ofrezca; no podemos imponérselo.

## Qué elegimos

**La opción 2, con una capa anticorrupción en `import-service`**, y la opción 3 si su contrato ofrece notificaciones.

- **Responsable:** `import-service` ([ADR-001](ADR-001-limites-de-los-servicios.md)). Ningún otro servicio conoce el formato del grupo de ropa.
- **Capa anticorrupción:** un adaptador `catalogoropa` que llama a su API, valida la respuesta contra su contrato y la traduce a nuestro modelo: cada prenda pasa a ser un `producto` (con un SKU nuestro mapeado a su código) y una `oferta` del proveedor "Tienda de ropa" con precio, stock, descuento y flete. Lo que su contrato no tenga (por ejemplo, flete o descuento por volumen) se completa con valores por defecto documentados.
- **Frecuencia:** cada **5 minutos**, y al arrancar. Tras importar, se publica `ListaPreciosActualizada` ([ADR-005](ADR-005-comunicacion.md)), que actualiza el ranking, la caché y la búsqueda.
- **Llamada saliente:** timeout de **2 s** por request; hasta **2 reintentos** con espera exponencial solo ante errores de red, `429` o `5xx`, y respetando `Retry-After`; nunca ante `4xx`. Circuit breaker según [ADR-010](ADR-010-resiliencia.md) (abre con 5 fallas seguidas, 30 s abierto). Credenciales en `.env`, nunca en el repo.
- **Si no responden:** se conserva la **última importación válida**. Las ofertas de ropa guardan `actualizado_en`; si la última sincronización exitosa tiene **más de 24 horas**, se excluyen del ranking para no recomendar precios viejos.
- **Qué ve el usuario:** el ranking sigue funcionando con los demás proveedores. Si las ofertas de ropa están desactualizadas o excluidas, el frontend lo indica con un aviso ("precios de Tienda de ropa sin actualizar desde HH:MM"). Para eso la respuesta tiene que exponer la fecha de actualización de cada oferta, que **hoy el contrato v1 no trae**: hay que agregar `actualizado_en` por oferta, un cambio compatible que se registra en [ADR-008](ADR-008-contrato-propio.md) cuando se implemente. La capacidad que publicamos no falla por su caída.
- **Test de contrato y cambios en su API:** validamos el esquema de sus respuestas reales contra su OpenAPI publicado y probamos el adaptador contra un servidor falso con los ejemplos de su contrato. Si una respuesta no valida, el adaptador la rechaza, registra el error con el `trace_id`, **no pisa** los datos buenos que ya teníamos y abrimos un issue en su repo.

## Por qué

- **Su indisponibilidad no puede convertirse en la nuestra.** Con pull periódico, una caída suya congela sus precios pero no nos tira la capacidad publicada ni suma su latencia a nuestras requests.
- **Entra al flujo principal con consecuencias de negocio**: sus prendas compiten en el ranking y el stock que les compramos lo lleva `stock-service`, que es lo que la consigna pide para que la integración cuente.
- **La capa anticorrupción aísla su modelo del nuestro**: si cambian un nombre de campo, se toca un adaptador y no el dominio.
- **Conservar la última versión buena** es la degradación más útil para un comparador de precios: un precio de hace una hora es mejor que ninguno, uno de hace días no.

## Consecuencias aceptadas

- **Sus precios pueden estar hasta 5 minutos desactualizados** en condiciones normales, y hasta 24 horas si se caen.
- **Depende de `import-service`, que no existe todavía.** Hasta que exista, la integración no cuenta para la consigna.
- **La traducción de modelos puede perder información** (por ejemplo, talles o colores que no existen en nuestro modelo de producto) o inventar valores por defecto (flete) que no son del proveedor.
- **Rubro mezclado:** el comparador hoy tiene productos de energía solar y va a sumar prendas. El ranking es por SKU, así que no se mezclan entre sí, pero la búsqueda ([ADR-006](ADR-006-busqueda.md)) va a necesitar filtrar por categoría.

## Con qué lo medimos

Todavía no hay mediciones. Cuando esté implementado:

- **Tasa de éxito** de las sincronizaciones y **antigüedad** de la última exitosa (objetivo < 10 min).
- **Latencia p95** de sus respuestas medida desde `import-service`.
- **Caída simulada en el Game Day:** con su API cortada, la capacidad publicada sigue en `200` y el ranking sigue incluyendo sus últimas ofertas válidas.
- **Test de contrato** en verde contra la versión publicada de su contrato.

## Qué nos haría cambiar de idea

- **Si su contrato ofrece webhooks o eventos**, pasar a la opción 3 y dejar el pull como respaldo.
- **Si su catálogo es tan grande que bajarlo completo cada 5 min es caro**, pedir paginación incremental por fecha de modificación o bajar la frecuencia.
- **Si su API resulta inestable** (muchas aperturas del circuit breaker), alargar el intervalo y avisarles por issue con la evidencia.
- **Si la cátedra considera que el pull periódico no es "consumo en el flujo principal"**, consultar su catálogo en el momento para el detalle de producto, manteniendo el ranking sobre la copia local.
