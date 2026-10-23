# ADR-009 — Consumo del proveedor: catálogo y pedidos de FAV (D9)

- **Estado:** **propuesto**. El diseño está hecho sobre el contrato publicado por FAV, pero todavía no hay código que lo consuma: los servicios responsables (`import-service` y `stock-service`) no existen. El consumo real corresponde a la Entrega 3 (6/11). Falta que FAV defina su URL desplegada y cómo entrega la clave y el secreto de avisos (marcados **[?]** en su contrato).
- **Fecha:** 2026-10-23
- **Entrega:** 2 (23/10)
- **Decisión de la consigna:** D9. Incorporación de la capacidad externa, adaptación al contrato y comportamiento ante errores, cambios o indisponibilidad.

## Contexto

Consumimos la capacidad de **FAV, una tienda de ropa**: [contrato `catalogo-pedidos` v1.1.0](../contracts/consumidos/fav-catalogo-pedidos-v1.1.0.openapi.yaml) (OpenAPI 3.0.3, 15/10/2026), con un mock que responde el contrato y envía avisos firmados. Según lo acordado con la cátedra, FAV funciona como **un proveedor más** de nuestro comparador, y llevamos el stock de lo que le compramos.

Lo que ofrece el contrato, y lo que condiciona el diseño:

| Operación | Qué da | Qué no da |
|---|---|---|
| `GET /v1/productos` | todo el catálogo en una respuesta (cientos de productos), con variantes por talle y color y el precio vigente por unidad | **stock**, flete, descuento por volumen, fecha de cambio de precio, paginación |
| `POST /v1/pedidos` | compra todo o nada; aparta el stock 48 h, pendiente de pago por transferencia; exige `Idempotency-Key`; hasta 20 variantes y **10 unidades por variante** **[propuesta de FAV]** | — |
| `GET /v1/pedidos/{pedidoId}` | el estado vigente: `PENDIENTE_DE_PAGO`, `PAGADO`, `VENCIDO` o `A_VERIFICAR` | — |
| Webhook `pedidoActualizado` | aviso de cada cambio de estado, firmado con HMAC-SHA256, entrega al menos una vez, con reintentos durante una hora | garantía de entrega: agotados los reintentos, el aviso se descarta |

La consigna (4.1) y el complemento piden que la capacidad entre en un **flujo importante del negocio** con consecuencias, que la llamada salga **del servicio responsable** (no del frontend ni a través de nuestro gateway), que trabajemos **solo con el contrato publicado**, con test de contrato y timeouts, y que definamos **qué ve el usuario si el proveedor se cae**.

## Opciones evaluadas

Para el catálogo:

1. **Consultar `GET /v1/productos` en cada request de nuestra capacidad.** Precios siempre frescos, pero nuestra disponibilidad pasaría a depender de la de FAV, y cada consulta bajaría el catálogo completo.
2. **Sincronización periódica a nuestra base.** `import-service` baja el catálogo cada cierto tiempo, lo traduce y lo guarda como ofertas de un proveedor más. Una caída de FAV solo congela sus precios.

Para comprar:

1. **Solo consultar** el catálogo, sin pedidos. Simple, pero la integración no tendría consecuencias sobre el stock, que es justo lo que la consigna pide para que cuente.
2. **Pedidos reales con seguimiento por consulta periódica** de cada pedido abierto.
3. **Pedidos reales con el webhook de FAV**, más una consulta periódica de respaldo por si se pierde un aviso.

## Qué elegimos

**Catálogo: la opción 2. Pedidos: la opción 3.** Cada uso sale del servicio dueño de esa responsabilidad ([ADR-001](ADR-001-limites-de-los-servicios.md)), y **ninguna comunicación con FAV pasa por nuestro gateway, en ninguna dirección**: la consigna (4.1) pide que no se canalice por el punto de entrada propio.

### Catálogo → ranking (`import-service`)

