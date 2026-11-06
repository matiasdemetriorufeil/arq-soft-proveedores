#!/usr/bin/env bash
# Corre la serie de mediciones de ADR-013 y guarda la evidencia: el resumen de
# k6, el uso de CPU y memoria de cada contenedor (docker stats cada ~4-5 s: 2 s
# de espera más lo que tarda cada llamada) y los contadores de PostgreSQL y
# Redis antes y después, por corrida. Uso, desde la raíz del repo y con el
# sistema levantado:
#
#   bash tests/carga/medir.sh [carpeta-de-salida] [tasas...]
#
# Por defecto guarda en tests/carga/resultados/AAAA-MM-DD y corre la rampa
# completa del script más una corrida aislada por tasa.
set -euo pipefail

SALIDA="${1:-tests/carga/resultados/$(date +%F)}"
shift || true
TASAS=("${@:-200 600 1000 1200 1400 1600 2000}")
read -r -a TASAS <<< "${TASAS[*]}"

RED=arq-soft-proveedores_interna
IMAGEN=grafana/k6:1.3.0
mkdir -p "$SALIDA"
# Qué se midió: sin esto, una carpeta de resultados no dice sobre qué código ni
# con qué configuración corrió.
{
  echo "fecha: $(date -Iseconds)"
  echo "commit: $(git rev-parse HEAD)"
  echo "cambios sin commitear en services/ y pkg/: $(git status --porcelain services pkg | wc -l)"
  echo "tasas: ${TASAS[*]}"
  echo "imagenes:"
  docker compose images --format json 2>/dev/null | tr -d '' || true
  echo
  echo "CACHE_HABILITADA en las replicas: $(docker inspect arq-soft-proveedores-proveedores-service-1-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep '^CACHE_HABILITADA=' || echo 'sin variable (codigo sin cache)')"
} > "$SALIDA/meta.txt"

# En Git Bash (Windows) docker necesita la ruta con formato de Windows y que
# MSYS no reescriba las rutas del contenedor (/scripts, /out).
export MSYS_NO_PATHCONV=1
ruta() { (cd "$1" && (pwd -W 2>/dev/null || pwd)); }
SCRIPTS=$(ruta tests/carga)
OUT=$(ruta "$SALIDA")

# Una línea de contadores: hora, transacciones confirmadas de la base, lecturas
# de la tabla productos (la consulta de la capacidad la lee una vez por cada
# ejecución, así que es el conteo de consultas de ADR-007) y aciertos y fallos de
# Redis. Las estadísticas de PostgreSQL se vuelcan con hasta 1 s de atraso, y
# por eso la línea de después se toma pasada la espera de 15 s de cada corrida.
contadores() {
  local pg redis
  pg=$(docker compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT (SELECT xact_commit FROM pg_stat_database WHERE datname = current_database()), (SELECT COALESCE(seq_scan,0) + COALESCE(idx_scan,0) FROM pg_stat_user_tables WHERE relname = '"'"'productos'"'"')"' | tr -d '' | tr '|' ',')
  redis=$(docker compose exec -T redis sh -c 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning INFO stats' | tr -d ''     | awk -F: '/^keyspace_hits/{h=$2} /^keyspace_misses/{m=$2} END{print h+0","m+0}')
  echo "$1,$(date +%T),$pg,$redis"
}

correr() { # nombre, variables extra para k6...
  local nombre=$1; shift
  echo "== $nombre"
  # Jaeger guarda las trazas en memoria: se recrea para que cada corrida
  # arranque desde cero y su memoria no arrastre lo de la anterior.
  docker compose up -d --force-recreate --wait jaeger >/dev/null 2>&1
  : > "$SALIDA/stats-$nombre.csv"
  echo "momento,hora,xact_commit,lecturas_productos,redis_hits,redis_misses" > "$SALIDA/contadores-$nombre.csv"
  contadores antes >> "$SALIDA/contadores-$nombre.csv"
  ( while true; do
      docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' \
        | grep -E 'arq-soft|k6-carga' | sed "s/^/$(date +%T),/" >> "$SALIDA/stats-$nombre.csv"
      sleep 2
    done ) &
  local muestreo=$!
  docker run --rm --name k6-carga --network "$RED" \
    -v "$SCRIPTS:/scripts" -v "$OUT:/out" \
    -e BASE_URL=http://api-gateway:8080 "$@" \
    "$IMAGEN" run --quiet --summary-export "/out/k6-$nombre.json" /scripts/recomendados.js \
    > "$SALIDA/k6-$nombre.txt" 2>&1 || true
  kill "$muestreo" 2>/dev/null || true
  wait "$muestreo" 2>/dev/null || true
  sleep 15
  contadores despues >> "$SALIDA/contadores-$nombre.csv"
}

correr rampa
for t in "${TASAS[@]}"; do
  correr "$t" -e "ESCALONES=$t" -e DURACION_ESCALON=30s
done
echo "listo: $SALIDA"
