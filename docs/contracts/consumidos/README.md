# Contratos que consumimos

Copias de los contratos publicados por los grupos que nos proveen una capacidad. No
las editamos: son la versión exacta contra la que se escribe el adaptador y contra la
que se corre el test de contrato. Cuando el proveedor publica una versión nueva, se
agrega su archivo al lado y se revisa [ADR-009](../../adr/ADR-009-consumo-proveedor.md).

| Proveedor | Contrato | Versión | Publicada | Estado |
|---|---|---|---|---|
| FAV (tienda de ropa) | [`fav-catalogo-pedidos-v1.2.0.openapi.yaml`](fav-catalogo-pedidos-v1.2.0.openapi.yaml) + [`fav-readme-v1.2.0.md`](fav-readme-v1.2.0.md) | **1.2.0** | 09/11/2026, recibida el 11/11 | **Vigente.** Rompe dos cosas nuestras: ver abajo |
| FAV (tienda de ropa) | [`fav-catalogo-pedidos-v1.1.0.openapi.yaml`](fav-catalogo-pedidos-v1.1.0.openapi.yaml) | 1.1.0 | 15/10/2026 | Contra esta se escribió nuestro adaptador de pedidos |

URL desplegada de FAV: `https://fav-ucc.chilecentral.cloudapp.azure.com` (HTTPS con
certificado válido, verificado el 11/11). Clave propia por `X-API-Key`, entregada por
privado: **no está en el repositorio**. Contacto: 2212370@ucc.edu.ar.

## La 1.2.0 saca dos cosas que usamos

FAV la publica como **compatible** con la 1.1.0, pero comparando los dos archivos:

| | 1.1.0 (nuestra copia) | 1.2.0 (vigente) |
|---|---|---|
| `GET /v1/productos` | sí | sí |
| `POST /v1/pedidos` | sí | sí |
| `GET /v1/pedidos/{pedidoId}` | **sí** | **NO** |
| Webhook `pedidoActualizado` con firma HMAC | **sí**, documentado | **NO**, cero menciones |
| `GET /v1/disponibilidad` | no | **sí** (nuevo) |

Su propio README lo confirma: *"Consultar el estado de un pedido no está en la v1"*, y
lo listan como un **agregado posible a futuro**. El webhook no aparece en ninguna parte.

**Por qué importa:** quitar operaciones es, según la sección 5 de su propio contrato, un
cambio **incompatible** que pide una versión mayor (`/v2/`). Salió como versión menor.
Lo que rompe de nuestro lado está en
[ADR-009](../../adr/ADR-009-consumo-proveedor.md): la consulta de respaldo del estado
del pedido y el receptor del webhook.

**Lo que sigue funcionando sin tocar código:** el catálogo. Nuestro modelo coincide
campo por campo con la respuesta real (`items[].productoId`, `nombre`, `categoria`,
`variantes[].varianteId`, `talle`, `color`, `precio`), verificado el 11/11 contra la
URL desplegada.

**Lo que va en la otra dirección** —lo que FAV necesitaría para avisarnos— está en
[`../para-fav.md`](../para-fav.md). **Quedó sin uso:** la 1.2.0 no tiene webhook, así
que FAV no nos va a avisar de nada y nuestro receptor no se va a llamar nunca.
