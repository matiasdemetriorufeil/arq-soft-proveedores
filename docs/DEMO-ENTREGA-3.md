# Demo y checklist de la Entrega 3 (6/11) y de la presentación grupal (11 o 13/11)

Qué está y qué no antes del tag, cómo se crea `entrega-3`, cómo se prepara la máquina
y el guion de 5 minutos, con las preguntas probables al final. **Todos los comandos
de abajo se corrieron el 10/11 contra el sistema levantado con `docker compose up`**
y la salida que se describe es la que dieron de verdad.

## 1. Checklist (consigna 6)

**Código**

- [x] Tres servicios de dominio operativos: `proveedores-service`, `import-service`
      y `stock-service`
- [x] Dos tipos de almacenamiento en el flujo: PostgreSQL (dos bases) y MongoDB
- [x] Dos patrones internos reconocibles: hexagonal y en capas
- [x] Capacidad del proveedor (FAV) dentro del flujo principal: su catálogo entra al
      ranking y las reposiciones salen como pedidos
- [x] Evento de dominio por mensajería, consumidor idempotente y DLQ
- [x] Caché en un flujo de lectura, con medición antes y después
- [x] Búsqueda indexada con paginación, filtros, orden y reconstrucción
- [x] Gateway con autenticación (API key y JWT), ruteo y límite de tráfico (`429`)
- [x] Frontend completo, del login a la reserva, hablando solo con el gateway
- [x] Métricas, tablero y tres alertas
- [x] CI que corre los tests en cada push (10 jobs, verde)
- [x] `docker compose up` levanta los 14 contenedores, sin secretos en el repo
- [ ] **Capacidad propia en la nube con URL pública** — es lo único obligatorio que
      falta: el override, el script y la guía están ([`NUBE.md`](NUBE.md)), falta
      crear la VM

**Decisiones (ADR)**

- [x] 13 de los 14 ADR en "aceptado", con medición o test que lo respalde
- [x] [ADR-004](adr/ADR-004-consistencia-y-concurrencia.md) nuevo: consistencia y
      concurrencia de la reserva (D4)
- [ ] [ADR-014](adr/ADR-014-tres-servicios-sin-catalogo.md) sigue en "propuesto":
      necesita que la cátedra confirme que tres servicios sin `catalog-service`
      alcanzan

**Documentación**

- [x] [`POSTMORTEM.md`](POSTMORTEM.md): Game Day con las siete caídas medidas
- [x] `README.md`, `SPEC.md` y `ARCHITECTURE.md` al día con lo que existe
- [x] Contrato propio 1.1.0 (con el `429`) y contrato de FAV consumido en
      `docs/contracts/consumidos/`

**Antes del tag**

- [ ] `docker compose up` desde un clon limpio, en al menos dos computadoras
- [ ] Demo ensayada de punta a punta, en menos de 5 minutos

## 2. Crear el tag

Desde `main` actualizado y con el CI en verde:

```bash
git switch main
git pull
git tag -a entrega-3 -m "Entrega 3: tres servicios, FAV en el flujo, cache medida y Game Day"
git push origin entrega-3
```

Si después del tag hay que corregir algo, se crea `entrega-3.1`; no se mueve un tag
ya publicado.

## 3. Antes de entrar (15 minutos antes)

```bash
git clone https://github.com/matiasdemetriorufeil/arq-soft-proveedores.git demo-e3
cd demo-e3
git checkout entrega-3
cp .env.example .env              # PowerShell: Copy-Item .env.example .env
docker compose up -d --build      # la primera vez tarda varios minutos
docker compose ps                 # los 14 contenedores en (healthy)
```

> **Tres cosas que pueden arruinar la demo, y las tres pasaron de verdad:**
>
> 1. **Un `.env` viejo** hace que `docker compose config` aborte y no levante
>    **nada**. En un clon nuevo no pasa, porque se copia de `.env.example`. En una
>    máquina que ya tenía el repo, sí: hay que copiar las variables nuevas.
> 2. **Si ya había un volumen de PostgreSQL de una entrega anterior**, la base
>    `stock` no se crea: su script solo corre con el volumen vacío. Se arregla con
>    `docker compose down -v` una vez.
> 3. **Puertos ocupados por otro stack de Docker** (8080, 15672, 16686, 8090, 9090,
>    3000). Se cambian en el `.env`, que existe para eso.

