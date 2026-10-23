# ADR-001 — Límites de los servicios (D1)

- **Estado:** aceptado para `api-gateway` y `proveedores-service` (implementados y verificados); **propuesto** para `stock-service`, `import-service` y `catalog-service` (todavía no existen en el código)
- **Fecha:** 2026-10-23
- **Entrega:** 1 (no se presentó en su fecha) y validación en la 2 (23/10)
- **Decisión de la consigna:** D1. Criterios usados para separar responsabilidades, relaciones entre servicios y propiedad de los datos.

## Contexto

La consigna (2.1) pide **al menos tres microservicios y un api gateway**, y (2.8) que cada servicio elija el almacenamiento que responde a sus patrones de acceso, lo que obliga a que cada uno sea **dueño de sus datos**.

Nuestro dominio es un **comparador de proveedores con stock y reposición**: se importan listas de precios de distintos proveedores, se rankean por costo final y se lleva un stock propio que dispara órdenes de reposición. Tenemos dos relaciones de integración asignadas: **publicamos** la consulta de proveedores recomendados al grupo de paneles solares y **consumimos** el catálogo de ropa de otro grupo, que se comporta como un proveedor más.

A la fecha de este ADR solo existen en el código el gateway y `proveedores-service`; el resto son límites diseñados para construir después del 23/10.

## Opciones evaluadas

1. **Monolito modular.** Lo más simple de operar, pero la consigna exige microservicios y una entrada única. Descartada.
2. **Un servicio por entidad** (`proveedores`, `productos`, `ofertas`). Es el corte más fino, pero la consulta principal necesita las tres tablas a la vez: el ranking terminaría haciendo tres llamadas de red para lo que hoy es un solo `JOIN`. Los límites siguen a las tablas, no al negocio.
3. **Un servicio por capacidad de negocio.** Cada servicio agrupa los datos que cambian juntos y las reglas que los usan. Una consulta de la capacidad principal se resuelve dentro de un solo servicio.
4. **Cuatro servicios del borrador de la IA** (importación, catálogo, precios+búsqueda, stock). Es parecido a la opción 3, pero agrupa precios con búsqueda en un mismo servicio y todavía no sabemos si la búsqueda (D6) tiene que ser parte de él.

## Qué elegimos

**La opción 3: un servicio por capacidad de negocio, y cada dato tiene un único dueño.**

| Servicio | Estado | Responsabilidad | Datos que posee |
|---|---|---|---|
| `api-gateway` | implementado | Entrada única: API key, `X-Request-ID`, trazas y reparto entre réplicas. **Sin base de datos ni lógica de dominio.** | ninguno |
| `proveedores-service` | implementado | Ranking de ofertas de proveedores por costo total. Publica la capacidad que consume el grupo de paneles solares. | `proveedores`, `productos`, `ofertas` (PostgreSQL) |
| `stock-service` | propuesto | Stock propio, reserva sin sobreventa, punto de reorden y órdenes de reposición (`BORRADOR → SOLICITADA → CONFIRMADA`, más `VENCIDA` y `EN_REVISION` según [ADR-009](ADR-009-consumo-proveedor.md)), incluidos los pedidos al grupo de ropa. | existencias y órdenes |
| `import-service` | propuesto | Ingesta y normalización de las listas de precios de cada proveedor, incluido el catálogo de ropa que consumimos. | listas crudas y su normalización |
| `catalog-service` | propuesto | Catálogo maestro: mapea los códigos de cada proveedor a un mismo producto. | catálogo unificado |

**Reglas de relación:**

- **Ningún servicio lee la base de otro.** Solo se llaman por su API o por eventos.
- **Solo el gateway recibe tráfico de afuera.** Los servicios y las bases viven en la red interna de Docker.
- **Las llamadas a proveedores externos salen del servicio responsable**, no del gateway ni del frontend (consigna 4.1). Con el grupo de ropa (FAV) hablan dos: `import-service` consume su catálogo y `stock-service` le hace los pedidos de reposición y recibe su webhook, directo y no por el gateway ([ADR-009](ADR-009-consumo-proveedor.md)).
- Interacciones previstas (a confirmar en D5): `stock-service` pregunta a `proveedores-service` a quién reponer (síncrona); `import-service` avisa a `proveedores-service` que hay precios nuevos (evento asíncrono).

