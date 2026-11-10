// Retraso del índice de búsqueda (ADR-006): cuánto tarda un cambio de precio
// confirmado en PostgreSQL en aparecer en la búsqueda, con eventos reales de punta
// a punta: mock de FAV → import-service → RabbitMQ → consumidor → Meilisearch.
//
// Corre en la red del compose para que k6 y PostgreSQL usen el mismo reloj: el
// retraso es el momento en que la búsqueda muestra el precio menos el
// `actualizado_en` que escribió la base. Hay que levantar el sistema con
// INTERVALO_SINCRONIZACION=10s para no esperar 5 min por evento. Cómo correrlo:
// tests/carga/README.md.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://api-gateway:8080';
const FAV_MOCK = __ENV.FAV_MOCK_URL || 'http://fav-mock:8090';
const API_KEY = __ENV.API_KEY || 'clave-de-desarrollo';
const VARIANTE = __ENV.VARIANTE || 'var-buzo-canguro-azul-l';
const EVENTOS = parseInt(__ENV.EVENTOS || '25', 10);
// Lo que puede tardar un evento entero: un intervalo de sincronización y algo más.
const ESPERA_MAXIMA_MS = parseInt(__ENV.ESPERA_MAXIMA_MS || '40000', 10);

const retraso = new Trend('retraso_indice_ms', true);
const medidos = new Counter('eventos_medidos');
const perdidos = new Counter('eventos_perdidos');

export const options = {
  scenarios: {
    eventos: { executor: 'per-vu-iterations', vus: 1, iterations: EVENTOS, maxDuration: '40m' },
  },
  thresholds: {
    retraso_indice_ms: ['p(95)<5000'],
    eventos_perdidos: ['count<1'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max', 'min'],
};

function documento() {
  const res = http.get(
    `${BASE_URL}/api/v1/productos/buscar?q=${encodeURIComponent('FAV-' + VARIANTE)}&tamanio=5`,
    { headers: { 'X-API-Key': API_KEY }, tags: { name: 'buscar' } },
  );
  if (res.status !== 200) {
    return null;
  }
  return res.json('resultados').find((d) => d.sku === 'FAV-' + VARIANTE) || null;
}

export default function () {
  // Un precio distinto en cada vuelta, para reconocer el evento en la búsqueda.
  const precio = 30000 + __ITER + 1 + (Date.now() % 1000) * 1000;
  const cambio = http.post(`${FAV_MOCK}/_mock/precio`, JSON.stringify({ varianteId: VARIANTE, precio }), {
    headers: { 'Content-Type': 'application/json' },
    tags: { name: 'cambiar_precio' },
  });
  check(cambio, { 'el mock aceptó el precio': (r) => r.status === 204 });

  const limite = Date.now() + ESPERA_MAXIMA_MS;
  while (Date.now() < limite) {
    const d = documento();
    if (d && d.precio_min === precio) {
      const ms = Date.now() - Date.parse(d.actualizado_en);
      retraso.add(ms);
      medidos.add(1);
      console.log(`evento ${__ITER + 1}: ${ms} ms`);
      return;
    }
    sleep(0.02);
  }
  perdidos.add(1);
  console.error(`evento ${__ITER + 1}: el precio ${precio} no apareció en ${ESPERA_MAXIMA_MS} ms`);
}
