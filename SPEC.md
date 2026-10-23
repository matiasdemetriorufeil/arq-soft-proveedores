# SPEC — Alcance, reglas de negocio y criterios de aceptación

Define **qué hace** el comparador de proveedores: las funcionalidades como
historias de usuario, las reglas de negocio que las gobiernan y los criterios con
los que se decide si cada una está terminada. Cómo está construido se explica en
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md); por qué se decidió así, en
[`docs/adr/`](docs/adr/).

## Cómo leer este documento

- **Estado `implementado`**: existe en `main` y se puede comprobar con
  `docker compose up`. Sus criterios de aceptación describen lo que el sistema
  hace hoy, con valores reales de los datos de demostración.
- **Estado `propuesto`**: es alcance diseñado, sin código todavía. Sus criterios
  son el compromiso a cumplir cuando se construya, no una descripción de lo que ya
  funciona.
- Los identificadores (`HU-xx`, `RN-xx`) son estables: los tests y los ADR pueden
  referenciarlos.

## 1. Dominio y actores

**Dominio.** Los proveedores publican precios en formatos distintos. El sistema
los unifica, **compara** a los proveedores por costo final (precio, descuento por
volumen y flete, con stock suficiente) y **recomienda** a quién comprar. Además
lleva un stock propio y, cuando un producto baja de su mínimo, genera una orden de
reposición con el mejor proveedor.

**Acción principal con restricciones de negocio:** elegir proveedor para una
cantidad concreta, donde el resultado depende del stock disponible, del descuento
por volumen y del flete (no es una consulta de datos sin más), y, en el alcance
propuesto, reservar stock sin sobreventa y reponerlo mediante una orden con
estados.

| Actor | Quién es | Qué necesita |
|---|---|---|
| **Consumidor externo** | El sistema del grupo de paneles solares | Saber a qué proveedor comprarle un producto y una cantidad. |
| **Operador de compras** | Persona que usa la interfaz web (propuesto) | Importar listas de precios, comparar proveedores y controlar el stock. |
| **Operador de la plataforma** | Quien mantiene el sistema | Entender qué pasó con una request y que el sistema siga respondiendo ante fallas. |
| **Proveedor externo** | El grupo de catálogo de ropa (propuesto) | Que su catálogo entre como un proveedor más. |

## 2. Reglas de negocio

### Ranking de proveedores — implementado

| ID | Regla |
|---|---|
| **RN-01** | El costo total de comprar `cantidad` unidades a un proveedor es `precio_unitario × cantidad × (1 − descuento_pct/100) + flete`, redondeado a 2 decimales (mitad hacia arriba). |
| **RN-02** | El descuento por volumen se aplica **solo si** el proveedor ofrece descuento (`descuento_pct > 0`) **y** `cantidad ≥ cantidad_minima_descuento` de esa oferta. Si no, el costo se calcula sin descuento. |
| **RN-03** | El flete es un costo fijo por pedido: se suma una vez y no se multiplica por la cantidad. |
| **RN-04** | Una oferta con `stock_disponible < cantidad` **no es candidata**: no aparece en el resultado. |
| **RN-05** | Las ofertas se ordenan por `costo_total` ascendente. Si empatan, por nombre del proveedor ascendente y después por `proveedor_id`. El orden es determinista. |
| **RN-06** | `cantidad` es un entero mayor a 0 (por defecto, 1). `limite` es un entero entre 1 y 50 (por defecto, 5). El resultado tiene como máximo `limite` elementos. |
| **RN-07** | Un producto que **existe** pero no tiene ninguna oferta candidata devuelve una lista vacía. Un SKU que **no existe** es un error. No son lo mismo. |
| **RN-08** | Los datos son consistentes por diseño: precio, flete y stock no pueden ser negativos; el descuento está entre 0 y 100; cada par proveedor–producto tiene una sola oferta. Lo garantiza la base. |

### Acceso y operación — implementado

| ID | Regla |
|---|---|
| **RN-09** | Toda consulta de la capacidad exige una clave válida en `X-API-Key`. Sin clave, o con una inválida, no se consulta nada. |
| **RN-10** | Toda respuesta, también las de error, lleva un `X-Request-ID` (el del cliente si es válido; si no, uno generado). |
| **RN-11** | Los errores se devuelven siempre en `application/problem+json` con un `code` estable. Los códigos son: `PARAMETRO_INVALIDO`, `NO_AUTORIZADO`, `PRODUCTO_NO_ENCONTRADO`, `RUTA_NO_ENCONTRADA`, `NO_DISPONIBLE`. |
| **RN-12** | Una réplica caída no puede dejar sin servicio a la capacidad mientras quede otra sana. Una consulta es un `GET` sin efectos y puede repetirse sin riesgo. |