**Tensión que dejamos escrita:** hoy la tabla `productos` vive en `proveedores-service`, porque es el único servicio y necesita distinguir un SKU inexistente (`404`) de un producto sin ofertas (`200 []`). Cuando exista `catalog-service`, el dueño de `productos` pasa a ser él, y habrá que decidir si `proveedores-service` guarda una copia local actualizada por evento o lo consulta por API. Esa decisión va en un ADR nuevo que **reemplace esta parte**; no se resuelve ahora.

## Por qué

- **La capacidad principal no cruza servicios.** El ranking necesita proveedores, productos y ofertas juntos; con la opción 3 es una consulta a una sola base, con latencia de milisegundos y sin fallas parciales entre servicios.
- **Cada servicio tiene una razón distinta para cambiar.** Cambiar la fórmula de costo toca `proveedores-service`; cambiar el formato de la lista de un proveedor toca `import-service`; cambiar la política de reposición toca `stock-service`.
- **Los patrones de acceso son distintos**, que es lo que pide la consigna para elegir almacenamiento (D3): el ranking es lectura intensiva y relacional; el stock exige transacciones; las listas importadas son semiestructuradas.
- **El gateway sin datos es lo que lo hace reemplazable**: no hay nada que migrar si cambiamos de gateway, y su caída no pierde información.

## Consecuencias aceptadas

- **Hoy hay un solo servicio de dominio.** La consigna pide tres para la presentación grupal (11 o 13/11); los otros dos (`stock-service` y uno entre `import-service` y `catalog-service`) todavía no están. Es el riesgo principal de este ADR.
- **`productos` tiene una ubicación provisoria**, descrita arriba, y habrá que migrarla.
- **Más servicios, más operación:** más contenedores, más trazas que seguir y más contratos internos que mantener.
- **Los datos derivados se actualizan con retraso.** Si `proveedores-service` guarda una copia de algo que es de otro, hay una ventana de inconsistencia; el retraso aceptable lo define D5/D6.
- **La separación entre `import-service` y `catalog-service` es una hipótesis.** Si el mapeo de códigos resulta ser parte de la importación, se funden y quedan tres servicios.

## Con qué lo medimos

Mediciones del 23/10 sobre el código de `main`:

- **Acoplamiento entre servicios:** `api-gateway` importa **0** paquetes de `proveedores-service` y viceversa (`grep` de los imports); los dos solo comparten `pkg/observabilidad`.
- **Gateway sin datos:** **0** archivos del gateway importan `pgx` o `database/sql`.
- **Propiedad del almacenamiento:** el acceso a PostgreSQL está en **un solo paquete** (`internal/adapters/postgres`) de un solo servicio.
- **Capacidad principal en un solo servicio:** una consulta del flujo principal genera **1** consulta SQL (traza de 4 spans: gateway → gateway cliente → servicio → `SELECT`; ver [ADR-011](ADR-011-observabilidad.md)). Si estuviera partida por entidad, serían tres llamadas.
- **Latencia de la capacidad en el gateway** (medida por el integrante 2 el 22/10, n=23): mediana 1,99 ms, p95 4,92 ms.

La medición que falta —y que decide si hay que fundir servicios— es el costo de las llamadas entre servicios cuando existan (ver abajo).

## Qué nos haría cambiar de idea

- **Si la latencia o la tasa de errores de una llamada entre servicios obliga a duplicar datos** en más de un lugar, estamos partiendo por donde no hay que partir: fundir los dos servicios.
- **Si el equipo no llega a construir los servicios propuestos**, cambiar el reparto: los que no existan se retiran de este ADR y se documentan como alcance recortado, en lugar de dejarlos descritos como si estuvieran.
- **Si la búsqueda (D6) exige su propio índice y ciclo de vida**, separarla de `proveedores-service` en un servicio nuevo.
- **Si el catálogo de ropa no se integra como proveedor** (por ejemplo, porque su contrato no trae precios), revisar qué hace `import-service`.
