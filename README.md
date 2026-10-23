# Comparador de proveedores

Sistema de microservicios que **compara proveedores por costo final y recomienda a
quién comprar**. Trabajo Práctico Integrador de Arquitectura de Software 2026
(Universidad Católica de Córdoba, Facultad de Ingeniería).

## Qué hace

Los proveedores publican precios con descuentos por volumen, fletes y stock
distintos. Dado un producto y una cantidad, el sistema calcula cuánto costaría
comprarle a cada proveedor, descarta a los que no tienen stock suficiente y
devuelve los más convenientes, de menor a mayor costo.

```
costo_total = precio_unitario × cantidad × (1 − descuento_pct/100) + flete
```

El descuento aplica solo si la cantidad alcanza el mínimo que fija el proveedor.

**Integración con otros grupos**

- **Publicamos** esta consulta al **grupo de paneles solares**, que la usa para
  elegir a quién comprarle. Contrato en [`docs/contracts/`](docs/contracts/README.md).
- **Consumimos** el catálogo del **grupo de ropa**, que entrará como un proveedor
  más. *Todavía no está integrado.*

## Flujo principal

```
consumidor ──► api-gateway ──► proveedores-service (2 réplicas) ──► PostgreSQL
 (X-API-Key)   valida clave      calcula costo total y ranking       ofertas, precios
               reparte carga
```

1. El consumidor pide `GET /api/v1/proveedores/recomendados` con su clave.
2. El gateway valida la clave, le asigna un `X-Request-ID` y elige una réplica sana.
3. El servicio consulta las ofertas del producto, descarta las que no tienen stock,
   calcula el costo total y ordena.
4. La respuesta vuelve con el `X-Request-ID`, y la misma request se puede seguir en
   los logs y en Jaeger.

## Estado del proyecto

Entrega 2 (23/10/2026). Lo que existe y lo que falta, sin mezclarlos:

| Componente | Estado |
|---|---|
| `api-gateway`: entrada única, API key, `X-Request-ID`, balanceo entre réplicas, trazas | ✅ implementado |
| `proveedores-service`: ranking de proveedores, PostgreSQL, tests unitarios y de integración | ✅ implementado |
| Logs JSON correlacionados y traza distribuida (gateway → servicio → SQL) en Jaeger | ✅ implementado |
| Contrato de la capacidad (OpenAPI v1) y documentación para el consumidor | ✅ implementado |
| `stock-service`, `import-service`, `catalog-service` | 📝 diseñado, sin código |
| MongoDB, mensajería, búsqueda, caché | 📝 diseñado o por decidir, sin código |
| Frontend web | 📝 pendiente |
| Métricas, tablero y alertas | 📝 pendiente |
| Despliegue en la nube (URL pública) | 📝 pendiente |

El detalle y las limitaciones conocidas están en
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#11-limitaciones-conocidas-y-deuda-técnica).

## Cómo ejecutarlo

### Requisitos

- **Docker** con Docker Compose, **corriendo** (en Windows, Docker Desktop abierto).
- **Git**.
- Puertos libres en tu máquina: `8080` (gateway) y `16686` (Jaeger). Postgres no
  publica ningún puerto, así que no choca con uno que ya tengas.

No hace falta tener Go instalado para levantar el sistema: se compila dentro de
Docker.

### Levantarlo

```bash
git clone https://github.com/matiasdemetriorufeil/arq-soft-proveedores.git
cd arq-soft-proveedores
cp .env.example .env
docker compose up -d --build
```

En PowerShell, el segundo paso es `Copy-Item .env.example .env`.

La primera vez tarda unos minutos porque descarga las imágenes y compila. Cuando
termina:

```bash
docker compose ps
```

Tienen que aparecer **cinco contenedores** (`postgres`, `jaeger`,
`proveedores-service-1`, `proveedores-service-2` y `api-gateway`) en estado
`healthy`. Las migraciones y los datos de demostración los aplica el propio
servicio al arrancar: no hay que cargar nada a mano.

### Comprobar que funciona

```bash
curl -H "X-API-Key: clave-de-desarrollo" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

En PowerShell conviene usar `curl.exe` (en PowerShell, `curl` es un alias de otro
comando) y la tilde invertida para partir líneas:

```powershell
curl.exe -H "X-API-Key: clave-de-desarrollo" `
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=2"
```

Tiene que devolver dos proveedores; el primero, **Cordoba Energia SA**, con
`costo_total` 1654378.92.

### Configuración

Todo se configura con variables de entorno en el archivo `.env` (copia de
[`.env.example`](.env.example), que no se sube al repositorio):

| Variable | Para qué sirve | Valor por defecto |
|---|---|---|
| `API_KEYS` | Claves aceptadas, separadas por coma | `clave-de-desarrollo` |
| `PUERTO_GATEWAY` | Puerto del host por el que entra todo | `8080` |
| `PUERTO_JAEGER` | Puerto del host de la UI de trazas | `16686` |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | Credenciales de la base (solo red interna) | valores de desarrollo |
| `TIMEOUT_UPSTREAM` | Cuánto espera el gateway a una réplica antes de devolver 504 | `3s` |
| `INTERVALO_SALUD` | Cada cuánto el gateway sondea a las réplicas | `5s` |

