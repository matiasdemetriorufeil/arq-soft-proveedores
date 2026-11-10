// Latencia de la búsqueda a través del gateway (ADR-006, objetivo p95 < 100 ms).
// Corre en la red del compose, a una tasa fija: ver tests/carga/README.md.

import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://api-gateway:8080';
const API_KEY = __ENV.API_KEY || 'clave-de-desarrollo';
const CONSULTAS = [
  'q=panel&orden=precio_asc', 'q=inversor%20hibrido', 'q=bateria&con_stock=true', 'q=invesor',
  'categoria=paneles&pagina=2&tamanio=5', 'q=buzo&categoria=ropa-buzos', 'precio_min=1000&precio_max=200000&orden=precio_desc',
];

export const options = {
  scenarios: {
    busquedas: {
      executor: 'constant-arrival-rate',
      rate: parseInt(__ENV.TASA || '100', 10),
      timeUnit: '1s',
      duration: __ENV.DURACION || '30s',
      preAllocatedVUs: 50,
      maxVUs: 500,
    },
  },
  thresholds: { http_req_failed: ['rate<0.01'], http_req_duration: ['p(95)<100'] },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const q = CONSULTAS[Math.floor(Math.random() * CONSULTAS.length)];
  const res = http.get(`${BASE_URL}/api/v1/productos/buscar?${q}`, {
    headers: { 'X-API-Key': API_KEY }, tags: { name: 'buscar' },
  });
  check(res, { 'status 200': (r) => r.status === 200 });
}
