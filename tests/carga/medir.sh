#!/usr/bin/env bash
# Corre la serie de mediciones de ADR-013 y guarda la evidencia: el resumen de
# k6 y el uso de CPU y memoria de cada contenedor (docker stats cada ~4-5 s:
# 2 s de espera más lo que tarda cada llamada) por corrida. Uso, desde la raíz
# del repo y con el sistema levantado:
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

# En Git Bash (Windows) docker necesita la ruta con formato de Windows y que
# MSYS no reescriba las rutas del contenedor (/scripts, /out).
export MSYS_NO_PATHCONV=1
ruta() { (cd "$1" && (pwd -W 2>/dev/null || pwd)); }
SCRIPTS=$(ruta tests/carga)
OUT=$(ruta "$SALIDA")

correr() { # nombre, variables extra para k6...
  local nombre=$1; shift
  echo "== $nombre"
  # Jaeger guarda las trazas en memoria: se recrea para que cada corrida
  # arranque desde cero y su memoria no arrastre lo de la anterior.
  docker compose up -d --force-recreate --wait jaeger >/dev/null 2>&1
  : > "$SALIDA/stats-$nombre.csv"
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
}

correr rampa
for t in "${TASAS[@]}"; do
  correr "$t" -e "ESCALONES=$t" -e DURACION_ESCALON=30s
done
echo "listo: $SALIDA"