Las claves y contraseñas de `.env.example` son solo para desarrollo local. **No se
suben secretos reales al repositorio.**

## Manual de uso

### 1. Consultar los proveedores recomendados

`GET /api/v1/proveedores/recomendados` con el header `X-API-Key`.

| Parámetro | Obligatorio | Por defecto | Qué es |
|---|---|---|---|
| `producto` | sí | — | SKU del producto (ver la lista de abajo). |
| `cantidad` | no | `1` | Unidades que se quieren comprar. |
| `limite` | no | `5` | Cuántos proveedores devolver (máximo 50). |

Cada proveedor de la respuesta trae:

| Campo | Significado |
|---|---|
| `proveedor_nombre` | Quién lo ofrece. |
| `precio_unitario` | Precio por unidad, sin descuento. |
| `descuento_pct` / `descuento_aplicado` | Descuento que ofrece y si, **para esta cantidad**, se alcanzó el mínimo. |
| `flete` | Costo de envío, fijo por pedido. |
| `stock_disponible` | Unidades que tiene el proveedor. |
| `costo_total` | **Lo que cuesta comprar esa cantidad.** Es el que ordena la lista. |

**Cómo interpretarlo:** el primero de la lista es el más barato para tu cantidad.
Cambiar la cantidad puede cambiar el ranking, porque el descuento y el stock
dependen de ella. Por ejemplo, para `PAN-550W-MONO`:

- con `cantidad=1` el primero es Cordoba Energia SA a **232573.99** (no alcanza el
  mínimo de descuento, que son 5 unidades);
- con `cantidad=10` sigue primero, a **1654378.92**, ahora con el 15 % aplicado;
- con `cantidad=400` la respuesta es `[]`: ningún proveedor tiene tanto stock.

Un `[]` significa "ningún proveedor puede entregarte esa cantidad"; **no es un
error**. El producto no existe solo si recibís un `404`.

### 2. Productos disponibles (datos de demostración)

El sistema trae 10 proveedores, 30 productos y 170 ofertas de ejemplo:

| Categoría | SKU |
|---|---|
| Paneles | `PAN-450W-MONO`, `PAN-550W-MONO`, `PAN-600W-BIF`, `PAN-330W-POLI`, `PAN-100W-FLEX` |
| Inversores | `INV-3KW-ONG`, `INV-5KW-ONG`, `INV-5KW-HIB`, `INV-10KW-TRI`, `MIC-800W` |
| Baterías | `BAT-LFP-5KWH`, `BAT-LFP-10KWH`, `BAT-GEL-200AH` |
| Reguladores | `REG-MPPT-60A`, `REG-PWM-30A` |
| Cableado | `CAB-SOLAR-4MM`, `CAB-SOLAR-6MM`, `CON-MC4-PAR`, `CON-MC4-Y` |
| Estructuras | `EST-ALU-TECHO`, `EST-ALU-PISO`, `RIEL-ALU-2M`, `GRAP-MED`, `GRAP-FIN` |
| Protecciones | `PROT-DC-1000V`, `FUS-DC-15A`, `SECC-DC-32A`, `TAB-STRING-2E` |
| Medición | `MED-BIDIR`, `MON-WIFI` |

`PAN-550W-MONO` tiene 10 ofertas y sirve para probar `limite`. `MON-WIFI` existe
pero no tiene ofertas: devuelve `[]`.

### 3. Qué pasa cuando algo sale mal

Los errores vienen en `application/problem+json` con un campo `code`:

| Si ves… | Qué pasó | Qué hacer |
|---|---|---|
| `400 PARAMETRO_INVALIDO` | Falta `producto`, o `cantidad`/`limite` no son enteros válidos. | Corregir la consulta. |
| `401 NO_AUTORIZADO` | Falta la clave o no es válida. | Revisar `X-API-Key` y `API_KEYS` en `.env`. |
| `404 PRODUCTO_NO_ENCONTRADO` | Ese SKU no existe. | Revisar el SKU. |
| `404 RUTA_NO_ENCONTRADA` | Esa ruta no la expone el sistema. | Revisar la URL. |
| `502`, `503` o `504 NO_DISPONIBLE` | No se pudo consultar (réplicas, base o tiempo agotado). | Reintentar en unos segundos. |

Ejemplo de un `404`:

```bash
curl -i -H "X-API-Key: clave-de-desarrollo" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=NOPE"
```