### Stock y reposición — propuesto

| ID | Regla |
|---|---|
| **RN-13** | **Sin sobreventa:** el stock reservado nunca supera el disponible, aun con reservas simultáneas. Ninguna reserva se pierde ni se duplica. |
| **RN-14** | **Punto de reorden:** cuando el stock de un producto baja de su mínimo configurado, corresponde generar una orden de reposición. Solo una orden abierta por producto. |
| **RN-15** | Una orden de reposición pasa por los estados `BORRADOR → SOLICITADA → CONFIRMADA`, en ese orden y sin saltear estados. Desde `SOLICITADA` también puede pasar a `VENCIDA` (el proveedor la dio de baja por falta de pago) o a `EN_REVISION` (el proveedor informa un estado que no conocemos), y desde `VENCIDA` a `EN_REVISION` (el pago llegó después del vencimiento). `EN_REVISION` se resuelve a mano ([ADR-009](docs/adr/ADR-009-consumo-proveedor.md)). |
| **RN-16** | La orden de reposición se dirige al proveedor con **menor costo total** para la cantidad a reponer (RN-01 a RN-05). |

### Importación y catálogo — propuesto

| ID | Regla |
|---|---|
| **RN-17** | Cada proveedor entrega su lista en su propio formato; el sistema la normaliza a una forma única antes de usarla. |
| **RN-18** | Los códigos de producto de cada proveedor se mapean a un único producto del catálogo maestro. Un código sin mapear no entra al ranking. |
| **RN-19** | Un cambio de precios o stock de un proveedor se refleja en la consulta de recomendados dentro de un retraso máximo definido (a fijar en D5/D6). |
| **RN-20** | El catálogo de ropa (FAV) se trata como un proveedor más. Si su sistema no responde, el sistema sigue funcionando con sus últimas ofertas válidas, informa desde cuándo no se actualizan, y las deja de usar si tienen más de 24 horas ([ADR-009](docs/adr/ADR-009-consumo-proveedor.md)). |

## 3. Historias de usuario y criterios de aceptación

Formato: *Dado* una situación, *cuando* ocurre algo, *entonces* se observa un
resultado. Los valores salen de los datos de demostración (10 proveedores, 30
productos, 170 ofertas) y se comprobaron contra el cálculo de RN-01 a RN-05.

### Capacidad de recomendación

#### HU-01 — Obtener los proveedores más convenientes · implementado
*Como consumidor externo, quiero consultar un producto y una cantidad y recibir los proveedores ordenados por costo total, para elegir a quién comprarle.*

- **CA-01.1** *Dado* el producto `PAN-550W-MONO`, *cuando* consulto con `cantidad=10` y `limite=2`, *entonces* recibo `200` con 2 elementos; el primero es **Cordoba Energia SA** con `costo_total` **1654378.92** y el segundo **Fotovoltaica Pampeana SRL** con **2001061.35**.
- **CA-01.2** *Dado* cualquier consulta con más de un resultado, *entonces* los `costo_total` vienen en orden ascendente (RN-05).
- **CA-01.3** *Dado* que no envío `cantidad` ni `limite`, *entonces* se usan 1 y 5: recibo como máximo 5 elementos evaluados para 1 unidad.
- **CA-01.4** *Dado* cada elemento de la respuesta, *entonces* trae `proveedor_id`, `proveedor_nombre`, `precio_unitario`, `descuento_pct`, `descuento_aplicado`, `flete`, `stock_disponible` y `costo_total`.

#### HU-02 — Que el descuento por volumen se aplique solo cuando corresponde · implementado
*Como consumidor externo, quiero que el costo refleje el descuento solo si mi cantidad lo alcanza, para no tomar una decisión con un precio engañoso.*

- **CA-02.1** *Dado* `PAN-550W-MONO` y `cantidad=10`, *entonces* el primer resultado tiene `descuento_aplicado: true` (Cordoba Energia exige 5 unidades o más).
- **CA-02.2** *Dado* `PAN-550W-MONO` y `cantidad=1`, *entonces* el primer resultado es Cordoba Energia SA con `descuento_aplicado: false` y `costo_total` **232573.99** (`189573.99 + 43000` de flete, sin descuento).
- **CA-02.3** *Dado* una oferta sin descuento (`descuento_pct` 0), *entonces* `descuento_aplicado` es `false` para cualquier cantidad.

