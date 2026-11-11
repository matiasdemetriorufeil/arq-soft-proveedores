# Despliegue en la nube

Cómo se sube el sistema a una VM con URL pública. Es la **opción A de [ADR-013](adr/ADR-013-capacidad-y-costos.md)**: una sola VM que corre el mismo `docker compose` que usamos en local, con un override que deja afuera solo el gateway.

- **URL pública: <http://20.226.84.78/>** — desplegada el 11/11/2026. VM `arqsoft` de Azure (`Standard_D2s_v4`, 2 vCPU y 8 GB, `brazilsouth`, resource group `arqsoft-rg`), con los **14 contenedores** corriendo. IP estática: no cambia si se apaga la VM.
- **Qué queda expuesto a internet:** solo el `api-gateway` en el puerto 80, que atiende la capacidad (`/api/v1/proveedores/recomendados`), el login, stock y el frontend (`/`).
- **Qué no queda expuesto:** bases, broker, servicios y las consolas de operación (Grafana, Prometheus, Jaeger, RabbitMQ y el control del mock de FAV). Las consolas quedan en `127.0.0.1` de la VM y se ven con un túnel SSH.

## 1. Crear la VM

Cualquier proveedor sirve. Lo que hace falta:

| Requisito | Valor | Por qué |
|---|---|---|
| Sistema | Ubuntu 24.04 LTS | el script de instalación usa `apt` vía `get.docker.com` |
| Tamaño | 2 vCPU y 4 GB de RAM como mínimo | son 14 contenedores; MongoDB, RabbitMQ y Grafana son los que más memoria piden |
| Disco | 20 GB | imágenes, volúmenes y la caché de build de Go |
| Puertos de entrada | **22** (SSH) y **80** (HTTP) | nada más: las consolas van por el túnel SSH. El 8081 del webhook de FAV **se cerró el 11/11**: su contrato 1.2.0 no tiene webhook, así que no había quien lo llamara, y abrirlo dejaba `/api/v1/stock/` accesible sin el límite de tráfico del gateway |

Opciones que no cuestan nada durante la cursada: **Oracle Cloud Free Tier** (VM Ampere A1, hasta 4 vCPU y 24 GB sin cargo), **Google Cloud** (USD 300 de crédito inicial, `e2-medium`) o **Azure for Students** (USD 100 sin tarjeta). En AWS una `t3.medium` sale cerca de USD 30 por mes y la capa gratuita (`t3.micro`, 1 GB) no alcanza.

**Sobre ARM (Ampere A1 de Oracle):** sirve. Se verificó el 10/11 que las ocho imágenes de terceros del compose publican `linux/arm64` —`postgres:16-alpine`, `mongo:9.0`, `redis:7.4-alpine`, `rabbitmq:4.1-management-alpine`, `getmeili/meilisearch:v1.14`, `jaegertracing/all-in-one:1.76.0`, `prom/prometheus:v3.5.0` y `grafana/grafana:12.1.1`— y que nuestros cuatro Dockerfiles no fijan `GOARCH`, así que Go compila para la arquitectura de la VM; `golang:1.26-alpine` y `distroless/static-debian12` también son multi-arquitectura. No hay que cambiar nada para desplegar en ARM.

**El problema real de Oracle no es ARM, es el stock:** las instancias Ampere están casi siempre agotadas y la consola responde *"Out of host capacity"*, incluso pidiendo 1 OCPU. Si pasa, hay tres salidas, en este orden:

1. **Probar la otra forma Always Free: `VM.Standard.E2.1.Micro`** (AMD, 1/8 OCPU y 1 GB). Es una familia distinta de Ampere y suele tener lugar. Con 1 GB no entran los 14 contenedores, pero sí el despliegue mínimo de la sección 5, que **mide 34 MB en total**.
2. **Otro proveedor.** Azure for Students (USD 100 sin tarjeta) o DigitalOcean (USD 200 por el GitHub Student Pack) dan una VM de 2 vCPU y 4 GB en minutos, y ahí corre el sistema completo. Lo que hicimos: ver abajo.

### Azure for Students: las tres trampas

Se desplegó así el 11/11. Tres cosas hicieron perder tiempo y conviene saberlas de antemano:

1. **Los proveedores de recursos no vienen registrados** en una suscripción nueva. Mientras `Microsoft.Compute` dice `NotRegistered`, `az vm list-usage` devuelve **vacío** en lugar de un error, así que parece que no hay cuota. Primero:
   ```bash
   az provider register -n Microsoft.Compute
   az provider register -n Microsoft.Network
   ```
   Tarda un par de minutos; se espera a que digan `Registered`.
2. **"Disponible" y "con cuota" son cosas distintas, y hay que mirar las dos.** `Standard_B2s` figura con cuota de 4 vCPU pero está `NotAvailableForSubscription`. `Standard_D2as_v7` figura disponible pero su familia tiene **cuota 0**, y la creación falla con `QuotaExceeded` recién en el deploy. Hay que cruzar `az vm list-skus` (sin `restrictions`) con `az vm list-usage` (límite de **esa** familia, por el campo `family` del SKU) y respetar además el tope regional, que en Azure for Students es de **6 vCPU**.
3. **El tamaño que funcionó** en `brazilsouth` fue **`Standard_D2s_v4`**: 2 vCPU y 8 GB, disponible y con cuota de 4 en su familia.

