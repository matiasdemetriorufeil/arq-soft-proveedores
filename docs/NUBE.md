# Despliegue en la nube

Cómo se sube el sistema a una VM con URL pública. Es la **opción A de [ADR-013](adr/ADR-013-capacidad-y-costos.md)**: una sola VM que corre el mismo `docker compose` que usamos en local, con un override que deja afuera solo el gateway.

- **URL pública:** _pendiente_. Se anota acá, en el [`README.md`](../README.md) y en [`contracts/README.md`](contracts/README.md) apenas exista.
- **Qué queda expuesto a internet:** solo el `api-gateway` en el puerto 80, que atiende la capacidad (`/api/v1/proveedores/recomendados`), el login, stock y el frontend (`/`).
- **Qué no queda expuesto:** bases, broker, servicios y las consolas de operación (Grafana, Prometheus, Jaeger, RabbitMQ y el control del mock de FAV). Las consolas quedan en `127.0.0.1` de la VM y se ven con un túnel SSH.

## 1. Crear la VM

Cualquier proveedor sirve. Lo que hace falta:

| Requisito | Valor | Por qué |
|---|---|---|
| Sistema | Ubuntu 24.04 LTS | el script de instalación usa `apt` vía `get.docker.com` |
| Tamaño | 2 vCPU y 4 GB de RAM como mínimo | son 14 contenedores; MongoDB, RabbitMQ y Grafana son los que más memoria piden |
| Disco | 20 GB | imágenes, volúmenes y la caché de build de Go |
| Puertos de entrada | **22** (SSH) y **80** (HTTP) | nada más: las consolas van por el túnel SSH |

Opciones que no cuestan nada durante la cursada: **Oracle Cloud Free Tier** (VM Ampere A1, hasta 4 vCPU y 24 GB sin cargo), **Google Cloud** (USD 300 de crédito inicial, `e2-medium`) o **Azure for Students** (USD 100 sin tarjeta). En AWS una `t3.medium` sale cerca de USD 30 por mes y la capa gratuita (`t3.micro`, 1 GB) no alcanza.

En el firewall o grupo de seguridad del proveedor hay que abrir el puerto 80. En Oracle Cloud además hay que abrirlo en el firewall de la propia VM: `sudo iptables -I INPUT -p tcp --dport 80 -j ACCEPT`.

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
| simular la caída de FAV (Game Day) | `curl -X POST localhost:8090/_mock/modo -d '{"modo":"caido"}'` |

## Pendiente

- **HTTPS.** La URL es `http://`. Para HTTPS hace falta un dominio (por ejemplo uno gratis de DuckDNS) y un proxy con certificado automático (Caddy) delante del gateway. No lo pide la consigna; queda como deuda en `ARCHITECTURE.md`.
- **El webhook de FAV.** Con el FAV desplegado, FAV tiene que poder llegar a `stock-service` en `/webhooks/fav` **sin pasar por el gateway** ([ADR-009](adr/ADR-009-consumo-proveedor.md)). Eso necesita publicar esa ruta en otro puerto de la VM y poner la URL en `FAV_NOTIFICACION_URL`. Mientras se use el mock, no hace falta.