#### HU-03 — Que solo aparezcan proveedores con stock suficiente · implementado
*Como consumidor externo, quiero ver solo proveedores que puedan entregarme la cantidad que necesito, para no recibir una recomendación imposible de cumplir.*

- **CA-03.1** *Dado* `PAN-550W-MONO` con 10 ofertas y `cantidad=10`, *entonces* aparecen **7** proveedores: quedan fuera los que tienen menos de 10 unidades (stock 0, 7 y 5).
- **CA-03.2** *Dado* cada elemento devuelto, *entonces* su `stock_disponible` es mayor o igual que la `cantidad` pedida.

#### HU-04 — Distinguir "nadie me puede vender" de "el producto no existe" · implementado
*Como consumidor externo, quiero respuestas distintas para un producto sin ofertas y para un SKU inexistente, para decidir correctamente qué mostrar a mi usuario.*

- **CA-04.1** *Dado* `PAN-550W-MONO` y `cantidad=400` (ningún proveedor tiene tanto stock), *entonces* recibo `200` con `[]`.
- **CA-04.2** *Dado* `MON-WIFI`, que existe pero no tiene ofertas, *entonces* recibo `200` con `[]`.
- **CA-04.3** *Dado* el SKU `NOPE`, *entonces* recibo `404` con `code: PRODUCTO_NO_ENCONTRADO` y `Content-Type: application/problem+json`.

#### HU-05 — Recibir un error claro cuando mi pedido es inválido · implementado
*Como consumidor externo, quiero que un parámetro mal formado me devuelva un error que yo pueda interpretar, para corregir mi llamada.*

- **CA-05.1** *Dado* que no envío `producto`, *entonces* recibo `400` con `code: PARAMETRO_INVALIDO`.
- **CA-05.2** *Dado* `cantidad=0`, `cantidad=-3` o `cantidad=abc`, *entonces* recibo `400 PARAMETRO_INVALIDO`.
- **CA-05.3** *Dado* `limite=51`, *entonces* recibo `400 PARAMETRO_INVALIDO`; con `limite=50`, *entonces* recibo `200`.
- **CA-05.4** *Dado* cualquier error, *entonces* el cuerpo trae `type`, `title`, `status`, `detail`, `code`, `instance` y `request_id`.

### Acceso y operación

#### HU-06 — Acceso solo con clave · implementado
*Como operador de la plataforma, quiero que solo quien tenga una clave pueda consultar la capacidad, para poder identificar y revocar a cada consumidor.*

- **CA-06.1** *Dado* que no envío `X-API-Key`, *entonces* recibo `401 NO_AUTORIZADO`.
- **CA-06.2** *Dado* una clave que no está en la lista configurada, *entonces* recibo `401 NO_AUTORIZADO`.
- **CA-06.3** *Dado* una clave válida, *entonces* la consulta se resuelve con normalidad.
- **CA-06.4** *Dado* que el cliente pega directo al servicio en lugar de al gateway, *entonces* la clave también se exige (se valida en los dos).

#### HU-07 — Encontrar una request puntual cuando algo falla · implementado
*Como operador de la plataforma, quiero un identificador que viaje con cada request, para encontrarla en los logs y en la traza cuando un consumidor reporte un problema.*

- **CA-07.1** *Dado* que envío `X-Request-ID: pedido-paneles-0042`, *entonces* la respuesta lo devuelve igual en el header.
- **CA-07.2** *Dado* que no envío `X-Request-ID`, *entonces* el sistema genera uno de 32 hexadecimales y lo devuelve.
- **CA-07.3** *Dado* una request que pasó por el gateway, *entonces* el `request_id` y el `trace_id` aparecen en la línea de log del gateway **y** en la del servicio, y la traza en Jaeger tiene cuatro spans, hasta la consulta SQL.
- **CA-07.4** *Dado* un error, *entonces* el `request_id` del cuerpo coincide con el header `X-Request-ID`.

#### HU-08 — Que la capacidad siga disponible si cae una réplica · implementado
*Como consumidor externo, quiero que la consulta siga respondiendo aunque falle una instancia del servicio, para no depender de un único proceso.*