Dejar abiertos: una terminal en la raíz, el frontend en <http://localhost:8080>,
Grafana en <http://localhost:3000> y Jaeger en <http://localhost:16686>.

> **En PowerShell** se usa `curl.exe`, no `curl` (que es un alias de
> `Invoke-WebRequest`).

## 4. Guion (5 minutos)

### 4.1 Qué es y cómo se levanta (30 s)

- "Comparador de proveedores con stock propio. Consumimos el catálogo del grupo de
  ropa (FAV) como un proveedor más, y publicamos nuestro ranking al grupo de paneles
  solares."
- `docker compose ps`: **14 contenedores healthy con un solo comando**. Tres
  servicios de dominio, un gateway, dos bases PostgreSQL, MongoDB, RabbitMQ, Redis,
  Meilisearch, Prometheus, Grafana y Jaeger.

### 4.2 El frontend, de punta a punta (1 min)

En <http://localhost:8080>: login con `demo` / `demo-arqsoft` → escribir "remera" →
elegir un resultado → ver el ranking de proveedores → reservar.

- **Lo que hay que señalar:** "remera" no es un producto nuestro. Es ropa de FAV, que
  entró por `import-service`, se publicó como evento y la indexó
  `proveedores-service`. **El consumo del proveedor externo está en el flujo
  principal, no en un costado.**
- El frontend habla **solo con el gateway**: nunca con FAV ni con los servicios.

### 4.3 La misma cosa por el contrato (45 s)

```bash
curl -s "http://localhost:8080/api/v1/proveedores/recomendados?producto=FAV-var-remera-basica-blanca-m&cantidad=5" -H "X-API-Key: clave-de-desarrollo"
```

Devuelve el proveedor 11, "FAV (tienda de ropa)", con su `costo_total`. Un producto
de otro grupo rankeado por nuestra fórmula, con el SKU ya traducido por la capa
anticorrupción.

### 4.4 La operación crítica: reservar sin sobreventa (1 min 15 s)

Es la parte más fuerte de la demo porque es un resultado, no una explicación.

```bash
# 20 reservas simultáneas de 1 unidad sobre un SKU con 5 disponibles
for i in $(seq 1 20); do
  curl -s -o /dev/null -w "%{http_code}\n" -X POST "http://localhost:8080/api/v1/stock/reservas" \
    -H "X-API-Key: clave-de-desarrollo" -H "Content-Type: application/json" \
    -H "Idempotency-Key: demo-$i" -d '{"sku":"BAT-LFP-10KWH","cantidad":1}' &
done | sort | uniq -c
```

Sale **`5 201`** y **`15 409`**. Se entregan exactamente las 5 unidades que había.

- Repetir un `POST` con la **misma** `Idempotency-Key` devuelve la misma reserva, sin
  descontar stock dos veces.
- Es `SELECT … FOR UPDATE` dentro de la transacción
  ([ADR-004](adr/ADR-004-consistencia-y-concurrencia.md)).

### 4.5 La caché, medida (45 s)

```bash
# con Redis arriba
curl -s -o /dev/null -w "%{time_total}s\n" "http://localhost:8080/api/v1/proveedores/recomendados?producto=FAV-var-remera-basica-blanca-m&cantidad=5" -H "X-API-Key: clave-de-desarrollo"
docker compose stop redis
# con Redis caído: sigue respondiendo, desde PostgreSQL
curl -s -o /dev/null -w "%{time_total}s\n" "http://localhost:8080/api/v1/proveedores/recomendados?producto=FAV-var-remera-basica-blanca-m&cantidad=5" -H "X-API-Key: clave-de-desarrollo"
docker compose start redis
```

