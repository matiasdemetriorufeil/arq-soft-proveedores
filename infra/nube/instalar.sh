#!/usr/bin/env bash
# Instala y levanta el sistema en una VM Ubuntu recién creada (ADR-013,
# opción A: una VM con el mismo docker compose). Correrlo como un usuario con
# sudo, desde cualquier carpeta:
#
#   curl -fsSL https://raw.githubusercontent.com/matiasdemetriorufeil/arq-soft-proveedores/main/infra/nube/instalar.sh | bash
#
# o, con el repo ya clonado: bash infra/nube/instalar.sh
#
# En una VM chica (1 GB), MINIMA=1 levanta solo el gateway,
# proveedores-service y PostgreSQL, que es lo único que la consigna exige que
# esté en la nube (ver docs/NUBE.md):
#
#   curl -fsSL .../instalar.sh | MINIMA=1 bash
#
# Es idempotente: si se vuelve a correr, actualiza el código y reconstruye,
# sin tocar el .env ni los volúmenes.
set -euo pipefail

REPO="${REPO:-https://github.com/matiasdemetriorufeil/arq-soft-proveedores.git}"
RAMA="${RAMA:-main}"
DIR="${DIR:-$HOME/arq-soft-proveedores}"
MINIMA="${MINIMA:-}"

# Los archivos de compose y, en el modo mínimo, los únicos servicios que se
# levantan: nombrarlos no alcanza, porque las réplicas declaran depends_on de
# redis, meilisearch y rabbitmq; eso lo corta el !override del tercer archivo.
ARCHIVOS=(-f docker-compose.yml -f docker-compose.nube.yml)
SERVICIOS=()
if [ -n "$MINIMA" ]; then
  ARCHIVOS+=(-f docker-compose.nube-minima.yml)
  SERVICIOS=(postgres proveedores-service-1 proveedores-service-2 api-gateway)
fi

# 1. Docker y el plugin de compose, desde el repositorio oficial de Docker.
if ! command -v docker >/dev/null 2>&1; then
  echo ">> instalando Docker"
  curl -fsSL https://get.docker.com | sudo sh
  sudo usermod -aG docker "$USER"
fi
DOCKER="docker"
if ! docker info >/dev/null 2>&1; then
  # Recién agregado al grupo: en esta sesión todavía hace falta sudo.
  DOCKER="sudo docker"
fi

# 2. El código.
if [ -d "$DIR/.git" ]; then
  echo ">> actualizando $DIR"
  git -C "$DIR" fetch --quiet origin "$RAMA"
  git -C "$DIR" checkout --quiet "$RAMA"
  git -C "$DIR" pull --quiet --ff-only origin "$RAMA"
else
  echo ">> clonando $REPO"
  git clone --quiet --branch "$RAMA" "$REPO" "$DIR"
fi
cd "$DIR"

# 3. El .env, solo la primera vez. Parte de .env.example y reemplaza todo lo
#    que es secreto por valores al azar: en la nube no puede quedar una
#    contraseña de desarrollo.
if [ ! -f .env ]; then
  echo ">> creando .env con secretos nuevos"
  azar() { openssl rand -hex "${1:-16}"; }
  cp .env.example .env
  CLAVE_PANELES="paneles-$(azar 12)"
  CONTRASENA_DEMO="$(azar 6)"
  sed -i \
    -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(azar)|" \
    -e "s|^STOCK_DB_PASSWORD=.*|STOCK_DB_PASSWORD=$(azar)|" \
    -e "s|^RABBITMQ_PASSWORD=.*|RABBITMQ_PASSWORD=$(azar)|" \
    -e "s|^MONGO_PASSWORD=.*|MONGO_PASSWORD=$(azar)|" \
    -e "s|^REDIS_PASSWORD=.*|REDIS_PASSWORD=$(azar)|" \
    -e "s|^MEILI_MASTER_KEY=.*|MEILI_MASTER_KEY=$(azar)|" \
    -e "s|^GRAFANA_PASSWORD=.*|GRAFANA_PASSWORD=$(azar 8)|" \
    -e "s|^JWT_SECRETO=.*|JWT_SECRETO=$(azar 32)|" \
    -e "s|^USUARIOS=.*|USUARIOS=demo:$CONTRASENA_DEMO|" \
    -e "s|^API_KEYS=.*|API_KEYS=interna-$(azar 12),$CLAVE_PANELES|" \
    -e "s|^PROVEEDORES_API_KEY=.*|PROVEEDORES_API_KEY=|" \
    -e "s|^PUERTO_GATEWAY=.*|PUERTO_GATEWAY=80|" \
    .env
  # stock-service usa la primera clave de API_KEYS para hablar con
  # proveedores-service (el compose la reusa si PROVEEDORES_API_KEY está vacía,
  # pero toma la lista entera): se fija explícitamente la interna.
  INTERNA="$(grep '^API_KEYS=' .env | cut -d= -f2 | cut -d, -f1)"
  sed -i "s|^PROVEEDORES_API_KEY=.*|PROVEEDORES_API_KEY=$INTERNA|" .env
  echo
  echo "   Clave para el grupo de paneles solares: $CLAVE_PANELES"
  echo "   Usuario del frontend:                   demo / $CONTRASENA_DEMO"
  echo "   (quedan guardadas en $DIR/.env)"
  echo
fi

# 4. Levantar.
if [ -n "$MINIMA" ]; then
  echo ">> levantando el DESPLIEGUE MINIMO: gateway, proveedores-service y PostgreSQL"
  echo "   (sin cache, busqueda, eventos, stock, FAV ni tablero: eso se muestra en local)"
else
  echo ">> levantando el sistema (la primera vez tarda unos minutos)"
fi
$DOCKER compose "${ARCHIVOS[@]}" up -d --build ${SERVICIOS[@]+"${SERVICIOS[@]}"}

IP="$(curl -fsS --max-time 5 https://api.ipify.org || hostname -I | awk '{print $1}')"
echo
echo ">> listo. Probar desde afuera:"
echo "   curl http://$IP/readyz"
echo "   frontend: http://$IP/"

# Las variables de FAV no se generan al azar: son datos que entrega el otro
# grupo. Sin ellas el sistema funciona igual, contra el mock, así que es un
# olvido fácil y silencioso: se avisa acá en lugar de que aparezca en la demo.
#
# En el modo mínimo no corre stock-service, así que no hay receptor del webhook
# y el aviso de arriba no aplica: lo que corresponde es decir qué falta.
if [ -n "$MINIMA" ]; then
  echo
  echo ">> esta VM corre el despliegue minimo. NO estan en la nube:"
  echo "   la cache, la busqueda, el consumo del evento, los pedidos a FAV,"
  echo "   el webhook, el tablero ni las trazas. Se muestran en local."
  echo "   La capacidad, el login y el frontend si estan, que es lo que pide la consigna."
elif ! grep -q '^FAV_URL=.\+' .env; then
  echo
  echo ">> FALTA conectar el FAV real. Hoy corre contra el mock."
  echo "   En $DIR/.env, con los datos que entregó FAV:"
  echo "     FAV_URL=<su URL>"
  echo "     FAV_API_KEY=<su clave>"
  echo "   Después: $DOCKER compose ${ARCHIVOS[*]} up -d ${SERVICIOS[*]}"
  echo "   El estado del pedido se sigue con GET /v1/pedidos/{pedidoId}: su contrato"
  echo "   1.2.0 quito el webhook y la 1.3.0 no lo devolvio (ver ADR-009)."
fi