- **Capa anticorrupción:** un adaptador `fav` traduce el catálogo a nuestro modelo. Cada **variante** es un producto nuestro (por ejemplo `FAV-<varianteId>`, con talle y color en el nombre), y FAV es el proveedor de esa oferta. La publicación pasa por `ListaPreciosActualizada` ([ADR-005](ADR-005-comunicacion.md)), que actualiza las ofertas de `proveedores-service`, la caché y la búsqueda.
- **Lo que el contrato no trae se completa con reglas explícitas:**

  | Campo nuestro | Valor para FAV | Por qué |
  |---|---|---|
  | `precio_unitario` | `variante.precio` | ya trae la promoción aplicada |
  | `descuento_pct` | 0 | el contrato no tiene descuento por volumen |
  | `flete` | **[propuesta]** valor fijo configurable, hasta acordarlo con FAV | el contrato no lo informa; la entrega se coordina por contacto |
  | `stock_disponible` | **10** (el máximo por variante de un pedido) | el contrato no informa stock: la oferta vale hasta lo que se puede pedir de una vez, y el stock real lo confirma el pedido |

- **Frecuencia:** cada **5 minutos** y al arrancar. Se compara con la última versión guardada y solo se publican los cambios.
- **Si FAV no responde:** se conserva la **última importación válida**. Si la última sincronización exitosa tiene **más de 24 horas**, sus ofertas se excluyen del ranking para no recomendar precios viejos.

### Reposición → pedidos (`stock-service`)

Cuando el stock propio de una prenda de FAV cae bajo el punto de reorden, `stock-service` usa la orden de reposición que propone [ADR-002](ADR-002-arquitectura-interna.md) (`BORRADOR → SOLICITADA → CONFIRMADA`), con dos estados más, `VENCIDA` y `EN_REVISION`, para los desenlaces que agrega el contrato de FAV (la regla RN-15 de [`SPEC.md`](../../SPEC.md) se actualizó con ellos):

| Momento | Llamada o aviso de FAV | Nuestra orden de reposición |
|---|---|---|
| Se decide comprar | `POST /v1/pedidos` con `Idempotency-Key` = `<id de la orden>-<intento>`, `referenciaExterna` = id de la orden y `notificacionUrl` = nuestro receptor | sigue en `BORRADOR` hasta tener respuesta |
| `201 PENDIENTE_DE_PAGO` | guardamos `pedidoId`, total y `pago.venceEn` | `BORRADOR → SOLICITADA`; el pago por transferencia lo hace una persona |
| `409 STOCK_INSUFICIENTE` | `faltantes` dice qué variantes no alcanzan | sigue en `BORRADOR` con el motivo a la vista. Como cada prenda de FAV solo la vende FAV, no hay otro proveedor al que pasarla: se reintenta en el siguiente ciclo de reposición (cada hora), con un intento nuevo, o el operador la cancela |
| Aviso o consulta `PAGADO` | — | `SOLICITADA → CONFIRMADA`; se registra el stock entrante |
| `VENCIDO` | — | `SOLICITADA → VENCIDA`. Para reponer se crea una orden nueva, después de que el operador confirma que no transfirió (si transfirió tarde, FAV la pasa a `A_VERIFICAR`) |
| `A_VERIFICAR` (el pago llegó después del vencimiento) | — | `VENCIDA → EN_REVISION`, sin acción automática; se resuelve por contacto con FAV |
| Un estado desconocido | — | `SOLICITADA → EN_REVISION`, sin acción automática, como pide el contrato |

- **Receptor del webhook:** `stock-service` expone `POST /webhooks/fav` en su propia dirección pública, **fuera del gateway**, para cumplir la regla 4.1. Es la única ruta de ese servicio accesible desde afuera, y se autentica con la firma, no con API key. El receptor:
  1. verifica la firma `X-FAV-Firma` con el secreto de avisos y rechaza con `401` si es inválida o tiene más de 5 minutos;
  2. descarta los duplicados por `eventoId`, con la misma tabla de eventos procesados que el consumidor idempotente de [ADR-005](ADR-005-comunicacion.md);
  3. responde `2xx` antes de 5 s y procesa después;
  4. confirma con `GET /v1/pedidos/{pedidoId}` antes de cambiar la orden, porque el contrato dice que la fuente es la consulta y no el aviso;
  5. ignora un aviso con un estado anterior al que ya tiene.
- **Respaldo:** cada 15 minutos, `GET /v1/pedidos/{pedidoId}` de las órdenes en `SOLICITADA` y de las `VENCIDA` de los últimos 7 días (para detectar un `A_VERIFICAR`), por si un aviso se perdió.