```bash
az group create -n arqsoft-rg -l brazilsouth
az vm create -g arqsoft-rg -n arqsoft \
  --image Ubuntu2404 --size Standard_D2s_v4 \
  --admin-username azureuser --generate-ssh-keys \
  --public-ip-sku Standard --os-disk-size-gb 32
az vm open-port -g arqsoft-rg -n arqsoft --port 80   --priority 900
az vm open-port -g arqsoft-rg -n arqsoft --port 8081 --priority 910
```

A diferencia de Oracle, acá **no hace falta tocar el firewall de la VM**: el NSG es el único filtro.

**Apagarla cuando no se usa** (detenida casi no consume crédito; el disco sí, poco):
```bash
az vm deallocate -g arqsoft-rg -n arqsoft    # apagar
az vm start      -g arqsoft-rg -n arqsoft    # prender (la IP pública se mantiene: es Standard, estática)
```
3. **Otra zona de disponibilidad o más tarde.** La capacidad de Ampere va y viene; no es algo para dejar a la última hora.

### Si la VM es chica: despliegue mínimo

El plan B que ADR-013 y el plan de la entrega ya tenían escrito: **subir solo el gateway, `proveedores-service` y PostgreSQL**, que es lo único que la consigna exige que esté en la nube.

```bash
docker compose \
  -f docker-compose.yml \
  -f docker-compose.nube.yml \
  -f docker-compose.nube-minima.yml \
  up -d --build postgres proveedores-service-1 proveedores-service-2 api-gateway
```

**Medido el 10/11** levantándolo de verdad: cuatro contenedores y **34 MB de memoria** (gateway 4,5 MB, cada réplica 3,6 MB, PostgreSQL 22,6 MB). Verificado que responden la capacidad con su ranking, el login, el frontend y el balanceo entre las dos réplicas.

**Qué se pierde, y hay que decirlo en la demo en lugar de que lo descubran:** la caché, la búsqueda, el consumo del evento, los pedidos a FAV, el tablero y las trazas **no están en la nube**; se muestran en local, donde corren los 14 contenedores. La búsqueda contesta `503` y `/api/v1/stock/` no se publica, las dos cosas a propósito: `proveedores-service` trata Redis, Meilisearch y RabbitMQ como opcionales ([ADR-010](adr/ADR-010-resiliencia.md)).

**Con 1/8 de OCPU, compilar es el cuello de botella**, no correr. Conviene agregar swap antes del primer build:

```bash
sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile
sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

En el firewall o grupo de seguridad del proveedor hay que abrir el puerto 80. En Oracle Cloud además hay que abrirlo en el firewall de la propia VM:

```bash
sudo iptables -I INPUT -p tcp --dport 80 -j ACCEPT
```

### Si no sale ninguna VM: túnel (respaldo, NO es la nube)

Red de seguridad para no quedarse sin URL pública el día de la presentación. **No cumple el ítem de la consigna** —es una máquina local detrás de un túnel, no capacidad desplegada— y hay que presentarlo por lo que es.

Con el sistema levantado en local:

```bash
cloudflared tunnel --url http://localhost:8080
```

Imprime una URL `https://<palabras>.trycloudflare.com` que sale a internet, con HTTPS y sin cuenta ni configuración. **Probado el 10/11**: el frontend carga, y la capacidad, el login, la búsqueda y la reserva responden igual que en local.

Se eligió sobre `ngrok` porque ngrok gratuito intercala una **pantalla de advertencia** antes de la aplicación la primera vez que se entra desde un navegador, y eso no se quiere en medio de una demo. Cloudflare no la tiene.

Límites: la URL cambia cada vez que se reinicia, el túnel vive mientras viva el proceso, y **mientras está arriba el sistema queda expuesto con las credenciales del `.env` local**. Conviene levantarlo para la demo y bajarlo después.

## 2. Instalar y levantar

Con SSH en la VM:

```bash
curl -fsSL https://raw.githubusercontent.com/matiasdemetriorufeil/arq-soft-proveedores/main/infra/nube/instalar.sh | bash
```

El script ([`infra/nube/instalar.sh`](../infra/nube/instalar.sh)):

1. instala Docker si no está;
2. clona el repo (o lo actualiza si ya estaba);
3. **la primera vez**, crea el `.env` a partir de `.env.example` y reemplaza **todas las contraseñas y secretos por valores al azar**, publica el gateway en el puerto 80 y genera la clave del grupo de paneles solares y la contraseña del usuario `demo`, que muestra al final;
4. levanta todo con `docker compose -f docker-compose.yml -f docker-compose.nube.yml up -d --build`.

Correrlo de nuevo actualiza el código y reconstruye, sin tocar el `.env` ni los datos. La primera vez tarda entre 5 y 10 minutos (descarga de imágenes y compilación).

