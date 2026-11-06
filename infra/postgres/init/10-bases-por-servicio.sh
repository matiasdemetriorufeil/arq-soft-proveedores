#!/bin/sh
# Una base por servicio (ADR-003): proveedores-service usa la base que crea la
# imagen de Postgres (POSTGRES_DB) y stock-service la suya, con un usuario
# propio que no puede leer la de proveedores.
#
# La imagen corre los scripts de /docker-entrypoint-initdb.d solo la primera
# vez, cuando el volumen está vacío. Si ya tenías el volumen de la Entrega 2:
#
#   docker compose down -v      (borra los datos locales)
#   docker compose up -d --build
set -eu

: "${STOCK_DB:?falta STOCK_DB}"
: "${STOCK_DB_USER:?falta STOCK_DB_USER}"
: "${STOCK_DB_PASSWORD:?falta STOCK_DB_PASSWORD}"

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v usuario="$STOCK_DB_USER" -v clave="$STOCK_DB_PASSWORD" -v base="$STOCK_DB" <<'SQL'
CREATE ROLE :"usuario" LOGIN PASSWORD :'clave';
CREATE DATABASE :"base" OWNER :"usuario";
REVOKE ALL ON DATABASE :"base" FROM PUBLIC;
SQL

# Que el usuario de stock no pueda conectarse a la base de proveedores.
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v base="$POSTGRES_DB" <<'SQL'
REVOKE CONNECT ON DATABASE :"base" FROM PUBLIC;
SQL