### Llamadas salientes, errores y cambios de contrato

- **Timeout de 3 s** por llamada, y reintentos **solo ante timeout, `429`, `500` o `503`**: hasta 3, con espera creciente, respetando `Retry-After`. Son las recomendaciones del contrato.
- **Idempotencia de los pedidos:** cada intento de envío de una orden tiene su clave, `<id de la orden>-<intento>`. Los reintentos técnicos de ese intento (timeout, `429`, `500`, `503`) usan **la misma clave y el mismo cuerpo**, así no se crea un pedido duplicado aunque FAV lo haya creado antes del timeout. Después de un error definitivo (un `4xx`), el intento siguiente usa una clave nueva: reusar la anterior devolvería la misma respuesta guardada, o `409 CLAVE_IDEMPOTENCIA_REUTILIZADA` si cambió el cuerpo. Como la clave vale 48 horas **[propuesta de FAV]** y no hay forma de buscar un pedido por `referenciaExterna`, un intento que pasa **24 horas** sin respuesta definitiva va a `EN_REVISION` y se confirma por contacto, en lugar de reintentarse.
- **Sin reintento** ante `400`, `401`, `404` y `409`: se registran con el `X-Correlation-Id`, que enviamos en cada llamada para seguirla en los logs de los dos sistemas.
- **Circuit breaker** sobre las llamadas a FAV ([ADR-010](ADR-010-resiliencia.md)).
- **Cambios de contrato:** se ignoran los campos desconocidos y un estado desconocido se trata como `EN_REVISION`, como pide su política de compatibilidad. Los headers `Deprecation` y `Sunset` se loguean como advertencia, y un `410 VERSION_RETIRADA` dispara una alerta. Una respuesta que no valida contra el contrato se rechaza y no pisa los datos buenos que ya teníamos.
- **Dudas y desvíos del contrato** (los puntos **[?]**, los límites marcados como propuesta y cualquier respuesta que no valide) se registran como **issue en el repositorio de FAV**, como pide el complemento de la consigna. El mail de contacto queda para las credenciales y para coordinar pedidos.
- **Test de contrato:** se validan las respuestas reales contra la [copia del contrato v1.1.0](../contracts/consumidos/fav-catalogo-pedidos-v1.1.0.openapi.yaml). Contra **el mock de FAV** se prueban el formato de las llamadas, los errores forzados (`Prefer: code=409`, `429`, `503`) y el receptor: firma válida, inválida o vieja, y avisos duplicados; el contrato trae además un vector de prueba de la firma. El mock no guarda estado (su consulta devuelve siempre el mismo pedido en `PAGADO`), así que **los caminos de estado** (`VENCIDO` y `A_VERIFICAR` confirmados por la consulta) **y la idempotencia de los reintentos** se prueban con un doble propio que sí guarda estado.
- **Credenciales** (clave del grupo y secreto de avisos) en `.env`, nunca en el repo.

### Qué ve el usuario si FAV se cae

- **El ranking sigue funcionando** con los demás proveedores y con las últimas ofertas válidas de FAV. Si tienen más de 24 horas, desaparecen del ranking.
- **El frontend avisa** "precios de FAV sin actualizar desde HH:MM". Para eso, la respuesta de nuestra capacidad tiene que exponer la fecha de actualización de cada oferta, que **hoy nuestro contrato v1 no trae**: hay que agregar `actualizado_en` por oferta, un cambio compatible que se registra en [ADR-008](ADR-008-contrato-propio.md) cuando se implemente.
- **Una orden de reposición que no se puede enviar** queda en `BORRADOR`, con el motivo a la vista, y se reintenta cuando se cierra el circuit breaker (con la misma `Idempotency-Key`, así que si FAV la había creado no se duplica). La capacidad que publicamos no falla por la caída de FAV.

## Por qué

