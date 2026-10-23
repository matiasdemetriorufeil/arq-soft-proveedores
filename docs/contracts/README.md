# Contrato de la capacidad publicada

> **Estado: incompleto.** Este archivo es un entregable del **Integrante 3**, que
> escribe el contrato completo y el `openapi.yaml`. Lo que hay acá lo dejó el
> **Integrante 2** (plataforma) por dos razones concretas:
>
> 1. **Los `type` de todos los errores apuntan a los anclas de este archivo.**
>    Tanto `proveedores-service` como el `api-gateway` devuelven
>    `"type": ".../docs/contracts/README.md#<CODIGO>"`. Sin la tabla de abajo esos
>    links no resuelven.
> 2. **Hay decisiones de plataforma que cambian el contrato** y que quien lo
>    escriba necesita saber. Están al final, con fecha y motivo.
>
> Falta (del Integrante 3): endpoints y parámetros en detalle, ejemplos `curl`,
> esquemas de respuesta, versionado e idempotencia, y el `openapi.yaml` v1.

## Entrada única

La capacidad se consume **a través del `api-gateway`**, no pegándole al servicio.
El gateway es el único puerto publicado al host (`8080` por defecto); Postgres y
las réplicas de `proveedores-service` viven en la red interna de Docker.

```
GET /api/v1/proveedores/recomendados?producto={sku}&cantidad={n}&limite={k}
Header: X-API-Key: <clave>
```

La ruta es **la misma** en el gateway y en el servicio, a propósito: quien nos
consume no tiene que saber que hay un gateway en medio.

## Autenticación

`X-API-Key`, validada con comparación de tiempo constante. **La valida el gateway
y la vuelve a validar el servicio**: el gateway reenvía el header tal cual. Las
claves se configuran por variable de entorno `API_KEYS` (lista separada por
comas) en los dos. Para el grupo de paneles solares se agrega una clave propia a
esa lista.

## Correlación: `X-Request-ID`

Si el cliente manda un `X-Request-ID` válido (ASCII imprimible, hasta 128
caracteres) se respeta; si no, el gateway genera 32 hexadecimales. **Vuelve
siempre en la respuesta**, también en los errores, y viaja al servicio. Es con lo
que el cliente encuentra su request en nuestros logs. Internamente se propaga
además el `traceparent` W3C, que es lo que une la traza en Jaeger.

## Errores

Todos en `application/problem+json` (RFC 9457) con la extensión `code`:

```json
{
  "type": "https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#NO_AUTORIZADO",
  "title": "No autorizado",
  "status": 401,
  "detail": "falta el header X-API-Key o no es válido",
  "code": "NO_AUTORIZADO",
  "instance": "/api/v1/proveedores/recomendados",
  "request_id": "45c4322bdb7e15ef109969397ee17e04"
}
```

| `code` | Status | Lo devuelve | Cuándo |
|---|---|---|---|
| <a id="PARAMETRO_INVALIDO"></a>`PARAMETRO_INVALIDO` | 400 | servicio | falta `producto`, o `cantidad`/`limite` no son enteros válidos (`limite` máximo 50) |
| <a id="NO_AUTORIZADO"></a>`NO_AUTORIZADO` | 401 | gateway y servicio | falta `X-API-Key` o no es válida |
| <a id="PRODUCTO_NO_ENCONTRADO"></a>`PRODUCTO_NO_ENCONTRADO` | 404 | servicio | el SKU no existe. Un producto que existe pero no tiene ofertas con stock devuelve **200 con `[]`**, no 404 |
| <a id="RUTA_NO_ENCONTRADA"></a>`RUTA_NO_ENCONTRADA` | 404 | **solo gateway** | la ruta no la expone el gateway |
| <a id="NO_DISPONIBLE"></a>`NO_DISPONIBLE` | 502 · 503 · 504 | gateway y servicio | 503 del servicio si la base no responde (timeout 2 s); 502 del gateway si ninguna réplica responde; 504 del gateway si la réplica no contestó en `TIMEOUT_UPSTREAM` |

Los errores del servicio **pasan por el gateway tal cual**: el gateway no los
reinterpreta ni cambia el cuerpo.

## Decisiones registradas

Decisiones tomadas al construir la plataforma que afectan a este contrato. Si
alguna no les cierra, se renegocia y se actualiza acá.

- **2026-10-22 · `RUTA_NO_ENCONTRADA` es un `code` nuevo, propio del gateway.**
  El contrato tenía cuatro códigos y ninguno describe un 404 de una ruta que el
  gateway no expone: `PRODUCTO_NO_ENCONTRADO` le mentiría al cliente, porque no
  se trata de un SKU inexistente. Se evaluó devolver el 404 sin campo `code`,
  pero eso deja un error que el cliente no puede clasificar. **Hay que incluirlo
  en el `openapi.yaml`**: el revisor automático compara los ADR y el contrato
  contra el código, y esta es exactamente la clase de diferencia que pregunta en
  el final.
- **2026-10-22 · Un `POST` a la ruta de capacidad devuelve 404, no 405.** El
  gateway tiene un catch-all que atrapa cualquier método, así que no llega a
  emitir "método no permitido". Se dejó así a propósito: el contrato es solo
  `GET` y arreglarlo agregaba ramas sin cliente que las necesite. Si el contrato
  va a documentar métodos, documentar este comportamiento o pedir el cambio.
- **2026-10-22 · La capacidad la atienden dos réplicas detrás del gateway.** El
  reparto es round-robin con sondeo de salud; una réplica caída sale del reparto
  y el `GET` se reintenta una vez en la otra. Para quien nos consume es
  transparente, pero explica por qué dos requests iguales pueden atenderlas
  procesos distintos. Detalle en
  [`services/api-gateway/README.md`](../../services/api-gateway/README.md).
