# ADR-008 — Contrato propio (D8)

- **Estado:** aceptado e implementado (contrato v1 operativo)
- **Fecha:** 2026-10-23
- **Entrega:** 1 (no se presentó en su fecha) y actualización en la 2 (23/10)
- **Decisión de la consigna:** D8. Diseño, publicación, compatibilidad y estrategia de versionado de la capacidad ofrecida a otro grupo.

## Contexto

La consigna (4.1) exige publicar **una capacidad con un contrato formal, versionado y procesable**, en `docs/contracts/`, que otro grupo pueda usar **sin explicaciones privadas**: operaciones, entradas y salidas, errores, autenticación, idempotencia, versión vigente y ejemplos.

Nuestra capacidad es la consulta de **proveedores recomendados**: la consume el grupo de paneles solares para elegir a quién comprar. Es una consulta de lectura, de respuesta inmediata, que ellos necesitan dentro de su flujo de compra.

El grupo consumidor trabaja contra nuestro contrato como contra un sistema que no controla, así que cualquier cambio nuestro puede romperlos. Por eso la compatibilidad y el aviso de cambios son parte del diseño, no un detalle.

## Opciones evaluadas

**Estilo de interacción**

1. **API REST sobre HTTP/JSON, descrita con OpenAPI.** Estándar, se prueba con `curl`, y el grupo consumidor puede validar contra el esquema.
2. **gRPC con Protobuf.** Contrato fuerte y compacto, pero exige herramientas y generación de código en el grupo consumidor, y no se puede probar con `curl`.
3. **GraphQL.** Permite pedir solo los campos necesarios, pero la capacidad tiene una sola consulta con una forma fija; agregaría un lenguaje de consultas sin resolver un problema.
4. **Eventos (AsyncAPI).** Sirve para avisar cambios, no para preguntar "quién conviene ahora" y recibir la respuesta en el mismo flujo.

**Versionado**

1. **Versión en la ruta** (`/api/v1/...`). Visible, cacheable, fácil de probar.
2. **Versión en un header** (`Accept: application/vnd...`). Deja la URL limpia, pero es fácil de olvidar y difícil de ver en un log.
3. **Sin versión.** Más simple hoy, pero cualquier cambio incompatible rompe al consumidor sin aviso.

**Autenticación**

1. **`X-API-Key`.** Simple, una clave por consumidor, revocable.
2. **JWT.** Más completo (identidad, vencimiento), pero necesita un emisor de tokens que no tenemos.
3. **Sin autenticación.** Descartada: es una capacidad que otro grupo consume y hay que poder identificar y revocar.

## Qué elegimos

**REST/JSON descrito con OpenAPI 3.0.3, versión en la ruta, autenticación por `X-API-Key` y errores en `application/problem+json`.**

- **Operación:** `GET /api/v1/proveedores/recomendados?producto={sku}&cantidad={n}&limite={k}`. Es un `GET` sin efectos, por lo tanto **idempotente y seguro de reintentar**.
- **Contrato formal:** [`docs/contracts/openapi.yaml`](../contracts/openapi.yaml), versión `1.0.0`. Documentación para el consumidor en [`docs/contracts/README.md`](../contracts/README.md).
- **Errores:** RFC 9457 con la extensión `code` (cinco valores: `PARAMETRO_INVALIDO`, `NO_AUTORIZADO`, `PRODUCTO_NO_ENCONTRADO`, `RUTA_NO_ENCONTRADA`, `NO_DISPONIBLE`). El consumidor decide por `code` y `status`; el texto `detail` no es parte del contrato.
- **Entrada única:** se consume por el `api-gateway`, con la misma ruta que el servicio, para que el consumidor no dependa de la topología interna.
- **Correlación:** `X-Request-ID` opcional en la entrada y siempre presente en la salida, para que el consumidor nos pase el caso cuando falle.

**Política de compatibilidad y versionado**

- **Cambios compatibles** (campo nuevo en la respuesta, parámetro opcional nuevo, `code` nuevo, aclaración de documentación): no cambian la ruta; suben la versión menor y se anotan en el historial.
- **Cambios incompatibles** (sacar o renombrar un campo, cambiar un tipo o la regla de cálculo, volver obligatorio un parámetro): nueva ruta `/api/v2/`.
- **v1 se mantiene funcionando** mientras el consumidor migra y hasta el final de la evaluación.
- **Todo cambio se avisa** al grupo consumidor y se registra con fecha en el historial de `docs/contracts/README.md`.
- **Al consumidor se le pide** ignorar campos desconocidos y no depender del texto de `detail`, para que nuestros cambios compatibles no lo rompan.

