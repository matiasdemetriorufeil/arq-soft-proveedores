# Contrato de la capacidad publicada: proveedores recomendados

Esta es la documentación para quien consume la capacidad **sin haber participado
en nuestras decisiones internas**. Con este archivo y el
[`openapi.yaml`](openapi.yaml) alcanza para integrarse y probar.

| | |
|---|---|
| **Capacidad** | Dado un producto (SKU) y una cantidad, devuelve los proveedores que conviene elegir, ordenados por costo total. |
| **Consumidor asignado** | Grupo de paneles solares. |
| **Versión vigente** | **v1** (contrato `1.0.0`). La versión viaja en la ruta: `/api/v1/...`. |
| **Contrato formal** | [`openapi.yaml`](openapi.yaml) (OpenAPI 3.0.3). |
| **Dónde está** | Por ahora, local con `docker compose up` (`http://localhost:8080`). La URL pública se agrega acá cuando la capacidad se despliegue en la nube. |

## En 30 segundos

```bash
curl -H "X-API-Key: <tu-clave>" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

```json
[
  {"proveedor_id":3,"proveedor_nombre":"Cordoba Energia SA","precio_unitario":189573.99,"descuento_pct":15,"descuento_aplicado":true,"flete":43000,"stock_disponible":394,"costo_total":1654378.92},
  {"proveedor_id":6,"proveedor_nombre":"Fotovoltaica Pampeana SRL","precio_unitario":199645.5,"descuento_pct":3,"descuento_aplicado":true,"flete":64500,"stock_disponible":62,"costo_total":2001061.35}
]
```

El primero de la lista es el más barato para esa cantidad.

## Operación

```
GET /api/v1/proveedores/recomendados?producto={sku}&cantidad={n}&limite={k}
Header: X-API-Key: <clave>
```

La capacidad se consume **a través del `api-gateway`**, que es el único puerto
publicado al host (`8080` por defecto). Postgres y las réplicas de
`proveedores-service` viven en la red interna de Docker. La ruta es **la misma**
en el gateway y en el servicio, a propósito: quien nos consume no tiene que
saber que hay un gateway en medio.

### Parámetros

| Parámetro | Obligatorio | Default | Regla |
|---|---|---|---|
| `producto` | sí | — | SKU del producto. Distingue mayúsculas y minúsculas. |
| `cantidad` | no | `1` | Entero mayor a 0. Define el stock exigido y si aplica el descuento por volumen. |
| `limite` | no | `5` | Entero entre 1 y 50. |

### Respuesta `200`

Un array JSON ordenado por `costo_total` ascendente (con `limite` elementos como
máximo). Cada elemento:

| Campo | Tipo | Significado |
|---|---|---|
| `proveedor_id` | entero | Identificador estable del proveedor. |
| `proveedor_nombre` | texto | Nombre del proveedor. |
| `precio_unitario` | número | Precio por unidad, sin descuento ni flete. |
| `descuento_pct` | número (0–100) | Descuento por volumen que ofrece el proveedor. Puede ser mayor a 0 aunque no se haya aplicado. |
| `descuento_aplicado` | booleano | `true` si con esta `cantidad` se alcanzó el mínimo del descuento y ya está incluido en `costo_total`. |
| `flete` | número | Costo de envío, fijo por pedido: **no** se multiplica por la cantidad. |
| `stock_disponible` | entero | Unidades que tiene el proveedor. Siempre es ≥ `cantidad`. |
| `costo_total` | número | Lo que cuesta comprar `cantidad` unidades a este proveedor. |

### Regla de negocio

```
costo_total = precio_unitario × cantidad × (1 − descuento_pct/100) + flete
```

- El descuento se resta **solo si** `cantidad ≥ cantidad_minima_descuento` de la
  oferta (ese mínimo es interno: lo que ve el consumidor es `descuento_aplicado`).
- Las ofertas con `stock_disponible < cantidad` **no aparecen**.
- El resultado se redondea a 2 decimales (mitad hacia arriba).
- **Orden:** `costo_total` ascendente. Si empatan, `proveedor_nombre` ascendente y
  después `proveedor_id`. Es determinista: la misma consulta devuelve el mismo
  orden mientras no cambien los datos.
- Si el producto **existe** pero ninguna oferta tiene stock suficiente, la
  respuesta es `200` con `[]`. Solo un SKU que **no existe** es `404`.

### Cosas que conviene saber

- **Los importes son `number` (coma flotante de doble precisión).** Para sumar o
  comparar dinero, conviene que el consumidor convierta a decimal o a centavos en
  su lado. El contrato v1 no declara moneda: todos los importes están en la misma
  moneda del catálogo.
- **`stock_disponible` es una foto del momento de la consulta.** No es una
  reserva: otro comprador puede llevarse el stock entre la consulta y la compra.
- **Los datos de la demo son de ejemplo.** Los SKU de arriba (`PAN-550W-MONO`,
  `INV-5KW-HIB`, `BAT-LFP-10KWH`, `CAB-SOLAR-4MM`) existen en la carga inicial;
  `MON-WIFI` existe pero no tiene ofertas, así que sirve para probar el `[]`.

## Autenticación

`X-API-Key` en cada request. La valida el gateway y la vuelve a validar el
servicio: el gateway reenvía el header tal cual. Una clave faltante o inválida
devuelve `401 NO_AUTORIZADO`.

**Cómo obtener una clave:** la entrega el grupo proveedor (nosotros). Se agrega a
la variable de entorno `API_KEYS` (lista separada por comas) del gateway y del
servicio. Cada consumidor recibe una clave propia, para poder revocarla sin
afectar a otros. La clave viaja por un header, **no** en la URL.

## Correlación: `X-Request-ID`

Opcional en la request. Si mandás uno válido (ASCII imprimible sin espacios, hasta
128 caracteres) lo respetamos; si falta o no es válido, el gateway genera 32
hexadecimales. **Vuelve siempre en la respuesta**, también en los errores, y
aparece como `request_id` en el cuerpo del error.

Cuando algo falle y nos avisen, **pasennos ese valor**: con él encontramos su
request en nuestros logs y la traza completa. Conviene que lo registren de su
lado en cada llamada.

## Errores

Todos en `application/problem+json` (RFC 9457) con la extensión `code`. **Decidan
qué hacer por `code` y por `status`, no por `detail`**: el texto de `detail` es
para una persona y puede cambiar sin cambiar de versión.

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

| `code` | Status | Lo devuelve | Cuándo | ¿Reintentar? |
|---|---|---|---|---|
| <a id="PARAMETRO_INVALIDO"></a>`PARAMETRO_INVALIDO` | 400 | servicio | Falta `producto`, o `cantidad`/`limite` no son enteros válidos (`limite` máximo 50). | No: corregir la request. |
| <a id="NO_AUTORIZADO"></a>`NO_AUTORIZADO` | 401 | gateway y servicio | Falta `X-API-Key` o no es válida. | No: corregir la clave. |
| <a id="PRODUCTO_NO_ENCONTRADO"></a>`PRODUCTO_NO_ENCONTRADO` | 404 | servicio | El SKU no existe. Un producto que existe pero no tiene ofertas con stock devuelve **200 con `[]`**, no 404. | No: el SKU no existe. |
| <a id="RUTA_NO_ENCONTRADA"></a>`RUTA_NO_ENCONTRADA` | 404 | **solo gateway** | La ruta o el método no los expone el gateway (incluye un `POST` a la ruta de la capacidad, que devuelve 404 y no 405). | No: es un error de integración. |
| <a id="NO_DISPONIBLE"></a>`NO_DISPONIBLE` | 502 · 503 · 504 | gateway y servicio | **503** del servicio si la base no responde (timeout de 2 s). **502** del gateway si ninguna réplica responde. **504** del gateway si la réplica no contestó en el tiempo límite (3 s por defecto). | **Sí**, con espera creciente. |

Los errores del servicio **pasan por el gateway tal cual**: el gateway no los
reinterpreta ni cambia el cuerpo.

### Cómo recomendamos que consuman

- **Timeout propio mayor a 3 s.** El gateway corta a la réplica a los 3 s y
  responde `504 NO_DISPONIBLE`. Si el timeout del consumidor es menor, nunca
  verá ese error: verá un corte de su lado, sin `request_id` para correlacionar.
- **Reintentar solo `NO_DISPONIBLE` (502, 503, 504)**, con espera creciente y un
  tope bajo (por ejemplo, 2 reintentos). Los 4xx no se arreglan reintentando.
  El gateway ya reintenta **una vez** internamente ante una réplica caída, así
  que no hace falta reintentar de inmediato.
- **Decidir de antemano qué ve su usuario** si la capacidad no responde. Un `[]`
  significa "nadie tiene stock suficiente", **no** "falló": no confundirlos.

## Idempotencia

La operación es un `GET` sin efectos: no crea ni modifica nada de nuestro lado.
**Se puede repetir cuantas veces haga falta**, y es seguro reintentar ante
cualquier error de red o `NO_DISPONIBLE`. No usa (ni necesita) un header de
idempotencia. La respuesta puede cambiar entre dos llamadas solo porque
cambiaron los precios o el stock.

## Otras garantías y límites

- **Dos réplicas detrás del gateway.** El reparto es round-robin con sondeo de
  salud; una réplica caída sale del reparto. Para quien consume es transparente,
  pero explica por qué dos requests iguales pueden atenderlas procesos distintos.
  Detalle en [`services/api-gateway/README.md`](../../services/api-gateway/README.md).
- **Sin límite de tasa en v1.** El gateway no limita requests todavía. Les
  pedimos un uso razonable; si agregamos un límite, será un cambio compatible
  que se anuncia acá antes.
- **Disponibilidad.** La capacidad se mantiene operativa hasta el final de la
  evaluación. No tenemos un SLA numérico publicado todavía.
- **Chequeos de salud** (`GET /healthz`, `GET /readyz`, sin clave): sirven para
  saber si el gateway está vivo y si tiene alguna réplica sana. **No son parte de
  la capacidad** y no tienen garantía de versionado.

## Versionado y compatibilidad

La versión mayor viaja en la ruta (`/api/v1/`). El número completo (`1.0.0`) está
en `info.version` del [`openapi.yaml`](openapi.yaml).

**Cambios compatibles (no cambian la ruta, suben la versión menor):**
agregar un campo nuevo a la respuesta, agregar un parámetro opcional, agregar un
`code` de error nuevo, aclarar la documentación.

**Cambios incompatibles (nueva ruta `/api/v2/`):** sacar o renombrar un campo,
cambiar el tipo o el significado de un campo, volver obligatorio un parámetro,
cambiar la regla de cálculo o el orden, cambiar la autenticación.

Reglas que nos comprometemos a cumplir:

1. **No cambiamos v1 de forma incompatible.** Si hace falta, publicamos `/api/v2/`
   y **mantenemos v1 funcionando** hasta que el consumidor migre o termine la
   evaluación.
2. **Avisamos antes de cambiar.** Cualquier cambio (aun compatible) se anota en el
   historial de abajo, con fecha, y se avisa al grupo consumidor.

**Lo que les pedimos al consumir (para que nuestros cambios compatibles no los
rompan):** ignoren los campos de la respuesta que no conozcan, y no dependan del
texto de `detail` ni del orden de las claves del JSON.

### Test de contrato (para el consumidor)

La consigna pide un test que detecte cambios incompatibles. Sugerimos que su test
valide una respuesta real (o la grabada) contra el esquema `Recomendacion` del
[`openapi.yaml`](openapi.yaml), y que compruebe como mínimo:

- el `200` es un array y cada elemento trae los 8 campos con su tipo;
- el array viene ordenado por `costo_total` ascendente;
- el `404` trae `Content-Type: application/problem+json` y `code`
  `PRODUCTO_NO_ENCONTRADO`;
- el `401` trae `code` `NO_AUTORIZADO`.

Si un campo cambia de tipo o desaparece, ese test es el que lo avisa.

## Ejemplos

Todos asumen el sistema levantado localmente y una clave en la variable `CLAVE`.

### Caso feliz

```bash
curl -i -H "X-API-Key: $CLAVE" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

