# Lo que FAV necesita para conectarse a nosotros

Hoja de entrega para el **grupo de ropa (FAV)**. Nosotros consumimos su catálogo y le
hacemos pedidos de reposición; lo único que FAV necesita de nuestro lado es **dónde
avisarnos cuando un pedido cambia de estado** y **cómo firmar ese aviso**.

No necesita clave de API nuestra: el aviso se autentica con la firma.

> **Estado:** la especificación de abajo no está escrita de memoria. Es la que verifica
> el código (`services/stock-service/internal/fav/aviso.go`, probada en `aviso_test.go`
> y `contrato_test.go`) y **se comprobó el 10/11 mandándole al servicio ocho avisos
> firmados de verdad**: firma correcta, repetido, firma inválida, `t` vencido,
> `eventoId` que no es UUID, cuerpo incompleto, cuerpo no JSON y header que no
> coincide. Los ocho dieron el código que figura en la tabla de la sección 4.
>
> **La URL ya existe y está abajo.** Lo único que falta es que FAV nos entregue su
> URL, su clave y el secreto de firma (sección 5).

## 1. A dónde nos avisa

```
POST http://20.226.84.78:8081/webhooks/fav
Content-Type: application/json
```

**Ya está escuchando** (desplegado el 11/11): un `POST` sin firma devuelve `401`,
que es la señal de que el receptor está arriba y pidiendo la firma.

Tres cosas importantes:

- **Es `stock-service` directo, no el gateway.** El gateway no publica esta ruta a
  propósito ([ADR-009](../adr/ADR-009-consumo-proveedor.md)): el aviso no lleva clave
  de API y no tiene por qué gastar el límite de tráfico de nadie. Es otro puerto, no
  otro path del `:8080`.
- **El puerto es configurable** (`PUERTO_WEBHOOK_FAV`, 8081 por defecto). El valor
  definitivo va en el mensaje final.
- **Es `http://`, sin TLS.** No tenemos dominio, así que no hay certificado. Está
  anotado como deuda; no lo pide la consigna.

## 2. Cómo se firma

Header **`X-FAV-Firma`**, con el formato que FAV mismo propuso:

```
X-FAV-Firma: t=<unix>,v1=<hex>
```

- `t`: el instante del aviso, en **segundos** Unix.
- `v1`: **HMAC-SHA256** en hexadecimal minúscula de la cadena `"<t>.<cuerpo crudo>"`
  —el `t` como texto, un punto, y el cuerpo **exactamente** como se envió, byte a
  byte—, con el secreto compartido como clave.

En pseudocódigo:

```
t    = 1762819200
mac  = HMAC_SHA256(secreto, "1762819200." + cuerpo_crudo)
v1   = hex(mac)
```

**Dos cosas que rechazan el aviso:**

- Firmar el cuerpo **reserializado** en lugar del que se manda. Si se cambia el orden
  de las claves o el espaciado después de firmar, el HMAC no coincide. Hay que firmar
  los bytes que viajan.
- Un `t` que se aparte más de **5 minutos** de nuestra hora (hacia adelante o hacia
  atrás). Es la ventana anti-reenvío.

El header **`X-FAV-Evento-Id`**, que su contrato declara obligatorio, lo aceptamos y lo
usamos para loguear sin leer el cuerpo. Si viene y **no coincide** con el `eventoId`
del cuerpo, es `400`. Si no viene, no rechazamos el aviso: alcanza el `eventoId` del
cuerpo.

## 3. Cuerpo del aviso

El mismo `pedidoActualizado` de su contrato, sin cambios:

```json
{
  "eventoId": "3b7e9a2c-5d1f-4c8e-a9b0-6f2d1e4c7a8b",
  "tipo": "pedido.actualizado",
  "ocurridoEn": "2026-11-11T00:40:00Z",
  "pedido": {
    "pedidoId": "7d9f2c1e-4b3a-4e8f-9c2d-1a0b3c4d5e6f",
    "referenciaExterna": "ae16985e-349d-46fb-a25b-0533c953500d",
    "estado": "PAGADO"
  }
}
```