- **Su indisponibilidad no se vuelve la nuestra.** Con la sincronización periódica, una caída de FAV congela sus precios pero no tira nuestra capacidad ni le suma su latencia.
- **Tiene consecuencias de negocio en los dos sentidos:** sus prendas compiten en el ranking, y lo que les compramos cambia nuestro stock a través de una orden de reposición con estados. Es lo que la consigna pide para que la integración cuente.
- **El webhook es la mejor fuente de cambios**, porque quien se entera primero del pago o del vencimiento es FAV. La consulta periódica cubre el caso que el propio contrato admite: que un aviso se pierda.
- **La `Idempotency-Key` igual al id de la orden** hace que reintentar un pedido sea seguro, incluso si el timeout ocurrió después de que FAV lo creó.
- **La capa anticorrupción aísla su modelo** (variantes, talles, pedidos con transferencia) del nuestro (ofertas por SKU con stock, flete y descuento).

## Consecuencias aceptadas

- **Hoy no hay código de integración.** Hasta que existan `import-service` y `stock-service`, el consumo no cuenta para la consigna.
- **Tomamos 10 unidades como stock de FAV**, que no es su stock real: una recomendación de FAV puede terminar en un `409 STOCK_INSUFICIENTE` al pedir, y para más de 10 unidades por variante FAV no aparece en el ranking.
- **El flete de FAV es un valor nuestro, no de ellos**, hasta acordarlo: el costo total de sus ofertas puede no ser el real.
- **Sus precios pueden estar hasta 5 minutos desactualizados** en condiciones normales, y hasta 24 horas si FAV se cae.
- **Rubros mezclados:** el comparador tiene productos de energía solar y va a sumar prendas. Cada variante de FAV es un producto propio, así que hasta que `catalog-service` las asocie con prendas de otros proveedores, FAV no compite con nadie en esos productos. La búsqueda ([ADR-006](ADR-006-busqueda.md)) va a necesitar filtrar por categoría.
- **El pago es manual** (transferencia) y la entrega se coordina por contacto: la orden queda `SOLICITADA` hasta que una persona paga, y un pago tardío termina en `EN_REVISION` y se resuelve a mano.
- **`stock-service` tiene una ruta accesible desde afuera que no pasa por el gateway.** Es una segunda puerta de entrada al sistema, limitada a `POST /webhooks/fav` y protegida solo por la firma HMAC, así que el secreto de avisos se cuida igual que una clave. Lo aceptamos porque la consigna (4.1) prohíbe canalizar la comunicación con el proveedor por el gateway; lo vamos a confirmar con la cátedra.
- **Dependemos de definiciones pendientes de FAV:** su URL desplegada, cómo entrega la clave y el secreto, y los límites que marcó como propuesta (60 pedidos por minuto, 20 variantes, 10 unidades).

## Con qué lo medimos

Todavía no hay mediciones. Cuando esté implementado:

- **Antigüedad de la última sincronización exitosa** del catálogo (objetivo < 10 min) y tasa de éxito de las sincronizaciones.
- **Latencia p95** de las llamadas a FAV, medida desde nuestro lado.
- **Avisos recibidos:** duplicados descartados, firmas rechazadas y órdenes que tuvo que resolver la consulta de respaldo (cuántos avisos se perdieron).
- **Órdenes que terminaron en `409`:** mide cuánto nos cuesta no conocer su stock.
- **Caída simulada en el Game Day:** con FAV cortado, la capacidad publicada sigue en `200`, el ranking muestra sus últimas ofertas válidas y las órdenes nuevas esperan en `BORRADOR`.
- **Test de contrato** en verde contra la v1.1.0 y contra su mock.

## Qué nos haría cambiar de idea

- **Si FAV agrega disponibilidad al catálogo** (lo anuncia como posible agregado compatible), usarla como `stock_disponible` y dejar el tope de 10.
- **Si agrega paginación o un filtro por fecha de cambio**, pasar a una sincronización incremental.
- **Si los pedidos terminan seguido en `409`**, la estimación de stock no sirve: consultar la disponibilidad antes de recomendar, o sacar a FAV del ranking para cantidades grandes.
- **Si publica una `v2`**, migrar antes de la fecha de baja anunciada y registrar el cambio en un ADR que reemplace a este.
- **Si la cátedra considera que la sincronización periódica no es "consumo en el flujo principal"**, consultar el catálogo en el momento para el detalle de producto, manteniendo el ranking sobre la copia local.