**Qué cambió desde el diseño original:** la Entrega 1 (diseño con mock) no se presentó, así que esta es la primera versión del contrato. Durante la construcción se agregó un quinto código, `RUTA_NO_ENCONTRADA`, propio del gateway, porque un 404 por ruta inexistente no puede reutilizar `PRODUCTO_NO_ENCONTRADO` sin mentirle al cliente (decisión del 22/10, registrada en el README del contrato).

## Por qué

- **Es lo que puede usar otro grupo sin ayuda.** Con `curl` y el `openapi.yaml` alcanza; no hace falta instalar herramientas ni generar código.
- **El contrato es procesable.** Cualquier herramienta de OpenAPI lo valida y el grupo consumidor puede armar su test de contrato (que la consigna les exige) contra el esquema.
- **La ruta versionada hace visible la compatibilidad:** en un log o en una captura de Jaeger se ve contra qué versión se llamó.
- **Una clave por consumidor** permite revocar a uno sin afectar a otros, que es lo mínimo que se espera de una capacidad publicada.
- **`problem+json` con un `code` estable** separa el error que se programa (el `code`) del texto que se lee (`detail`), y deja agregar códigos sin romper a nadie.

## Consecuencias aceptadas

- **`X-API-Key` es una autenticación básica.** No identifica personas ni vence solo; si la clave se filtra, hay que rotarla a mano en el gateway y en el servicio.
- **Sin límite de tasa en v1.** El gateway no limita requests, así que un consumidor que repita llamadas sin control puede saturarnos. Está dicho en el contrato.
- **Los importes son `number` de coma flotante y el contrato no declara moneda.** Está avisado en el contrato; agregar un campo `moneda` sería un cambio compatible.
- **El consumidor tiene que tener un timeout mayor al del gateway (3 s)** para recibir el error `504` en lugar de cortar antes. Lo recomendamos, pero no podemos obligarlo.
- **Mantener `/v1/` mientras exista `/v2/` duplica trabajo.** Lo aceptamos para no romper al consumidor.
- **Solo describimos la operación de consulta.** Los chequeos `/healthz` y `/readyz` aparecen en el OpenAPI como "operación", sin garantía de versionado.
- **Hoy la capacidad corre solo en local.** La URL pública llega con el despliegue en la nube (presentación grupal).

## Con qué lo medimos

Mediciones del 23/10:

- **El contrato es válido y coincide con el código:** `openapi.yaml` pasa la validación de OpenAPI 3.0.3, y los **5** códigos de error del código del gateway y del servicio son los mismos **5** del esquema `Problem` y los **5** anclas del README del contrato.
- **Los ejemplos cumplen el esquema:** los elementos del ejemplo de respuesta `200` validan contra `Recomendacion`.
- **Lo que ve un consumidor al pegarle al sistema real:** 7 respuestas documentadas (`200`, `400`, `401`, `404`, `502`, `503`, `504`), cada una con un ejemplo.
- **Costo de pasar por el gateway:** mediana 1,99 ms y p95 4,92 ms medidos en el gateway (n=23, integrante 2, [ADR-011](ADR-011-observabilidad.md)); comparable con los 1 a 3 ms contra el servicio directo.
- **Lo que todavía no está medido:** si el grupo de paneles solares logra integrarse **solo con la documentación**. Ese es el verdadero criterio de éxito de este ADR y se mide cuando tengan su cliente funcionando.

## Qué nos haría cambiar de idea

- **Si el grupo consumidor necesita pedir varios SKU a la vez**, la forma de la respuesta no alcanza: sería un endpoint nuevo (compatible) o un cuerpo distinto (`/v2/`).
- **Si aparece una necesidad de avisar cambios** (por ejemplo, "bajó el precio") en lugar de consultar, el contrato necesitaría una parte asíncrona descrita con AsyncAPI.
- **Si el consumidor reporta que el `X-API-Key` es un problema** (porque necesita identidad por usuario o vencimiento), pasar a JWT.
- **Si el consumidor pide un cambio incompatible** que beneficia a ambos, publicar `/v2/` y no modificar `/v1/`.
- **Si el grupo consumidor no logra integrarse sin ayuda privada**, el contrato está mal documentado: se corrige la documentación antes que el diseño.