En PowerShell:

```powershell
curl.exe -i -H "X-API-Key: $env:CLAVE" `
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

### Con un identificador propio de correlación

```bash
curl -i -H "X-API-Key: $CLAVE" -H "X-Request-ID: pedido-paneles-0042" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=INV-5KW-HIB"
```

La respuesta trae `X-Request-Id: pedido-paneles-0042`.

### Producto que existe pero nadie ofrece: `200` con `[]`

```bash
curl -H "X-API-Key: $CLAVE" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=MON-WIFI"
```

```json
[]
```

### SKU inexistente: `404 PRODUCTO_NO_ENCONTRADO`

```bash
curl -i -H "X-API-Key: $CLAVE" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=NOPE"
```

```json
{"type":"https://github.com/matiasdemetriorufeil/arq-soft-proveedores/blob/main/docs/contracts/README.md#PRODUCTO_NO_ENCONTRADO","title":"Producto no encontrado","status":404,"detail":"no existe un producto con SKU 'NOPE'","code":"PRODUCTO_NO_ENCONTRADO","instance":"/api/v1/proveedores/recomendados","request_id":"demo-1"}
```

### Parámetro inválido: `400 PARAMETRO_INVALIDO`

```bash
curl -i -H "X-API-Key: $CLAVE" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&limite=51"
```