- **CA-08.1** *Dado* las dos réplicas sanas, *cuando* hago varias consultas, *entonces* las atienden las dos (el reparto es parejo).
- **CA-08.2** *Dado* que detengo una réplica, *entonces* mis consultas siguen devolviendo `200` por la otra.
- **CA-08.3** *Dado* que detengo las dos, *entonces* recibo `502 NO_DISPONIBLE`, y el `readyz` del gateway pasa a 503.
- **CA-08.4** *Dado* que vuelvo a levantar la réplica, *entonces* vuelve al reparto sin reiniciar el gateway.

#### HU-09 — Una falla de la base no debe colgar al consumidor · implementado
*Como consumidor externo, quiero un error rápido y explícito si el sistema no puede consultar sus datos, para poder reintentar o mostrar un aviso.*

- **CA-09.1** *Dado* que la base no responde, *cuando* consulto, *entonces* recibo `503 NO_DISPONIBLE` en no más de 2 segundos más el tiempo de red.
- **CA-09.2** *Dado* que el servicio no contesta dentro de 3 segundos, *entonces* el gateway devuelve `504 NO_DISPONIBLE`.
- **CA-09.3** *Dado* un `NO_DISPONIBLE`, *entonces* repetir la misma consulta es seguro (RN-12).

#### HU-10 — Levantar todo con un solo comando · implementado
*Como integrante del equipo o evaluador, quiero poner el sistema en marcha con un procedimiento único y documentado, para no depender de configuraciones de una máquina en particular.*

- **CA-10.1** *Dado* un clon limpio del repositorio y un `.env` copiado de `.env.example`, *cuando* ejecuto `docker compose up -d --build`, *entonces* los cinco contenedores quedan en estado sano y la capacidad responde en `http://localhost:8080`.
- **CA-10.2** *Dado* el repositorio, *entonces* no contiene claves ni contraseñas reales (solo los valores de desarrollo de `.env.example`).

### Stock, reposición e importación (propuestas)

#### HU-11 — Importar la lista de precios de un proveedor · propuesto
*Como operador de compras, quiero subir la lista de un proveedor en su propio formato, para que sus precios entren al comparador.*

- **CA-11.1** *Dado* una lista válida en el formato de un proveedor, *cuando* la importo, *entonces* sus ofertas aparecen en el ranking dentro del retraso máximo definido (RN-19).
- **CA-11.2** *Dado* una lista con filas inválidas, *entonces* las válidas se importan y las inválidas quedan informadas, sin perder el resto.
- **CA-11.3** *Dado* la misma lista importada dos veces, *entonces* no se duplican las ofertas.

#### HU-12 — Unificar los códigos de los proveedores en un catálogo · propuesto
*Como operador de compras, quiero que los códigos distintos de cada proveedor correspondan a un mismo producto, para compararlos entre sí.*

- **CA-12.1** *Dado* dos proveedores que llaman distinto al mismo producto, *entonces* ambos aparecen en el mismo ranking.
- **CA-12.2** *Dado* un código sin mapear, *entonces* no entra al ranking y queda señalado para revisión (RN-18).

#### HU-13 — Reservar stock sin sobreventa · propuesto
*Como operador de compras, quiero reservar unidades de mi stock sabiendo que nunca voy a reservar más de las que hay.*

- **CA-13.1** *Dado* un stock de 10 y dos reservas simultáneas de 6, *entonces* una se acepta y la otra se rechaza; el stock nunca queda negativo (RN-13).
- **CA-13.2** *Dado* la misma reserva repetida por un reintento, *entonces* se aplica una sola vez.

#### HU-14 — Reponer automáticamente lo que baja del mínimo · propuesto
*Como operador de compras, quiero que el sistema detecte el faltante y proponga una orden con el proveedor más conveniente.*

- **CA-14.1** *Dado* un producto cuyo stock baja de su mínimo, *entonces* se genera una orden en estado `BORRADOR` con el proveedor de menor costo total para la cantidad a reponer (RN-14, RN-16).
- **CA-14.2** *Dado* una orden abierta para ese producto, *entonces* no se genera otra.
- **CA-14.3** *Dado* una orden, *entonces* solo puede pasar a `SOLICITADA` desde `BORRADOR`; a `CONFIRMADA` o `VENCIDA` desde `SOLICITADA`; y a `EN_REVISION` desde `SOLICITADA` o `VENCIDA` (RN-15).

