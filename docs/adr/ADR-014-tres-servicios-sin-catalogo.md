# ADR-014 — Tres servicios de dominio, sin `catalog-service` (D1)

- **Estado:** propuesto. Lo tiene que validar el grupo en la reunión de inicio de la Entrega 3 y confirmarlo la cátedra. **Reemplaza** la parte de [ADR-001](ADR-001-limites-de-los-servicios.md) que proponía `catalog-service` y la "tensión" sobre la tabla `productos`.
- **Fecha:** 2026-11-03
- **Entrega:** 3 (06/11)
- **Decisión de la consigna:** D1. Criterios usados para separar responsabilidades, relaciones entre servicios y propiedad de los datos.

## Contexto

[ADR-001](ADR-001-limites-de-los-servicios.md) proponía cuatro servicios de
dominio: `proveedores-service`, `stock-service`, `import-service` y
`catalog-service`. Este último iba a mapear los códigos de cada proveedor a un
único producto del catálogo maestro (RN-18). El mismo ADR dejó escrito que esa
separación era una hipótesis: "si el mapeo de códigos resulta ser parte de la
importación, se funden y quedan tres servicios".

Al 03/11 hay que llegar al 06/11 con el código congelado, y la consigna pide
**al menos tres** servicios operativos para la presentación. Con el contrato
de FAV ya leído ([ADR-009](ADR-009-consumo-proveedor.md)), el mapeo resultó ser
justamente parte de la importación: cada variante de FAV se traduce a un
producto nuestro en la capa anticorrupción de `import-service`.

## Opciones evaluadas

1. **Construir `catalog-service` igual.** Cumple el diseño de ADR-001, pero hoy
   no tendría trabajo propio: el único proveedor externo ya llega traducido, y
   los 10 del seed ya usan nuestros SKU. Sería un cuarto servicio que solo
   reenvía datos, con su base, su contrato interno y su operación.
2. **Tres servicios: el mapeo vive en `import-service`** y `productos` queda en
   `proveedores-service`, que la recibe por el evento `ListaPreciosActualizada`.
3. **Fundir importación y ranking en un servicio.** Quedarían dos servicios de
   dominio: no cumple la consigna.

## Qué elegimos

**La opción 2.** Los servicios de dominio quedan así:

| Servicio | Responsabilidad | Datos | Estado |
|---|---|---|---|
| `proveedores-service` | Ranking por costo total, capacidad publicada, caché y búsqueda | `proveedores`, `productos`, `ofertas` (PostgreSQL) | implementado |
| `import-service` | Bajar listas de proveedores externos, traducirlas (capa anticorrupción) y avisar los cambios | importaciones y respuesta cruda (MongoDB) | implementado (FAV) |
| `stock-service` | Stock propio, reserva sin sobreventa, reposición y pedidos a FAV | existencias y órdenes (PostgreSQL, base propia) | propuesto (Integrante 3) |

- **Dueño de `productos`: `proveedores-service`.** Los productos de un proveedor
  externo entran por el evento, con el SKU que arma `import-service`
  (`FAV-<varianteId>`). Se termina la "tensión" de ADR-001: no hay copia de un
  dato ajeno, porque el evento es la forma en que se lo entregan.
- **El mapeo es por proveedor y vive en su adaptador** (`internal/dominio` de
  `import-service`): un proveedor nuevo es un adaptador nuevo, no un servicio.

## Por qué

- **Lo que iba a hacer `catalog-service` ya lo hace la importación.** Traducir el
  formato del proveedor y su código de producto es una sola operación; partirla
  en dos servicios agregaba una llamada de red y una falla parcial sin ganar nada.
- **La consigna se cumple con tres** y el tiempo que queda va a lo que sí exige
  la presentación (evento con consumidor idempotente, búsqueda, caché, stock).
- **Es la salida que ADR-001 ya había previsto**, no un cambio de rumbo.

## Consecuencias aceptadas

- **No hay catálogo maestro entre proveedores.** Una remera de FAV y una igual de
  otro proveedor son dos productos distintos: no compiten en el mismo ranking.
  RN-18 queda cumplida solo dentro de cada proveedor.
- **`proveedores-service` crece**: además del ranking, tiene que aplicar el evento,
  la caché y la búsqueda. Si se vuelve difícil de mantener, separar la búsqueda
  (ya lo prevé ADR-001).
- **ADR-001 queda con una parte reemplazada**, que se marca ahí sin borrarla.

## Con qué lo medimos

- **Cantidad de servicios de dominio operativos en `docker compose up`:** 2 al
  03/11 (`proveedores-service` e `import-service`); 3 cuando entre `stock-service`.
- **Llamadas entre servicios para importar una lista:** **0** síncronas; un solo
  evento por cambio (medido en los tests de `internal/sincronizacion`).
- **Cuántas variantes de FAV entran sin mapeo manual:** 10 de 10 del catálogo del
  mock; 3 de 3 del ejemplo del contrato (test de contrato en `internal/fav`).

## Qué nos haría cambiar de idea

- **Si se suma un segundo proveedor externo con productos que coinciden con los de
  FAV** y el negocio necesita compararlos entre sí, el catálogo maestro vuelve a
  tener trabajo propio: crear `catalog-service` en un ADR que reemplace a este.
- **Si la cátedra no acepta tres servicios** con esta división, separar la
  búsqueda de `proveedores-service` antes que crear un catálogo sin trabajo.