`detail` indica `'limite' no puede ser mayor a 50`.

### Sin clave: `401 NO_AUTORIZADO`

```bash
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO"
```

### Ejemplo de consumo en Go (con timeout y reintento)

```go
var cliente = &http.Client{Timeout: 5 * time.Second} // mayor al límite de 3 s del gateway

func recomendados(ctx context.Context, sku string, cantidad int) ([]Recomendacion, error) {
	destino := fmt.Sprintf("%s/api/v1/proveedores/recomendados?producto=%s&cantidad=%d",
		base, url.QueryEscape(sku), cantidad)

	var ultimo error
	for intento := 0; intento < 3; intento++ {
		if intento > 0 {
			time.Sleep(time.Duration(intento) * 300 * time.Millisecond) // espera creciente
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, destino, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-API-Key", clave)

		res, err := cliente.Do(req)
		if err != nil {
			ultimo = err // corte de red o timeout propio: reintentable
			continue
		}
		recs, reintentable, err := leer(res)
		if err == nil || !reintentable {
			return recs, err
		}
		ultimo = err
	}
	return nil, ultimo
}

func leer(res *http.Response) (recs []Recomendacion, reintentable bool, err error) {
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return recs, false, json.NewDecoder(res.Body).Decode(&recs)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return nil, true, fmt.Errorf("NO_DISPONIBLE (%d)", res.StatusCode) // reintentable
	default:
		return nil, false, fmt.Errorf("error del contrato (%d)", res.StatusCode) // 4xx: no reintentar
	}
}
```

