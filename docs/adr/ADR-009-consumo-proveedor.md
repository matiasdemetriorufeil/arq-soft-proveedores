# ADR-009 — Consumo del proveedor: catálogo y pedidos de FAV (D9)

- **Estado:** **aceptado e implementado para el catálogo**, que funciona contra el FAV real. **Parcialmente invalidado para los pedidos**: el 11/11 FAV entregó su URL y su contrato **1.2.0**, que **quitó** `GET /v1/pedidos/{pedidoId}` y el webhook `pedidoActualizado`, las dos piezas sobre las que esta decisión apoyaba la confirmación de un pedido. El código existe y está probado, pero contra una versión del contrato que FAV ya no publica. Ver [Lo que rompió la 1.2.0 de FAV](#lo-que-rompió-la-120-de-fav).
- **Fecha:** 2026-10-23 · actualizado 2026-11-11
- **Entrega:** 2 (23/10) y 3 (06/11)
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
  | `stock_disponible` | el real de `GET /v1/disponibilidad` desde el 11/11; antes, **10** fijo (el máximo por variante de un pedido) | el catálogo no informa stock. Hasta la 1.2.0 de FAV la oferta valía hasta lo que se podía pedir de una vez; ahora sale el dato real y el tope quedó de respaldo |

- **Frecuencia:** cada **5 minutos** (`INTERVALO_SINCRONIZACION`) y al arrancar. Se compara con lo último que se avisó y solo se publican los cambios: altas, modificaciones y bajas, con una `secuencia` que crece ([`eventos.md`](../contracts/eventos.md)).
- **Si FAV no responde:** se conserva la **última importación válida**. Si la última sincronización exitosa tiene **más de 24 horas**, `import-service` publica un evento con todas sus ofertas como bajas (`motivo: catalogo_vencido`), **una sola vez**; cuando FAV vuelve, se reenvía el catálogo completo.
- **Llamada a FAV** (`internal/fav`): timeout de **3 s** por intento y hasta **3 reintentos** solo ante timeout, error de red, `429`, `500` o `503`, con espera de 0,5, 1 y 2 s o la que pida `Retry-After` (con tope de 30 s). Un `401` no se reintenta. Un catálogo que no cumple el contrato (faltan campos obligatorios) se rechaza entero y sigue valiendo el anterior; una variante con datos que no sirven (precio negativo, id repetido) se descarta sola y queda registrada.
- **Circuit breaker:** después de **3** sincronizaciones fallidas seguidas, se saltean las **3** vueltas siguientes sin llamar a FAV (la espera es `3,5 × INTERVALO_SINCRONIZACION`, 17 min 30 s con 5 min); la cuarta hace una sola llamada de prueba. Un `401` no cuenta como falla: es un problema de configuración, no una caída ([ADR-010](ADR-010-resiliencia.md)).
- **Mock local:** mientras FAV no publique su URL, el compose levanta `fav-mock`, que cumple su contrato y permite simular fallas (`caido`, `lento`, `limite`, `error`, `invalido`) para el Game Day.

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

- **Cómo sale el pedido** (`internal/service/pedidos.go`): una prenda de FAV (`FAV-<varianteId>`) solo la vende FAV, así que su orden nace dirigida a FAV (proveedor 11) sin consultar el ranking, con **hasta 10 unidades** (el máximo por variante de su contrato; lo que falte lo pide el ciclo siguiente). El pedido sale en la misma vuelta del ciclo de reposición, o a mano con `POST /api/v1/stock/ordenes/{id}/pedido`. Al confirmarse, las unidades se suman al stock **en la misma transacción** que pasa la orden a `CONFIRMADA`.
- **Receptor del webhook** (`internal/handler/pedidos.go`): `stock-service` expone `POST /webhooks/fav`, **fuera del gateway**, para cumplir la regla 4.1. Es la única ruta de ese servicio sin API key: se autentica con la firma, y sin `FAV_SECRETO_AVISOS` no se registra. El receptor:
  1. verifica la firma `X-FAV-Firma` con el secreto de avisos y rechaza con `401` si es inválida o tiene más de 5 minutos;
  2. descarta los duplicados por `eventoId` con la tabla `avisos_fav_procesados`, cuya PRIMARY KEY deja pasar uno solo aunque lleguen dos a la vez;
  3. responde `2xx` enseguida y procesa después; si la base no responde, contesta `503` para que FAV reintente;
  4. confirma con `GET /v1/pedidos/{pedidoId}` antes de cambiar la orden, porque el contrato dice que la fuente es la consulta y no el aviso;
  5. ignora un aviso con un estado anterior al que ya tiene.
- **Respaldo:** cada 15 minutos (`INTERVALO_PEDIDOS_FAV`), `GET /v1/pedidos/{pedidoId}` de las órdenes en `SOLICITADA` y de las `VENCIDA` de los últimos 7 días (para detectar un `A_VERIFICAR`), por si un aviso se perdió. La misma vuelta reintenta los pedidos que quedaron en `BORRADOR`. Si el aviso y la consulta confirman la misma orden a la vez, el `UPDATE` condicionado al estado deja ganar a uno solo y el stock entra una vez.

### Llamadas salientes, errores y cambios de contrato

- **Timeout de 3 s** por llamada, y reintentos **solo ante timeout, `429`, `500` o `503`**: hasta 3, con espera creciente, respetando `Retry-After`. Son las recomendaciones del contrato.
- **Idempotencia de los pedidos:** cada intento de envío de una orden tiene su clave, `<id de la orden>-<intento>`. Los reintentos técnicos de ese intento (timeout, `429`, `500`, `503`) usan **la misma clave y el mismo cuerpo**, así no se crea un pedido duplicado aunque FAV lo haya creado antes del timeout. Después de un error definitivo (un `4xx`), el intento siguiente usa una clave nueva: reusar la anterior devolvería la misma respuesta guardada, o `409 CLAVE_IDEMPOTENCIA_REUTILIZADA` si cambió el cuerpo. Como la clave vale 48 horas **[propuesta de FAV]** y no hay forma de buscar un pedido por `referenciaExterna`, un intento que pasa **24 horas** sin respuesta definitiva va a `EN_REVISION` y se confirma por contacto, en lugar de reintentarse.
- **Sin reintento** ante `400`, `401`, `404` y `409`: se registran con el `X-Correlation-Id`, que enviamos en cada llamada para seguirla en los logs de los dos sistemas.
- **Circuit breaker** sobre las llamadas a FAV ([ADR-010](ADR-010-resiliencia.md)).
- **Cambios de contrato:** se ignoran los campos desconocidos y un estado desconocido se trata como `EN_REVISION`, como pide su política de compatibilidad. Los headers `Deprecation` y `Sunset` se loguean como advertencia, y un `410 VERSION_RETIRADA` dispara una alerta. Una respuesta que no valida contra el contrato se rechaza y no pisa los datos buenos que ya teníamos.
- **Dudas y desvíos del contrato** (los puntos **[?]**, los límites marcados como propuesta y cualquier respuesta que no valide) se registran como **issue en el repositorio de FAV**, como pide el complemento de la consigna. El mail de contacto queda para las credenciales y para coordinar pedidos.
- **Test de contrato:** se validan las requests y respuestas contra la [copia del contrato v1.1.0](../contracts/consumidos/fav-catalogo-pedidos-v1.1.0.openapi.yaml). El mock que publica FAV no guarda estado (su consulta devuelve siempre el mismo pedido en `PAGADO`), así que el nuestro (`fav-mock`) sí lo guarda. Con él se prueban la idempotencia de los pedidos, los caminos de estado (`PAGADO`, `VENCIDO`, `A_VERIFICAR`) y el webhook: firma válida e inválida y avisos duplicados. El vector de prueba de la firma que trae el contrato se verifica en los dos servicios.
- **Credenciales** (clave del grupo y secreto de avisos) en `.env`, nunca en el repo.

### Qué ve el usuario si FAV se cae

- **El ranking sigue funcionando** con los demás proveedores y con las últimas ofertas válidas de FAV. Si tienen más de 24 horas, desaparecen del ranking.
- **El frontend avisa** "precios de FAV sin actualizar desde HH:MM". Para eso, la respuesta de nuestra capacidad tiene que exponer la fecha de actualización de cada oferta, que **hoy nuestro contrato v1 no trae**: hay que agregar `actualizado_en` por oferta, un cambio compatible que se registra en [ADR-008](ADR-008-contrato-propio.md) cuando se implemente.
- **Una orden de reposición que no se puede enviar** queda en `BORRADOR`, con el motivo a la vista, y se reintenta en la vuelta siguiente (cada 15 minutos) con la misma `Idempotency-Key`, así que si FAV la había creado no se duplica. La capacidad que publicamos no falla por la caída de FAV.

## Por qué

- **Su indisponibilidad no se vuelve la nuestra.** Con la sincronización periódica, una caída de FAV congela sus precios pero no tira nuestra capacidad ni le suma su latencia.
- **Tiene consecuencias de negocio en los dos sentidos:** sus prendas compiten en el ranking, y lo que les compramos cambia nuestro stock a través de una orden de reposición con estados. Es lo que la consigna pide para que la integración cuente.
- **El webhook es la mejor fuente de cambios**, porque quien se entera primero del pago o del vencimiento es FAV. La consulta periódica cubre el caso que el propio contrato admite: que un aviso se pierda.
- **La `Idempotency-Key` igual al id de la orden** hace que reintentar un pedido sea seguro, incluso si el timeout ocurrió después de que FAV lo creó.
- **La capa anticorrupción aísla su modelo** (variantes, talles, pedidos con transferencia) del nuestro (ofertas por SKU con stock, flete y descuento).

## Consecuencias aceptadas

- **El catálogo ya se importa, pero todavía no llega al ranking**: falta el consumidor del evento en `proveedores-service` (Integrante 2). Los pedidos ya funcionan, porque no dependen del ranking.
- **Hoy se prueba contra nuestro mock**, no contra FAV: el mock cumple su contrato (lo verifica el test de contrato), pero no reemplaza probar contra su despliegue.
- **Tomamos 10 unidades como stock de FAV**, que no es su stock real: una recomendación de FAV puede terminar en un `409 STOCK_INSUFICIENTE` al pedir, y para más de 10 unidades por variante FAV no aparece en el ranking.
- **El flete de FAV es un valor nuestro, no de ellos**, hasta acordarlo: el costo total de sus ofertas puede no ser el real.
- **Sus precios pueden estar hasta 5 minutos desactualizados** en condiciones normales, y hasta 24 horas si FAV se cae.
- **Rubros mezclados:** el comparador tiene productos de energía solar y va a sumar prendas. Cada variante de FAV es un producto propio, así que hasta que `catalog-service` las asocie con prendas de otros proveedores, FAV no compite con nadie en esos productos. La búsqueda ([ADR-006](ADR-006-busqueda.md)) va a necesitar filtrar por categoría.
- **El pago es manual** (transferencia) y la entrega se coordina por contacto: la orden queda `SOLICITADA` hasta que una persona paga, y un pago tardío termina en `EN_REVISION` y se resuelve a mano.
- **`stock-service` tiene una ruta accesible desde afuera que no pasa por el gateway.** Es una segunda puerta de entrada al sistema, limitada a `POST /webhooks/fav` y protegida solo por la firma HMAC, así que el secreto de avisos se cuida igual que una clave. Lo aceptamos porque la consigna (4.1) prohíbe canalizar la comunicación con el proveedor por el gateway; lo vamos a confirmar con la cátedra. Con el mock, el aviso viaja por la red interna del compose; con el FAV desplegado, `FAV_NOTIFICACION_URL` tiene que ser una URL pública que llegue solo a esa ruta (parte de la nube, Integrante 4).
- **Dependemos de definiciones pendientes de FAV:** su URL desplegada, cómo entrega la clave y el secreto, y los límites que marcó como propuesta (60 pedidos por minuto, 20 variantes, 10 unidades).

## Con qué lo medimos

Mediciones del 06/11 sobre los pedidos (`stock-service`):

- **Prueba de punta a punta** con los binarios reales (`fav-mock`, `stock-service` y PostgreSQL 16):
  - El primer ciclo creó las órdenes de las 2 prendas bajo el mínimo y les hizo el pedido sin intervención: las dos pasaron a `SOLICITADA` con su `pedido_id`.
  - Un aviso `PAGADO` con firma inválida recibió `401` y la orden no cambió. La consulta de respaldo lo corrigió en la vuelta siguiente: `CONFIRMADA`, y el stock de la remera pasó de 2 a 10.
  - Un aviso `VENCIDO` enviado dos veces se procesó una sola vez (`VENCIDA`). Un pago tardío (`A_VERIFICAR`) dejó la orden en `EN_REVISION`.
- **Test de contrato en verde** (`internal/fav/contrato_test.go`): nuestro `POST /v1/pedidos` y nuestro `GET /v1/pedidos/{pedidoId}` cumplen el contrato, y el validador detecta un pedido de 11 unidades. El cliente entiende los ejemplos `Pedido` y `PedidoPagado` y traduce los 4 ejemplos de error del contrato. El vector de firma verifica. Las respuestas y los avisos del mock validan contra el mismo contrato (`import-service/internal/fav/contrato_pedidos_test.go`).
- **Reintentos:** ante `503` y `500`, el pedido sale al tercer intento con la **misma clave y el mismo cuerpo**, respetando `Retry-After`. Con FAV caído se rinde a los 4 intentos, y un intento colgado se corta por timeout. `409`, `400`, `401` y una respuesta fuera de contrato se intentan 1 vez. Después de un `409 STOCK_INSUFICIENTE`, el intento siguiente usa la clave `<id>-2`.
- **Contra PostgreSQL 16 real:**
  - 20 entregas simultáneas del mismo aviso: se registra 1.
  - 10 confirmaciones simultáneas de la misma orden (webhook y respaldo): el stock suma 8 una sola vez.
  - Un fallo después de mover la orden deshace la orden y el stock.
- **En total:** 151 casos unitarios en verde, 0 en rojo, y 16 tests de integración contra PostgreSQL real, repetidos 5 veces. Cobertura: `internal/fav` 86,3 %, `internal/service` 78,5 %, `internal/handler` 78,4 %.

Mediciones del 03/11 sobre `import-service` (tests de `go test ./...`):

- **Test de contrato en verde** contra la copia del OpenAPI v1.1.0 (`internal/fav/contrato_test.go`): nuestro cliente entiende el ejemplo del propio contrato (3 de 3 variantes traducidas, 0 descartes), nuestra request cumple el contrato (ruta, método, `X-API-Key`, `X-Correlation-Id`) y las respuestas del mock validan contra el esquema en `200`, `401`, `429`, `500` y `503`. El validador detecta una respuesta fuera de contrato. Con `FAV_URL_CONTRATO` y `FAV_API_KEY`, el mismo test corre contra el FAV desplegado.
- **Reintentos:** ante `429`, `500` o `503` transitorios, el catálogo llega al tercer intento con esperas crecientes; con FAV caído se rinde a los **4** intentos; un `401` se intenta **1** vez; un intento colgado se corta a los 3 s.
- **Caída de FAV** (tests de `internal/sincronizacion`): tres sincronizaciones fallidas no publican nada y abren el breaker; la cuarta no llama a FAV. A las 23 h 59 min sin sincronizar no se retira nada; pasadas las 24 h se publica **un** evento con las 2 bajas y no se repite.

Cuando esté integrado de punta a punta:

- **Antigüedad de la última sincronización exitosa** del catálogo (objetivo < 10 min) y tasa de éxito de las sincronizaciones.
- **Latencia p95** de las llamadas a FAV, medida desde nuestro lado.
- **Avisos recibidos:** duplicados descartados, firmas rechazadas y órdenes que tuvo que resolver la consulta de respaldo (cuántos avisos se perdieron).
- **Órdenes que terminaron en `409`:** mide cuánto nos cuesta no conocer su stock.
- **Caída simulada en el Game Day:** con FAV cortado, la capacidad publicada sigue en `200`, el ranking muestra sus últimas ofertas válidas y las órdenes nuevas esperan en `BORRADOR`.
- **Test de contrato** en verde contra la v1.1.0 y contra su mock.

## Lo que rompió la 1.2.0 de FAV

El 11/11 FAV entregó su URL desplegada (`https://fav-ucc.chilecentral.cloudapp.azure.com`, HTTPS con certificado válido), su clave y el contrato **1.2.0** del 09/11. Comparando los dos archivos en [`consumidos/`](../contracts/consumidos/README.md):

| Operación | 1.1.0 | 1.2.0 |
|---|---|---|
| `GET /v1/productos` | sí | sí |
| `POST /v1/pedidos` | sí | sí |
| `GET /v1/pedidos/{pedidoId}` | **sí** | **NO** |
| Webhook `pedidoActualizado` firmado con HMAC | **sí** | **NO** |
| `GET /v1/disponibilidad` | no | **sí** (nuevo) |

FAV la publica como compatible. **No lo es**: la sección 5 de su propio contrato define quitar una operación como cambio **incompatible**, que pediría `/v2/`. Su README lo dice sin ambigüedad: *"Consultar el estado de un pedido no está en la v1"*, y lo lista como agregado posible a futuro.

**Lo que sigue en pie:** el catálogo. Nuestro modelo coincide campo por campo con la respuesta real, verificado el 11/11 contra la URL desplegada: 5 productos y 19 variantes. **No hace falta tocar código para importar del FAV real.**

**Lo que queda sin apoyo en el contrato vigente**, aunque el código exista y esté probado:

1. **La consulta de respaldo del estado del pedido** (`internal/fav.ConsultarPedido`, usada en `internal/service/pedidos.go`). Contra el FAV real responde `404 NO_ENCONTRADO`.
2. **El receptor del webhook** (`POST /webhooks/fav`, firma HMAC, deduplicación por `eventoId`). FAV no tiene webhook: nunca va a llamarlo.
3. **Por lo tanto, el paso `SOLICITADA → CONFIRMADA` no se puede completar automáticamente.** FAV confirma el pago por fuera de la API, por mail (su §7). Con la 1.2.0, una orden enviada se queda en `SOLICITADA` hasta que alguien la transicione a mano con `POST /api/v1/stock/ordenes/{id}/transiciones`.
4. **El secreto de firma de avisos** ya no aplica: no hay avisos que firmar.

**Lo que la 1.2.0 habilita, y ya está implementado (11/11):** `GET /v1/disponibilidad` devuelve el stock real por variante. Las ofertas de FAV entraban al ranking con un **stock fijo de 10 por variante**, que era la razón principal por la que un pedido podía terminar en `409`. Ahora el stock sale del dato real.

- `import-service` pide la disponibilidad de todas las variantes del catálogo en la misma vuelta de sincronización, **troceando en tandas de 200**, que es el tope del contrato.
- Una variante que FAV no devuelve queda en **0, no en el tope**: su contrato informa con 0 tanto la inexistente como la sin stock, y suponer 10 unidades de algo que FAV no reconoce es justamente lo que producía el `409`.
- Una oferta con 0 **no se descarta**: se publica con 0 y el comparador la deja afuera del ranking por no alcanzar la cantidad pedida.
- **Si la consulta falla, la importación sigue** con el tope fijo. Importa porque su README avisa que FAV puede **apagar** la operación: ese `404` se trata como `ErrDisponibilidadApagada`, que no es una caída.
- **Hueco de su contrato:** el README documenta ese `404` pero el OpenAPI 1.2.0 **no lo declara** para esa ruta. Lo manejamos igual, y queda un test que documenta la diferencia.

**Verificado contra el FAV real** (`TestContratoDisponibilidadContraElFAVReal`): 19 variantes con stock entre 0 y 9, **6 de ellas en 0**. Antes esas 6 entraban al ranking con stock 10.

**Lo que no se decidió todavía**, porque es una decisión de grupo y no técnica:

- ¿Se apunta `FAV_URL` al FAV real para la presentación? El catálogo funciona, pero **cambian los SKU** del ranking (`FAV-var-remera-*` → `FAV-var-buzo-lino-hh-*`, etc.), y eso toca los ejemplos de `README.md`, `DEMO-ENTREGA-3.md` y `para-renewable-energy.md`.
- ¿Se pide a FAV que publique la consulta de estado como `1.3.0`, o se acepta el cierre manual de la orden?

## Pendiente

Partes de esta decisión que todavía no están en el código:

- **Circuit breaker en los pedidos.** Hoy solo el catálogo tiene breaker. Los pedidos tienen timeout, reintentos y la vuelta cada 15 minutos, que ya limita la carga sobre FAV caído, pero no un breaker.
- **Un intento sin respuesta definitiva durante 24 horas** no pasa solo a `EN_REVISION`. Sigue en `BORRADOR` con el motivo a la vista y se reintenta con la misma clave. RN-15 no permite `BORRADOR → EN_REVISION`, así que hay que decidir si se suma esa transición.
- **`Deprecation`, `Sunset` y `410 VERSION_RETIRADA`** no se tratan aparte: un `410` cuenta como pedido rechazado.
- **La URL pública del webhook** para el FAV desplegado (ver *Consecuencias*).

## Qué nos haría cambiar de idea

- ~~**Si FAV agrega disponibilidad al catálogo**, usarla como `stock_disponible` y dejar el tope fijo.~~ **Pasó y está hecho** (11/11): la 1.2.0 trae `GET /v1/disponibilidad` y el stock del ranking ahora sale de ahí. El tope fijo quedó como respaldo.
- **Si agrega paginación o un filtro por fecha de cambio**, pasar a una sincronización incremental.
- **Si los pedidos terminan seguido en `409`**, la estimación de stock no sirve: consultar la disponibilidad antes de recomendar, o sacar a FAV del ranking para cantidades grandes.
- **Si publica una `v2`**, migrar antes de la fecha de baja anunciada y registrar el cambio en un ADR que reemplace a este.
- **Si la cátedra considera que la sincronización periódica no es "consumo en el flujo principal"**, consultar el catálogo en el momento para el detalle de producto, manteniendo el ranking sobre la copia local.