#### HU-15 — Comprar al catálogo de ropa como a un proveedor más · propuesto
*Como operador de compras, quiero que el catálogo del grupo de ropa participe del ranking y del stock, para comparar sus precios con los demás.*

- **CA-15.1** *Dado* el catálogo de ropa disponible, *entonces* sus productos aparecen entre los candidatos del ranking y su compra impacta en el stock.
- **CA-15.2** *Dado* que su sistema no responde, *entonces* el ranking sigue funcionando con sus últimas ofertas válidas, el usuario ve desde cuándo no se actualizan, y pasadas 24 horas quedan fuera del ranking (RN-20).
- **CA-15.3** *Dado* un cambio incompatible en su contrato, *entonces* un test de contrato lo detecta.

#### HU-16 — Buscar productos y proveedores con filtros · propuesto
*Como operador de compras, quiero buscar por texto y filtrar y ordenar los resultados, para encontrar rápido lo que necesito.*

- **CA-16.1** *Dado* una búsqueda, *entonces* los resultados vienen paginados, con al menos un filtro del dominio (por ejemplo, categoría) y un orden.
- **CA-16.2** *Dado* un cambio en los datos, *entonces* aparece en la búsqueda dentro del retraso máximo definido en D6.

#### HU-17 — Usar el sistema desde una interfaz web · propuesto
*Como operador de compras, quiero una interfaz web con ingreso, búsqueda, detalle y la acción principal, para no depender de herramientas técnicas.*

- **CA-17.1** *Dado* un usuario con sesión, *entonces* puede iniciar sesión, buscar, ver el detalle de un producto con su ranking y ver sus órdenes de reposición.
- **CA-17.2** *Dado* que la capacidad de un proveedor falla, *entonces* la interfaz informa qué pasó en lugar de quedar en blanco.

## 4. Requisitos no funcionales

Los que ya se cumplen se comprueban en las secciones de arriba; los demás son
criterios de aceptación del sistema completo.

| ID | Requisito | Estado |
|---|---|---|
| **RNF-01** | **Entrada única:** todo el tráfico externo pasa por el gateway. | implementado |
| **RNF-02** | **Un solo comando** levanta el sistema y no hay secretos en el repositorio. | implementado |
| **RNF-03** | **Observabilidad básica:** logs JSON correlacionados con `request_id` y `trace_id`, y una traza distribuida del flujo principal. | implementado |
| **RNF-04** | **Tests:** unitarios sobre las reglas de negocio e integración contra una base real. | implementado para `proveedores-service` |
| **RNF-05** | **Observabilidad completa:** métricas, tablero y alerta. | propuesto |
| **RNF-06** | **Control de tráfico** en el gateway (límite de requests por consumidor). | propuesto |
| **RNF-07** | **Test de carga** documentado, con capacidad y límite principal identificados. | propuesto |
| **RNF-08** | **Resiliencia:** al menos un mecanismo de protección frente a la falla de una dependencia, demostrado con una caída provocada. | propuesto |

## 5. Fuera de alcance (por ahora)

- Compras reales ni pagos: el sistema **recomienda**, no ejecuta la compra.
- Gestión de usuarios y permisos más allá de una clave por consumidor y el login de
  la interfaz.
- Cálculo de impuestos. El costo final considera precio, descuento por volumen y
  flete.
- Más de una moneda: el contrato v1 no declara moneda.

## 6. Trazabilidad

| Historia | Reglas | Código o documento |
|---|---|---|
| HU-01 a HU-05 | RN-01 a RN-08, RN-11 | `services/proveedores-service/internal/domain`, `internal/adapters/http`; contrato en [`docs/contracts/`](docs/contracts/README.md) |
| HU-06 | RN-09 | `conAPIKey` en el gateway y en el servicio |
| HU-07 | RN-10 | [`docs/observabilidad/README.md`](docs/observabilidad/README.md), [ADR-011](docs/adr/ADR-011-observabilidad.md) |
| HU-08 | RN-12 | `services/api-gateway/internal/gateway/balanceador.go`, [ADR-012](docs/adr/ADR-012-balanceo-de-carga.md) |
| HU-09 | RN-11, RN-12 | timeouts del gateway y del adaptador de PostgreSQL |
| HU-10 | — | `docker-compose.yml`, `.env.example` |
| HU-11 a HU-17 | RN-13 a RN-20 | sin código todavía; ver [ADR-001](docs/adr/ADR-001-limites-de-los-servicios.md) |