## Historial de versiones

| Versión | Fecha | Cambio |
|---|---|---|
| `1.0.0` | 2026-10-23 | Primera versión operativa: `GET /api/v1/proveedores/recomendados`, autenticación por `X-API-Key`, errores `problem+json` con cinco `code`. Contrato formal en `openapi.yaml`. |

## Decisiones registradas

Decisiones tomadas al construir la plataforma que afectan a este contrato. Si
alguna no les cierra, se renegocia y se actualiza acá. La decisión de diseño del
contrato (cómo se publica, cómo se versiona) está en
[ADR-008](../adr/ADR-008-contrato-propio.md).

- **2026-10-22 · `RUTA_NO_ENCONTRADA` es un `code` nuevo, propio del gateway.**
  El contrato tenía cuatro códigos y ninguno describe un 404 de una ruta que el
  gateway no expone: `PRODUCTO_NO_ENCONTRADO` le mentiría al cliente, porque no
  se trata de un SKU inexistente. Se evaluó devolver el 404 sin campo `code`,
  pero eso deja un error que el cliente no puede clasificar. Ya está incluido en
  el [`openapi.yaml`](openapi.yaml).
- **2026-10-22 · Un `POST` a la ruta de capacidad devuelve 404, no 405.** El
  gateway tiene un catch-all que atrapa cualquier método, así que no llega a
  emitir "método no permitido". Se dejó así a propósito: el contrato es solo
  `GET` y arreglarlo agregaba ramas sin cliente que las necesite. Está
  documentado en el `openapi.yaml` y arriba.
- **2026-10-22 · La capacidad la atienden dos réplicas detrás del gateway.** El
  reparto es round-robin con sondeo de salud; una réplica caída sale del reparto
  y el `GET` se reintenta una vez en la otra. Para quien nos consume es
  transparente, pero explica por qué dos requests iguales pueden atenderlas
  procesos distintos. Detalle en
  [`services/api-gateway/README.md`](../../services/api-gateway/README.md) y en
  [ADR-012](../adr/ADR-012-balanceo-de-carga.md).
