# Game Day en vivo · guion ensayado

El complemento de la consigna pide que la presentación grupal sea un **Game Day en
vivo**: *"tablero y alertas configurados, con una caída simulada en el momento (propia
y/o del proveedor)"*.

Esto no es el informe de caídas —ese es [`POSTMORTEM.md`](POSTMORTEM.md), hecho el
10/11 sobre el stack local—. **Esto es el guion de lo que se hace delante de los
profes**, con los tiempos medidos en el ensayo del 11/11 **contra la instancia
desplegada**. Evidencia cruda en
[`tests/carga/resultados/2026-11-11-gameday-vivo/`](../tests/carga/resultados/2026-11-11-gameday-vivo/).

## Antes de entrar: el túnel

Grafana y Prometheus **no salen a internet a propósito** ([`NUBE.md`](NUBE.md)): viven
en `127.0.0.1` de la VM. Sin el túnel no hay tablero, y abrirlo en el momento cuesta
dos de los cinco minutos.

```bash
ssh -f -N -L 9090:127.0.0.1:9090 -L 3000:127.0.0.1:3000 azureuser@20.226.84.78
```

Después:

- **Grafana** en <http://localhost:3000> → el tablero de capacidad (tasa, errores,
  latencia y rechazos).
- **Prometheus** en <http://localhost:9090/alerts> → las tres alertas. Es la pantalla
  que conviene tener a la vista: se ve el cambio `inactive → pending → firing`.

Comprobar antes de empezar que las tres digan **`inactive`**. Si alguna quedó
encendida de una prueba anterior, esperar a que baje.

## Escenario A · Caen las dos réplicas (el que enciende la alerta)

Es el único que enciende una alerta en vivo: `SinReplicasSanas` tiene `for: 30s`,
mientras las otras dos tienen `for: 2m` y no llegan en una demo.

```bash
# en la VM
cd ~/arq-soft-proveedores
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml \
  stop proveedores-service-1 proveedores-service-2
```

**Tiempos medidos en el ensayo:**

| Momento | Qué pasa |
|---|---|
| **al instante** | la capacidad devuelve **`502 NO_DISPONIBLE`**, con el `code` del contrato |
| **~12 s** | la alerta pasa a **`pending`** en Prometheus |
| **~43 s** | la alerta pasa a **`firing`** |

**Qué decir mientras se esperan esos 43 segundos** (el silencio es el enemigo):

> "El gateway sondea `/healthz` de cada réplica cada 5 segundos. Al detectar que no
> quedan réplicas sanas deja de reenviar y contesta `502` con el código del contrato,
> en lugar de colgarse esperando. La alerta no salta al primer fallo: tiene una espera
> de 30 segundos para no avisar por un reinicio normal. Eso es `pending`; cuando la
> condición se sostiene, pasa a `firing`."

Mostrar en paralelo, con la clave del consumidor:

```bash
curl -i "http://20.226.84.78/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10" \
  -H "X-API-Key: <clave>"
```

**Restaurar:**

```bash
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml \
  start proveedores-service-1 proveedores-service-2
```

Medido: la capacidad vuelve a responder **5 segundos** después de arrancar los
contenedores, y la alerta vuelve a `inactive` sola.

## Escenario B · Cae la caché (el que no corta nada)

El más seguro: **no deja la capacidad caída**, solo la hace más lenta. Sirve si no se
quiere arriesgar un corte delante de los profes.

```bash
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml stop redis
# ... medir ...
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml start redis
```

**Medido en el ensayo, desde internet:**

| | Latencia |
|---|---|
| con Redis | **121 ms** |
| sin Redis | **168 ms** |
| recuperado | 103 ms |

**Qué decir:** los ~47 ms de diferencia **no son el costo de ir a PostgreSQL**: son el
timeout de 50 ms del decorador de caché, que se paga completo en cada request mientras
Redis no responde. Las respuestas siguen siendo `200` y correctas: la caché es un
acelerador, no una dependencia. Está medido y aceptado como consecuencia en
[ADR-007](adr/ADR-007-cache.md).

## Escenario C · Se cae el proveedor externo (FAV)

El complemento acepta que la caída sea *"propia y/o del proveedor"*. Esta es la del
proveedor, y es la que mejor contesta **"¿qué ve el usuario si el proveedor se cae?"**,
que la consigna pide demostrar.

> **Ojo con el atajo equivocado.** Desconectar `import-service` de la red lo aísla
> también de MongoDB y RabbitMQ: eso no es "se cayó FAV", es "se cayó nuestro
> servicio". Y poner el mock en `caido` **ya no hace nada**, porque el sistema le habla
> al FAV real. La forma correcta es dejar a FAV inalcanzable y nada más:

```bash
cd ~/arq-soft-proveedores
cp .env .env.respaldo-gameday
sed -i 's|^FAV_URL=.*|FAV_URL=http://192.0.2.1:9|' .env   # 192.0.2.0/24 es rango de descarte
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml \
  up -d --no-deps --force-recreate import-service
```

Recrearlo fuerza una sincronización al arrancar, así que **no hay que esperar los 5
minutos del ciclo**. A los ~30 segundos el log muestra:

```
"msg":"no se pudo bajar el catálogo de FAV; siguen valiendo las últimas ofertas"
"breaker":"cerrado"
"msg":"sincronización del catálogo de FAV terminada"
```

```bash
sudo docker compose logs import-service --since 60s | tail -5
```

**Y lo importante, que es lo que se muestra:** el ranking de una prenda de FAV sigue
respondiendo `200` con su oferta, y los productos propios no se enteran de nada.

**Qué decir:** no consultamos a FAV en cada request. `import-service` sincroniza su
catálogo cada 5 minutos a nuestra base, así que una caída suya **congela sus precios,
no nuestra disponibilidad**. El circuit breaker abre después de 3 vueltas fallidas y
deja de llamarlos; si la última importación válida pasa las 24 horas, sus ofertas se
retiran del ranking con un evento, en lugar de quedar vendiendo datos viejos para
siempre.

**Restaurar:**

```bash
cp .env.respaldo-gameday .env && rm .env.respaldo-gameday
sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml \
  up -d --no-deps --force-recreate import-service
```

Medido: la sincronización siguiente termina en `estado: sin_cambios` y el ranking queda
igual que antes.

## Qué escenario elegir

| | Enciende alerta | Corta la capacidad | Cuánto tarda |
|---|---|---|---|
| **A · dos réplicas** | **sí**, a los 43 s | sí, ~1 min | el más lento |
| **B · caché** | no | **no** | inmediato |
| **C · proveedor** | no | **no** | ~30 s |

**Lo que haría:** **A** si hay que mostrar una alerta encendiéndose, porque es el único
que lo logra en vivo. **C** si lo que se quiere contestar es qué ve el usuario cuando
el proveedor se cae. **B** como respaldo si no se quiere arriesgar ningún corte.

Si alcanza el tiempo, **A seguido de C** cubre las dos mitades que menciona el
complemento: la caída propia y la del proveedor.

## Después

Dejar las tres alertas en `inactive` y comprobar que los 14 contenedores están
*healthy*:

```bash
sudo docker compose ps
curl -s -o /dev/null -w "%{http_code}\n" http://20.226.84.78/readyz
```

Verificado después del ensayo del 11/11: 13 servicios *healthy* (Grafana no declara
healthcheck), `readyz` en `200`, la capacidad y el stock real de FAV respondiendo, y
las tres alertas en `inactive`.
