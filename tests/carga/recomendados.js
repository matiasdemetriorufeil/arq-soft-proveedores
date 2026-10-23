// Test de carga de la capacidad publicada: GET /api/v1/proveedores/recomendados
// a través del gateway. Sube la tasa de requests por escalones para encontrar
// hasta dónde el sistema mantiene el objetivo (p95 < 300 ms y < 1 % de errores).
// Cómo correrlo y cómo leer el resultado: tests/carga/README.md.

import http from 'k6/http';
import { check } from 'k6';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const API_KEY = __ENV.API_KEY || 'clave-de-desarrollo';
// Requests por segundo de cada escalón, separadas por coma.
const ESCALONES = (__ENV.ESCALONES || '50,100,200,400,800,1200,1600')
  .split(',')
  .map((s) => parseInt(s.trim(), 10));
const DURACION_ESCALON = __ENV.DURACION_ESCALON || '30s';

// Los 30 SKU del seed (002_seed.sql): la carga se reparte entre productos
// reales, como haría el grupo de paneles solares.
const SKUS = [
  'PAN-450W-MONO', 'PAN-550W-MONO', 'PAN-600W-BIF', 'PAN-330W-POLI', 'PAN-100W-FLEX',
  'INV-3KW-ONG', 'INV-5KW-ONG', 'INV-5KW-HIB', 'INV-10KW-TRI', 'MIC-800W',
  'BAT-LFP-5KWH', 'BAT-LFP-10KWH', 'BAT-GEL-200AH', 'REG-MPPT-60A', 'REG-PWM-30A',
  'CAB-SOLAR-4MM', 'CAB-SOLAR-6MM', 'CON-MC4-PAR', 'CON-MC4-Y', 'EST-ALU-TECHO',
  'EST-ALU-PISO', 'RIEL-ALU-2M', 'GRAP-MED', 'GRAP-FIN', 'PROT-DC-1000V',
  'FUS-DC-15A', 'SECC-DC-32A', 'TAB-STRING-2E', 'MED-BIDIR', 'MON-WIFI',
];

// Cada escalón sube en 5 s y se sostiene DURACION_ESCALON: el ramping
// arrival-rate mantiene la tasa aunque el sistema se ponga lento (modelo
// abierto), que es lo que pasa con un consumidor real.
const stages = [];
for (const rps of ESCALONES) {
  stages.push({ target: rps, duration: '5s' });
  stages.push({ target: rps, duration: DURACION_ESCALON });
}

export const options = {
  scenarios: {
    escalones: {
      executor: 'ramping-arrival-rate',
      startRate: ESCALONES[0],
      timeUnit: '1s',
      preAllocatedVUs: 100,
      maxVUs: 2000,
      stages,
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<300'],
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const sku = SKUS[Math.floor(Math.random() * SKUS.length)];
  const cantidad = 1 + Math.floor(Math.random() * 50);
  const res = http.get(
    `${BASE_URL}/api/v1/proveedores/recomendados?producto=${sku}&cantidad=${cantidad}&limite=5`,
    { headers: { 'X-API-Key': API_KEY }, tags: { name: 'recomendados' } },
  );
  check(res, { 'status 200': (r) => r.status === 200 });
}
