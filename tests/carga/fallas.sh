#!/usr/bin/env bash
# Simulacro de fallas de la lectura rápida (ADR-010): apaga Redis, Meilisearch,
# RabbitMQ, PostgreSQL y la réplica activa, de a uno, y muestra qué responde el
# sistema. Solo toca contenedores de este proyecto. Uso, desde la raíz del repo,
# con el sistema levantado e INTERVALO_SINCRONIZACION=10s:
#
#   bash tests/carga/fallas.sh | tee tests/carga/resultados/<fecha>-fallas/fallas.txt
set -uo pipefail

CLAVE="${API_KEY:-clave-de-desarrollo}"
GW="${GATEWAY_URL:-http://localhost:8080}"
MOCK="${FAV_MOCK_URL:-http://localhost:8090}"
RABBIT_UI="${RABBIT_UI_URL:-http://localhost:15672}"
RABBIT_AUTH="${RABBITMQ_USER:-arqsoft}:${RABBITMQ_PASSWORD:-rabbit-local}"
VARIANTE=var-buzo-canguro-azul-l
SKU="FAV-$VARIANTE"
export MSYS_NO_PATHCONV=1

titulo() { printf '\n=== %s\n' "$*"; }
capacidad() { curl -s -o /dev/null -w '%{http_code} en %{time_total}s' -H "X-API-Key: $CLAVE" "$GW/api/v1/proveedores/recomendados?producto=$1"; }
precio_ranking() { curl -s -H "X-API-Key: $CLAVE" "$GW/api/v1/proveedores/recomendados?producto=$SKU" | python -c "import json,sys; d=json.load(sys.stdin); print(d[0]['precio_unitario'] if d else None)"; }
buscar_codigo() { curl -s -o /dev/null -w '%{http_code}' -H "X-API-Key: $CLAVE" "$GW/api/v1/productos/buscar?q=$SKU"; }
precio_busqueda() { curl -s -H "X-API-Key: $CLAVE" "$GW/api/v1/productos/buscar?q=$SKU" | python -c "import json,sys; d=json.load(sys.stdin); print(next((r['precio_min'] for r in d.get('resultados',[]) if r['sku']=='$SKU'),None))"; }
cambiar_precio() { curl -s -o /dev/null -X POST "$MOCK/_mock/precio" -d "{\"varianteId\":\"$VARIANTE\",\"precio\":$1}"; }
psql_q() { docker compose exec -T postgres sh -c "psql -U \"\$POSTGRES_USER\" -d \"\$POSTGRES_DB\" -Atc \"$1\""; }
pendientes() { psql_q 'SELECT count(*) FROM skus_por_reindexar' | tr -d '\r'; }
mensajes_dlq() { curl -s -u "$RABBIT_AUTH" "$RABBIT_UI/api/queues/%2F/proveedores-service.lista-precios.dlq" | python -c "import json,sys; print(json.load(sys.stdin).get('messages',0))"; }
esperar_hasta() { # segundos, comando, valor
  local hasta=$((SECONDS + $1)) got
  while [ "$SECONDS" -lt "$hasta" ]; do
    got=$($2 2>/dev/null)
    if [ "$got" = "$3" ]; then echo "  llegó a $3 en $((SECONDS + $1 - hasta)) s"; return 0; fi
    python -c "import time; time.sleep(0.5)"
  done
  echo "  NO llegó a $3 en $1 s (último: ${got:-})"
  return 1
}

titulo "Punto de partida"
echo "capacidad: $(capacidad PAN-550W-MONO)  búsqueda: $(buscar_codigo)  pendientes: $(pendientes)"

titulo "Invalidación: un evento toca un SKU cacheado"
base=$((40000 + RANDOM))
capacidad "$SKU" >/dev/null
echo "precio cacheado: $(precio_ranking)"
cambiar_precio "$base"
esperar_hasta 30 precio_ranking "$base"

titulo "Redis caído"
docker compose stop redis >/dev/null 2>&1
for _ in 1 2 3; do echo "capacidad (SKU existente): $(capacidad INV-5KW-HIB)"; done
echo "capacidad (SKU inexistente): $(capacidad NO-EXISTE)"
cambiar_precio $((base + 1))
echo "un evento con Redis caído:"; esperar_hasta 30 precio_ranking "$((base + 1))"
docker compose start redis >/dev/null 2>&1
echo "de vuelta: $(capacidad INV-5KW-HIB)"

titulo "Meilisearch caído: búsqueda 503, capacidad 200, y el evento no se pierde"
docker compose stop meilisearch >/dev/null 2>&1
echo "búsqueda: $(buscar_codigo)  capacidad: $(capacidad PAN-550W-MONO)"
cambiar_precio $((base + 2))
echo "la oferta se aplica igual:"; esperar_hasta 30 precio_ranking "$((base + 2))"
echo "SKU pendientes con el motor caído: $(pendientes)"
docker compose start meilisearch >/dev/null 2>&1
echo "al volver, se indexa sin otro evento:"; esperar_hasta 60 precio_busqueda "$((base + 2))"
echo "SKU pendientes: $(pendientes)"