- **Obligatorios:** `eventoId`, `pedido.pedidoId` y `pedido.estado`. Sin alguno de los
  tres, `400`.
- **`eventoId` tiene que ser un UUID**, como dice su `format: uuid`. Es la clave de
  idempotencia: si se reenvía el mismo, no pasa nada dos veces. Con otra forma
  devolvemos `400` —no `503`—, así que no hay que reintentarlo.
- **`referenciaExterna`** es el identificador de nuestra orden, el mismo que mandamos
  al crear el pedido. Nos sirve para cruzarlo, pero no es obligatorio.
- **`tipo`** se registra y no decide nada: lo que manda es `pedido.estado`.
- Los campos que no conocemos **se ignoran**, como pide su contrato: pueden agregar
  cosas en una versión menor sin romper nada de nuestro lado.
- Tamaño máximo del cuerpo: 32 KiB.

## 4. Qué respondemos

| Código | Cuándo | ¿Reintentar? |
|---|---|---|
| `200` | Recibido. El cuerpo es `{"recibido":true,"repetido":false}`; `repetido:true` si ese `eventoId` ya había llegado | No |
| `400` | El cuerpo no es JSON válido, le faltan campos, el `eventoId` no es un UUID, es demasiado grande, o `X-FAV-Evento-Id` no coincide con el `eventoId` | **No**: no se arregla reintentando |
| `401` | La firma no valida, o el `t` está fuera de la ventana de 5 minutos | **No**, por el mismo motivo |
| `503` | No pudimos registrarlo (por ejemplo, nuestra base no respondió) | **Sí** |

El `200` llega **antes** de terminar de procesar: el aviso se registra, se responde y
el trabajo sigue en segundo plano. Entra con holgura en los 5 segundos que su contrato
se da antes de reintentar.

**El estado vigente lo decide la consulta, no el aviso**, como pide su contrato. Al
recibir un aviso confirmamos con `GET /v1/pedidos/{pedidoId}` y actuamos sobre **esa**
respuesta: un aviso que dice `PAGADO` cuando la consulta todavía dice
`PENDIENTE_DE_PAGO` no confirma nada. Verificado el 10/11 contra el mock.

**Un aviso perdido no rompe nada.** Cada 15 minutos consultamos los pedidos que
tenemos abiertos, así que el estado se termina sincronizando aunque el webhook nunca
llegue. El webhook lo hace rápido, no posible. Su entrega "al menos una vez" nos sirve
igual: deduplicamos por `eventoId` y el efecto sobre el stock se aplica una sola vez.

## 5. Qué necesitamos de FAV

Tres datos, que van a nuestro `.env` y no al repositorio:

| Qué | Para qué | Variable |
|---|---|---|
| **URL base** desplegada | Bajar el catálogo y crear pedidos | `FAV_URL` |
| **Clave de API** del grupo | Va como `X-API-Key` en cada llamada | `FAV_API_KEY` |
| **Secreto de firma** de los avisos | Verificar el `X-FAV-Firma` de arriba | `FAV_SECRETO_AVISOS` |

Con esos tres valores **no hay que cambiar una línea de código**: el adaptador ya está
escrito contra su OpenAPI v1.1.0 y probado con tests de contrato.

El secreto tiene que ser el mismo a los dos lados. Si FAV ya eligió uno, usamos el
suyo; si no, proponemos uno nosotros.

## 6. Estado

**De nuestro lado está todo.** El sistema está desplegado en
<http://20.226.84.78/> con los 14 contenedores, y el receptor del webhook escucha en
el puerto 8081.

**Falta que FAV entregue los tres datos de la sección 5.** Hasta entonces el circuito
corre contra nuestro mock, que cumple su OpenAPI v1.1.0, y los tests de contrato pasan
igual. Cuando lleguen, se cargan en el `.env` de la VM y se reinicia: **no hay que
cambiar código**.
