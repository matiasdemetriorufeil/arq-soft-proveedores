# Demo y checklist de la Entrega 2 (23/10)

Guion de 5 minutos, checklist de lo que tiene que estar en `main` y cómo se crea el
tag `entrega-2`. Los comandos están probados contra el sistema levantado con
`docker compose up` el 23/10.

> En PowerShell usar `curl.exe` (no `curl`, que es un alias de
> `Invoke-WebRequest`). En bash, `curl` tal cual.

## Antes de entrar (10 min antes)

```bash
git clone https://github.com/matiasdemetriorufeil/arq-soft-proveedores.git demo-e2
cd demo-e2
git checkout entrega-2
cp .env.example .env              # PowerShell: Copy-Item .env.example .env
docker compose up -d --build      # la primera vez tarda unos minutos en construir
docker compose ps                 # los 5 contenedores tienen que decir (healthy)
```

Dejar abiertos: una terminal en la raíz del repo, Jaeger en
<http://localhost:16686> y el repo en GitHub en `docs/`.

## Guion (5 minutos)

### 1. Qué es y cómo se levanta (45 s)

- "Comparador de proveedores: dado un producto y una cantidad, rankea a los
  proveedores por costo total. Es la capacidad que consume el grupo de paneles solares."
- Mostrar `docker compose ps`: gateway, dos réplicas de `proveedores-service`,
  PostgreSQL y Jaeger, todos `healthy`, con **un solo comando** y sin secretos en el
  repo (`.env` se copia de `.env.example`).

### 2. La capacidad funcionando (1 min)

```bash
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=3" \
  -H "X-API-Key: clave-de-desarrollo"
```

- Explicar el primer resultado: `costo_total = precio × cantidad × (1 − descuento) + flete`,
  el descuento aplica porque la cantidad supera el mínimo (`descuento_aplicado: true`),
  y se excluyen las ofertas sin stock suficiente.
- Mostrar el header `X-Request-ID` de la respuesta.

### 3. Errores del contrato (45 s)

```bash
# 404: el SKU no existe
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=NO-EXISTE" \
  -H "X-API-Key: clave-de-desarrollo"

# 401: sin API key
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO"
```

- Señalar `application/problem+json`, el campo `code` y que el `type` apunta a la
  tabla de errores de [`docs/contracts/README.md`](contracts/README.md).

### 4. Un `trace_id` de los logs a Jaeger (1 min 15 s)

```bash
curl -s -o /dev/null -H "X-Request-ID: demo-1" -H "X-API-Key: clave-de-desarrollo" \
  "http://localhost:8080/api/v1/proveedores/recomendados?producto=INV-5KW-HIB&cantidad=3"

docker compose logs --no-log-prefix api-gateway proveedores-service-1 proveedores-service-2 | grep demo-1
# PowerShell: ... | Select-String demo-1
```

- Las dos líneas (gateway y la réplica que atendió) tienen el **mismo `request_id`
  y el mismo `trace_id`**.
- Copiar el `trace_id` y abrir `http://localhost:16686/trace/<trace_id>`: 4 spans,
  gateway → llamada a la réplica → servicio → `SELECT` a PostgreSQL. El atributo
  `upstream` dice qué réplica atendió.

### 5. Balanceo y caída de una réplica (45 s, si sobra tiempo)

```bash
docker compose stop proveedores-service-1
# repetir el curl del paso 2: sigue respondiendo 200 por la otra réplica
docker compose start proveedores-service-1
```

- En los logs del gateway aparece `upstream caído` y, al volver, `upstream recuperado`
  ([ADR-012](adr/ADR-012-balanceo-de-carga.md)).

### 6. Documentación y decisiones (30 s)

- [`README.md`](../README.md), [`SPEC.md`](../SPEC.md), [`ARCHITECTURE.md`](ARCHITECTURE.md)
  con diagramas y el contrato [`openapi.yaml`](contracts/openapi.yaml).
- ADR de la entrega: D2, D5, D6, D7, D9, D10, D12 y D13, más D1, D3, D8 y D11. Aclarar
  que los que no están implementados (búsqueda, caché, consumo de ropa, mensajería)
  están marcados **propuestos**.
- Dato fuerte para cerrar: **test de carga con k6, ~1 500 req/s sostenidas con p95 bajo
  300 ms; el límite es la CPU y el componente que más gasta es el gateway**
  ([ADR-013](adr/ADR-013-capacidad-y-costos.md)).

## Preguntas probables y dónde está la respuesta

| Pregunta | Respuesta corta | Dónde |
|---|---|---|
| ¿Por qué el balanceo está en el gateway y no en nginx? | Ya está en el camino de todas las requests y se puede demostrar con logs y spans | ADR-012 |
| ¿Qué pasa si se cae PostgreSQL? | `503 NO_DISPONIBLE` en 2 ms (o 2 s si está colgada); es punto único de falla | ADR-010 |
| ¿Por qué no reintentan ante un timeout? | La réplica puede estar procesando: reintentar suma carga a algo lento | ADR-005 |
| ¿Cuál es el límite del sistema? | CPU; el gateway gasta casi la mitad por un pool de 2 conexiones por réplica | ADR-013 |
| ¿Dónde está la mensajería, la búsqueda y la caché? | Propuestas con diseño completo; se implementan para la presentación | ADR-005, 006, 007 |
| ¿Y el grupo de ropa? | Pull periódico con capa anticorrupción; falta su contrato | ADR-009 |

## Checklist de la entrega (consigna 6.3)

**Código**

- [x] Al menos un servicio operativo (`proveedores-service`, dos réplicas)
- [x] Capacidad compartida funcionando, sin mock
- [x] Un almacenamiento integrado al flujo (PostgreSQL)
- [x] Logs estructurados y correlacionados con `trace_id`
- [x] Traza distribuida gateway → servicio → PostgreSQL
- [x] `docker compose up` levanta todo, sin secretos en el repo
- [x] Test de carga ejecutado ([`tests/carga/`](../tests/carga/README.md))

**Documentación**

- [x] ADR nuevos: D2, D6, D7, D9, D10, D12 y D13
- [x] D1, D3 y D5 validados; D11 en su primera versión; D8
- [x] `README.md`, `SPEC.md` y `docs/ARCHITECTURE.md` con diagramas
- [x] Contrato OpenAPI v1 en `docs/contracts/`
- [ ] Bonus: anunciado o escrito que no hacemos ninguno
- [ ] Contrato del grupo de ropa conseguido (bloquea completar D9)

**Antes del tag**

- [ ] PRs de `docs/base` (Integrante 3) y `docs/adrs` (Integrante 4) mergeados en `main`
- [ ] `docker compose up` desde un clon limpio en al menos dos computadoras
- [ ] Demo ensayada de punta a punta en menos de 5 minutos

## Crear el tag

Con todo mergeado y el ensayo hecho, desde `main` actualizado:

```bash
git switch main
git pull
git tag -a entrega-2 -m "Entrega 2: dominio propio de punta a punta"
git push origin entrega-2
```

Si después del tag hay que corregir algo, se crea `entrega-2.1`; no se mueve un tag
ya publicado.