**6,5 ms contra 56,6 ms**, y el segundo sigue siendo `200` con el dato correcto. Las
series completas antes y después están en
[ADR-007](adr/ADR-007-cache.md); el detalle de por qué son 50 ms exactos, en
[`POSTMORTEM.md`](POSTMORTEM.md#escenario-1--redis-caído).

### 4.6 Apagar FAV (45 s)

```bash
curl -s -X POST http://localhost:8090/_mock/modo -H "Content-Type: application/json" -d '{"modo":"caido"}'
curl -s "http://localhost:8080/api/v1/proveedores/recomendados?producto=FAV-var-remera-basica-blanca-m&cantidad=5" -H "X-API-Key: clave-de-desarrollo"
curl -s -X POST http://localhost:8090/_mock/modo -H "Content-Type: application/json" -d '{"modo":"normal"}'
```

Con FAV caído el ranking **sigue respondiendo en 5 ms** con la última importación
válida. Si queda tiempo, mostrar el log del breaker abriendo en la tercera vuelta
fallida.

### 4.7 Cierre: las decisiones con evidencia (20 s)

- Grafana: tasa, errores y latencia; tres alertas configuradas.
- 13 de 14 ADR en "aceptado", **cada uno con una medición o un test**, no con una
  opinión. El que falta espera una respuesta de la cátedra.
- [`POSTMORTEM.md`](POSTMORTEM.md): siete caídas provocadas y medidas.

## 5. Lo que hay que decir sin que lo pregunten

Dos cosas que es mejor poner sobre la mesa antes de que las encuentren:

1. **No hay URL pública.** La VM no se creó. Está el override del compose, el script
   de instalación y la guía; falta la máquina. Es el único ítem obligatorio sin
   cumplir.
2. **FAV nunca publicó su URL.** Todo el consumo está probado contra `fav-mock`, que
   cumple su OpenAPI v1.1.0, con tests de contrato sobre el catálogo y los pedidos.
   Era el plan B escrito en el plan de la entrega. Con `FAV_URL` en el `.env` se
   apunta al real sin cambiar una línea de código.

## 6. Preguntas probables

**"¿Cómo evitan la sobreventa con dos usuarios a la vez?"**
Bloqueo pesimista: `SELECT … FOR UPDATE` sobre la fila del producto dentro de la
transacción, así la segunda transacción espera y vuelve a leer el disponible ya
descontado. Demostrado con 20 pedidos en paralelo: 5 `201` y 15 `409`
([ADR-004](adr/ADR-004-consistencia-y-concurrencia.md)).

**"¿Y si se repite el mismo pedido por un reintento de red?"**
`Idempotency-Key`: la misma clave devuelve la misma reserva sin descontar de nuevo.
Lo mismo hacia FAV en los pedidos de reposición.

**"¿Qué pasa si se cae Redis?"**
Sigue funcionando: la lectura cae a PostgreSQL. Se paga el timeout de 50 ms del
decorador en cada request, medido: 6,5 ms → 56,6 ms. Está anotado como deuda en el
postmortem, con el arreglo propuesto (un breaker en la caché).

**"¿Y si se cae la base?"**
`503 NO_DISPONIBLE` en 2 s, con `request_id`, sin colgarse y sin datos falsos. Son
los 2 s del timeout de conexión de pgx que ya predecía [ADR-010](adr/ADR-010-resiliencia.md),
medidos en el Game Day. Contra el timeout de 3 s del gateway, el margen es de 1 s:
está anotado como lo próximo a ajustar.

**"¿Cómo garantizan que no se pierde un evento si se cae el broker?"**
Outbox: el cambio se guarda en MongoDB en la misma operación que la importación y se
publica aparte, con confirmaciones. Probado con RabbitMQ apagado: el cambio quedó
`pendiente=1`, y al volver el broker se publicó y el precio nuevo llegó al ranking.

**"¿Por qué no hay `catalog-service`?"**
Porque el mapeo de códigos lo resuelve la capa anticorrupción de `import-service`, y
un servicio más sin dueño de datos propio no agregaba nada
([ADR-014](adr/ADR-014-tres-servicios-sin-catalogo.md)). La consigna pide tres y hay
tres.

**"¿Por qué dos patrones distintos?"**
`proveedores-service` es hexagonal porque su dominio tiene reglas de costo que
conviene aislar de la base y de la caché. `stock-service` es en capas porque su valor
está en la transacción, y una capa de puertos encima de un `FOR UPDATE` solo agregaba
indirección ([ADR-002](adr/ADR-002-arquitectura-interna.md)).
