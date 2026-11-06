#!/usr/bin/env bash
# Tasa de aciertos de la caché a la demanda esperada de ADR-007: pocas req/s y SKU
# sesgados, con los contadores del propio Redis. Con 30 SKU, una distribución
# uniforme y cientos de req/s, cualquier TTL de 60 s acierta casi el 100 % y el
# número no dice nada. Uso, desde la raíz del repo y con la caché prendida:
#
#   bash tests/carga/aciertos.sh [carpeta-de-salida] [req/s] [duracion]
#
# Por defecto 5 req/s durante 5 minutos, varias veces el TTL.
set -euo pipefail

SALIDA="${1:-tests/carga/resultados/$(date +%F)-aciertos}"
TASA="${2:-5}"
DURACION="${3:-300s}"
mkdir -p "$SALIDA"

export MSYS_NO_PATHCONV=1
ruta() { (cd "$1" && (pwd -W 2>/dev/null || pwd)); }
SCRIPTS=$(ruta tests/carga)
OUT=$(ruta "$SALIDA")

redis_cli() {
  docker compose exec -T redis sh -c "redis-cli -a \"\$REDIS_PASSWORD\" --no-auth-warning $*" | tr -d '\r'
}

redis_cli CONFIG RESETSTAT >/dev/null
docker run --rm --name k6-carga --network arq-soft-proveedores_interna \
  -v "$SCRIPTS:/scripts" -v "$OUT:/out" \
  -e BASE_URL=http://api-gateway:8080 -e "ESCALONES=$TASA" -e "DURACION_ESCALON=$DURACION" -e SKU_SESGADOS=1 \
  grafana/k6:1.3.0 run --quiet --summary-export "/out/k6-aciertos.json" /scripts/recomendados.js \
  > "$SALIDA/k6-aciertos.txt" 2>&1 || true

redis_cli INFO stats > "$SALIDA/redis-info-stats.txt"
awk -F: -v tasa="$TASA" -v dur="$DURACION" '
  /^keyspace_hits/ {h=$2+0}
  /^keyspace_misses/ {m=$2+0}
  END {printf "tasa=%s req/s, duracion=%s, aciertos=%d, fallos=%d, tasa_de_aciertos=%.1f %%\n", tasa, dur, h, m, 100*h/(h+m)}
' "$SALIDA/redis-info-stats.txt" | tee "$SALIDA/aciertos.txt"