titulo "RabbitMQ caído: la capacidad sigue y el consumidor reconecta"
docker compose stop rabbitmq >/dev/null 2>&1
echo "capacidad: $(capacidad PAN-550W-MONO)  búsqueda: $(buscar_codigo)"
cambiar_precio $((base + 3))
docker compose start rabbitmq >/dev/null 2>&1
echo "el evento esperó en el outbox y llega al volver:"; esperar_hasta 120 precio_ranking "$((base + 3))"

titulo "Una réplica arranca con las tres dependencias caídas"
docker compose stop redis meilisearch rabbitmq >/dev/null 2>&1
docker compose restart proveedores-service-2 >/dev/null 2>&1
for _ in $(seq 1 40); do
  estado=$(docker inspect arq-soft-proveedores-proveedores-service-2-1 --format '{{.State.Health.Status}}')
  [ "$estado" = "healthy" ] && break
  python -c "import time; time.sleep(1)"
done
echo "salud de la réplica 2: $estado"
docker compose stop proveedores-service-1 >/dev/null 2>&1
python -c "import time; time.sleep(6)"
echo "capacidad servida solo por la réplica 2: $(capacidad PAN-550W-MONO)"
docker compose start redis meilisearch rabbitmq proveedores-service-1 >/dev/null 2>&1
docker compose up -d --wait >/dev/null 2>&1

titulo "Cae la réplica que consume: la otra toma"
activa=$(docker compose logs --no-log-prefix --since 30m proveedores-service-1 2>/dev/null | grep -c '"evento aplicado"')
activa2=$(docker compose logs --no-log-prefix --since 30m proveedores-service-2 2>/dev/null | grep -c '"evento aplicado"')
cambiar_precio $((base + 4))
esperar_hasta 60 precio_ranking "$((base + 4))"
n1=$(docker compose logs --no-log-prefix --since 30m proveedores-service-1 2>/dev/null | grep -c '"evento aplicado"')
n2=$(docker compose logs --no-log-prefix --since 30m proveedores-service-2 2>/dev/null | grep -c '"evento aplicado"')
if [ "$n1" -gt "$activa" ]; then consume=proveedores-service-1; otra=proveedores-service-2; else consume=proveedores-service-2; otra=proveedores-service-1; fi
echo "consume $consume (eventos: réplica 1 $activa→$n1, réplica 2 $activa2→$n2)"
docker compose stop "$consume" >/dev/null 2>&1
antes=$(docker compose logs --no-log-prefix --since 30m "$otra" 2>/dev/null | grep -c '"evento aplicado"')
cambiar_precio $((base + 5))
esperar_hasta 60 precio_ranking "$((base + 5))"
echo "eventos de $otra: $antes→$(docker compose logs --no-log-prefix --since 30m "$otra" 2>/dev/null | grep -c '"evento aplicado"')"
docker compose start "$consume" >/dev/null 2>&1
docker compose up -d --wait >/dev/null 2>&1

titulo "Mensaje basura: va a la DLQ sin reintentos"
dlq=$(mensajes_dlq)
curl -s -u "$RABBIT_AUTH" -H 'Content-Type: application/json' -X POST "$RABBIT_UI/api/exchanges/%2F/proveedores.eventos/publish" \
  -d '{"properties":{"message_id":"simulacro-basura"},"routing_key":"lista_precios.actualizada","payload":"esto no es JSON","payload_encoding":"string"}'
echo
esperar_hasta 10 mensajes_dlq "$((dlq + 1))"

titulo "PostgreSQL caído al llegar un evento: 3 entregas y DLQ"
dlq=$(mensajes_dlq)
docker compose stop postgres >/dev/null 2>&1
echo "capacidad con un SKU cacheado: $(capacidad "$SKU")"
echo "capacidad con un SKU no cacheado: $(capacidad RIEL-ALU-2M)"
curl -s -u "$RABBIT_AUTH" -H 'Content-Type: application/json' -X POST "$RABBIT_UI/api/exchanges/%2F/proveedores.eventos/publish" \
  -d "{\"properties\":{\"message_id\":\"simulacro-base\"},\"routing_key\":\"lista_precios.actualizada\",\"payload_encoding\":\"string\",\"payload\":\"{\\\"evento_id\\\":\\\"simulacro-base-$RANDOM\\\",\\\"tipo\\\":\\\"ListaPreciosActualizada\\\",\\\"version\\\":1,\\\"ocurrido_en\\\":\\\"2026-11-06T00:00:00Z\\\",\\\"datos\\\":{\\\"proveedor\\\":{\\\"id\\\":5,\\\"codigo\\\":\\\"seed\\\",\\\"nombre\\\":\\\"seed\\\"},\\\"secuencia\\\":1,\\\"motivo\\\":\\\"sincronizacion\\\",\\\"completa\\\":false,\\\"productos\\\":[],\\\"ofertas\\\":[],\\\"bajas\\\":[]}}\"}"
echo
esperar_hasta 40 mensajes_dlq "$((dlq + 1))"
docker compose start postgres >/dev/null 2>&1
docker compose up -d --wait >/dev/null 2>&1
echo "de vuelta: $(capacidad RIEL-ALU-2M)"

titulo "Reconstrucción a mano"
docker compose exec -T proveedores-service-1 /reindexar 2>&1 | grep -E 'reconstru|error' | cut -c1-220
echo "búsqueda después: $(buscar_codigo), pendientes: $(pendientes)"

titulo "Estado final"
docker compose ps --format '{{.Name}} {{.Status}}'