El detalle de cada error está en el
[contrato](docs/contracts/README.md#errores).

### 4. Seguir una request en logs y trazas

Cada respuesta trae un header `X-Request-ID` (o el que hayas mandado vos). Con ese
valor se encuentra la request en los logs, y de ahí se llega a la traza.

```bash
curl -i -H "X-API-Key: clave-de-desarrollo" -H "X-Request-ID: prueba-1" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=INV-5KW-HIB"

docker compose logs api-gateway proveedores-service-1 proveedores-service-2 | grep prueba-1
```

En PowerShell: `... | Select-String "prueba-1"`.

Vas a ver una línea JSON del gateway y otra de la réplica que atendió, con el mismo
`request_id` y el mismo `trace_id`. Ese `trace_id` se pega en Jaeger
(<http://localhost:16686> → **Lookup by Trace ID**) y muestra la traza completa:
gateway → servicio → consulta SQL. Más detalle en
[`docs/observabilidad/README.md`](docs/observabilidad/README.md).

### 5. Ver el balanceo y la tolerancia a fallas

El gateway reparte las consultas entre las dos réplicas. Para verlo, hacé varias
consultas y mirá cuál las atendió:

```bash
docker compose logs proveedores-service-1 proveedores-service-2 | grep '"msg":"request"'
```

Para provocar una caída:

```bash
docker compose stop proveedores-service-1
# las consultas siguen respondiendo 200 por la otra réplica
docker compose start proveedores-service-1
# vuelve al reparto sola, en pocos segundos
```

Con las dos detenidas, la capacidad responde `502 NO_DISPONIBLE`.

### 6. Detener y reiniciar

```bash
docker compose down        # detiene y borra los contenedores; conserva los datos
docker compose down -v     # además borra la base: el próximo arranque parte de cero
```

## Tests

**Tests unitarios** (no necesitan Docker ni base de datos; requieren Go 1.26 o
superior):

```bash
cd services/proveedores-service && go vet ./... && go test ./... -cover
cd ../api-gateway && go test ./... -cover
```

**Tests de integración** contra un PostgreSQL real (levanta un contenedor propio;
**necesita Docker corriendo**):

```bash
cd services/proveedores-service
go test -tags=integration ./internal/adapters/postgres/ -v
```

Si falla con un error del contenedor *reaper*, ver la explicación en
[`services/proveedores-service/README.md`](services/proveedores-service/README.md#tests).

## Problemas frecuentes

| Síntoma | Causa y solución |
|---|---|
| `port is already allocated` al levantar | El `8080` o el `16686` está ocupado. Cambiá `PUERTO_GATEWAY` o `PUERTO_JAEGER` en `.env` y volvé a correr `docker compose up -d`. |
| `falta API_KEYS en .env` | No copiaste `.env.example` a `.env`. |
| `Cannot connect to the Docker daemon` | Docker no está corriendo: abrí Docker Desktop. |
| `docker compose ps` muestra un contenedor `starting` | Esperá: la base y las réplicas tardan hasta ~1 minuto en quedar sanas la primera vez. |
| `401` aunque mandás la clave | La clave tiene que estar en `API_KEYS` de `.env`; si la cambiaste, reiniciá con `docker compose up -d`. |
| `git` no se reconoce en la terminal de VS Code | Instalaste Git con VS Code abierto: cerrá VS Code por completo y volvé a abrirlo. |

## Cómo acceder a la parte desplegada

**Todavía no hay una versión desplegada en la nube.** El sistema corre solo en
local. La URL pública se agregará acá y en
[`docs/contracts/README.md`](docs/contracts/README.md) cuando exista.

## Estructura del repositorio

```
├── README.md · SPEC.md
├── docker-compose.yml · .env.example
├── services/
│   ├── api-gateway/          entrada única (Go)
│   └── proveedores-service/  ranking de proveedores (Go, hexagonal, PostgreSQL)
├── pkg/observabilidad/       logger JSON con trace_id y trazas OpenTelemetry
└── docs/
    ├── ARCHITECTURE.md
    ├── adr/                  decisiones arquitectónicas
    ├── contracts/            contrato de la capacidad (OpenAPI + README)
    └── observabilidad/       cómo reproducir la traza
```

## Documentación

| Documento | Qué contiene |
|---|---|
| [`SPEC.md`](SPEC.md) | Funcionalidades, reglas de negocio y criterios de aceptación. |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Diagramas de contexto y de contenedores, servicios, datos, comunicaciones, despliegue y limitaciones. |
| [`docs/adr/`](docs/adr/) | Una decisión arquitectónica por archivo, con opciones, razones, consecuencias y medición. |
| [`docs/contracts/README.md`](docs/contracts/README.md) | Contrato de la capacidad que publicamos, con ejemplos y errores. |
| [`docs/contracts/openapi.yaml`](docs/contracts/openapi.yaml) | El mismo contrato en OpenAPI 3.0.3. |
| [`docs/observabilidad/README.md`](docs/observabilidad/README.md) | Logs correlacionados y traza distribuida. |
| [`services/api-gateway/README.md`](services/api-gateway/README.md) | Configuración, balanceo y errores del gateway. |
| [`services/proveedores-service/README.md`](services/proveedores-service/README.md) | Configuración, estructura y tests del servicio. |

## Cómo trabajamos

- `main` siempre levanta con `docker compose up`.
- Cada integrante trabaja en su rama, abre un Pull Request que revisa otro
  integrante y se hace *squash merge*.
- Cada entrega queda marcada con un tag (`entrega-2`).
- Las decisiones se registran como ADR durante el diseño y la implementación, y se
  actualizan en cada entrega.