## 3. Verificar desde afuera

Desde cualquier computadora:

```bash
curl http://<IP>/readyz
curl -H "X-API-Key: <clave-paneles>" "http://<IP>/api/v1/proveedores/recomendados?producto=PAN-450W-MONO&cantidad=10"
```

Y el frontend en `http://<IP>/`, con el usuario `demo`.

## 4. Pasarle la URL al grupo de paneles solares

Lo que necesitan saber:

- la URL base `http://<IP>`;
- su clave (`paneles-…`, la que imprimió el script; está en `API_KEYS` del `.env` de la VM);
- que el contrato pasó a **1.1.0**: se agregó `429 LIMITE_EXCEDIDO` con `Retry-After` ([`contracts/README.md`](contracts/README.md)).

## Consolas de operación (túnel SSH)

```bash
ssh -L 3000:localhost:3000 -L 9090:localhost:9090 -L 16686:localhost:16686 -L 15672:localhost:15672 usuario@<IP>
```

Con el túnel abierto: Grafana en <http://localhost:3000> (usuario `admin`, contraseña `GRAFANA_PASSWORD` del `.env` de la VM), Prometheus en <http://localhost:9090/alerts>, Jaeger en <http://localhost:16686> y RabbitMQ en <http://localhost:15672>.

## Operación

| Para | Comando (en la VM, dentro de `~/arq-soft-proveedores`) |
|---|---|
| ver el estado | `docker compose ps` |
| ver los logs del gateway | `docker compose logs -f api-gateway` |
| actualizar a lo último de `main` | `bash infra/nube/instalar.sh` |
| apagar sin perder datos | `docker compose down` |
| apagar la VM sin perder la IP | `az vm deallocate -g arqsoft-rg -n arqsoft` (prender: `az vm start`) |
| simular la caída de FAV (Game Day) | **ya no alcanza el mock**: ver abajo |

### Dar de alta la clave de un grupo consumidor

Las claves viven en `API_KEYS` del `.env`, separadas por coma. **La primera es la
interna**, con la que el gateway llama a los servicios: no se toca ni se entrega.

```bash
cd ~/arq-soft-proveedores
CLAVE="grupo-$(openssl rand -hex 12)"
# Se agrega al final de la lista, sin pisar las que ya estaban.
sed -i "s|^\(API_KEYS=.*\)|\1,$CLAVE|" .env

sudo docker compose -f docker-compose.yml -f docker-compose.nube.yml \
  up -d --no-deps api-gateway proveedores-service-1 proveedores-service-2 stock-service
echo "$CLAVE"   # entregarla por canal privado, nunca por el repo
```

**Hay que recrear los cuatro servicios, no solo el gateway.** Es el error fácil, y
pasa de verdad: el gateway **reenvía la `X-API-Key` del cliente** al servicio de
atrás, y cada servicio la valida por su cuenta contra su propia copia de
`API_KEYS`. Si se recrea solo el gateway, la clave nueva pasa el primer control y
`proveedores-service` la rechaza: el consumidor ve un **`401` que parece del
gateway y no lo es**. Se verifica así, y los cuatro tienen que decir lo mismo:

```bash
for c in api-gateway proveedores-service-1 proveedores-service-2 stock-service; do
  printf '%-24s' "$c"
  sudo docker inspect arq-soft-proveedores-$c-1 \n    --format '{{range .Config.Env}}{{println .}}{{end}}' |
    grep '^API_KEYS=' | awk -F, '{print NF" claves"}'
done
```

### Simular la caída de FAV (Game Day)

Desde el 11/11 el sistema le habla al **FAV real**, así que poner el mock en
`caido` **no hace nada**. Dos caminos:

- **Cortarle la red a `import-service`**, que es lo más parecido a que FAV se caiga:
  `sudo docker network disconnect arq-soft-proveedores_interna arq-soft-proveedores-import-service-1`
  y reconectar con `network connect` cuando termine.
- **Volver al mock**: dejar `FAV_URL=` vacío en el `.env` y recrear
  `import-service`. Queda el respaldo `.env.respaldo-antes-de-fav-real`.

## Pendiente

- **HTTPS.** La URL es `http://`. Para HTTPS hace falta un dominio (por ejemplo uno gratis de DuckDNS) y un proxy con certificado automático (Caddy) delante del gateway. No lo pide la consigna; queda como deuda en `ARCHITECTURE.md`.
- **El webhook de FAV quedó cerrado, y es correcto.** Se abrió el 10/11 y se cerró el 11/11: el contrato **1.2.0 de FAV no tiene webhook**, así que no hay quien llame a esa ruta, y tener el puerto abierto dejaba `/api/v1/stock/...` accesible por fuera del gateway, sin su límite de tráfico. Para reabrirlo, si FAV alguna vez lo publica: descomentar las tres líneas de `stock-service` en `docker-compose.nube.yml`, abrir el puerto en el grupo de seguridad y poner `FAV_NOTIFICACION_URL` en el `.env`. La especificación de lo que verificamos está en [`contracts/para-fav.md`](contracts/para-fav.md).
