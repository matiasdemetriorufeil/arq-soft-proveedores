# Demo y checklist de la Entrega 2 (23/10)

Qué tiene que estar en `main` antes del tag, cómo se crea el tag `entrega-2`, cómo
se prepara la máquina y el guion de 5 minutos, con las preguntas probables al final.
Los comandos están probados el 23/10 contra el sistema levantado con
`docker compose up`, en PowerShell y en bash.

## 1. Checklist (consigna 6.3)

**Código**

- [x] Al menos un servicio operativo (`proveedores-service`, dos réplicas)
- [x] Capacidad compartida funcionando, sin mock
- [x] Un almacenamiento integrado al flujo (PostgreSQL)
- [x] Logs estructurados y correlacionados con `trace_id`
- [x] Traza distribuida gateway → servicio → PostgreSQL
- [x] `docker compose up` levanta todo, sin secretos en el repo
- [x] Test de carga ejecutado ([`tests/carga/`](../tests/carga/README.md))

**Decisiones (ADR)**

- [x] Nuevas en esta entrega: D2, D6, D7, D9, D10, D12 y D13
- [x] D5 publicado por primera vez (la versión inicial de la Entrega 1 no se presentó)
- [x] D1 y D3 validados; D8 al día con el contrato; D11 en su primera versión
- [x] Los que no están implementados (D6, D7, D9 y la mensajería de D5) dicen **propuesto**

**Documentación**

- [x] `README.md` con manual de uso, `SPEC.md` y `docs/ARCHITECTURE.md` con diagramas
- [x] Contrato OpenAPI v1 en `docs/contracts/`
- [x] Bonus: anunciado que no tomamos ninguno (README y ARCHITECTURE)
- [x] Contrato del grupo de ropa conseguido: FAV v1.1.0, copia en `docs/contracts/consumidos/` y D9 diseñado sobre él

**Antes del tag**

- [ ] PRs de `docs/base` (Integrante 3) y `docs/adrs` (Integrante 4) mergeados en `main`
- [ ] `docker compose up` desde un clon limpio de `main` en al menos dos computadoras
- [ ] Demo ensayada de punta a punta sobre `main`, en menos de 5 minutos

## 2. Crear el tag

Con todo lo anterior tildado, desde `main` actualizado:

```bash
git switch main
git pull
git tag -a entrega-2 -m "Entrega 2: dominio propio de punta a punta"
git push origin entrega-2
```

Si después del tag hay que corregir algo, se crea `entrega-2.1`; no se mueve un tag
ya publicado.

## 3. Antes de entrar (10 minutos antes)

Ya con el tag publicado:

```bash
git clone https://github.com/matiasdemetriorufeil/arq-soft-proveedores.git demo-e2
cd demo-e2
git checkout entrega-2
cp .env.example .env              # PowerShell: Copy-Item .env.example .env
docker compose up -d --build      # la primera vez tarda unos minutos en construir
docker compose ps                 # los 5 contenedores tienen que decir (healthy)
```

Dejar abiertos una terminal en la raíz del repo, Jaeger en
<http://localhost:16686> y el repo en GitHub en `docs/`.

> **En PowerShell** se usa `curl.exe`, no `curl` (que es un alias de
> `Invoke-WebRequest`), y cada comando va en una sola línea. Los bloques de abajo
> traen las dos versiones.

## 4. Guion (5 minutos)

### 4.1 Qué es y cómo se levanta (45 s)

- "Comparador de proveedores: dado un producto y una cantidad, rankea a los
  proveedores por costo total. Es la capacidad que consume el grupo de paneles solares."
- Mostrar `docker compose ps`: gateway, dos réplicas de `proveedores-service`,
  PostgreSQL y Jaeger, todos `healthy`, con **un solo comando** y sin secretos en el
  repo (`.env` se copia de `.env.example`).

### 4.2 La capacidad funcionando (1 min)

```bash
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=3" -H "X-API-Key: clave-de-desarrollo"
```

```powershell
curl.exe -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO&cantidad=10&limite=3" -H "X-API-Key: clave-de-desarrollo"
```

- Explicar el primer resultado: `costo_total = precio × cantidad × (1 − descuento) + flete`.
  El descuento aplica porque la cantidad llega al mínimo (`descuento_aplicado: true`),
  y las ofertas sin stock suficiente quedan afuera.
- Mostrar el header `X-Request-ID` de la respuesta.

### 4.3 Errores del contrato (45 s)

```bash
# 404: el SKU no existe
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=NO-EXISTE" -H "X-API-Key: clave-de-desarrollo"
# 401: sin API key
curl -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO"
```

```powershell
curl.exe -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=NO-EXISTE" -H "X-API-Key: clave-de-desarrollo"
curl.exe -i "http://localhost:8080/api/v1/proveedores/recomendados?producto=PAN-550W-MONO"
```

- Señalar `application/problem+json`, el campo `code` y que el `type` apunta a la
  tabla de errores de [`docs/contracts/README.md`](contracts/README.md).

### 4.4 Un `trace_id` de los logs a Jaeger (1 min 15 s)

Cada corrida usa un `X-Request-ID` nuevo, así el filtro devuelve solo esta request
aunque se haya ensayado antes con los mismos contenedores.

```bash
RID=demo-$(date +%H%M%S)
curl -s -o /dev/null -H "X-Request-ID: $RID" -H "X-API-Key: clave-de-desarrollo" "http://localhost:8080/api/v1/proveedores/recomendados?producto=INV-5KW-HIB&cantidad=3"
docker compose logs --no-log-prefix api-gateway proveedores-service-1 proveedores-service-2 | grep "\"$RID\""
```

```powershell
$rid = "demo-" + (Get-Date -Format HHmmss)
curl.exe -s -o NUL -H "X-Request-ID: $rid" -H "X-API-Key: clave-de-desarrollo" "http://localhost:8080/api/v1/proveedores/recomendados?producto=INV-5KW-HIB&cantidad=3"
docker compose logs --no-log-prefix api-gateway proveedores-service-1 proveedores-service-2 | Select-String "`"$rid`""
```

- Las dos líneas (gateway y la réplica que atendió) tienen el **mismo `request_id`
  y el mismo `trace_id`**.
- Copiar el `trace_id` y abrir `http://localhost:16686/trace/<trace_id>`: 4 spans,
  gateway → llamada a la réplica → servicio → `SELECT` a PostgreSQL. El atributo
  `upstream` dice qué réplica atendió.

### 4.5 Balanceo y caída de una réplica (45 s, si sobra tiempo)

```bash
docker compose stop proveedores-service-1
# repetir el curl de 4.2: sigue respondiendo 200 por la otra réplica
docker compose start proveedores-service-1
```

- En los logs del gateway aparece `upstream caído` y, al volver, `upstream recuperado`
  ([ADR-012](adr/ADR-012-balanceo-de-carga.md)).

### 4.6 Documentación y decisiones (30 s)

- [`README.md`](../README.md), [`SPEC.md`](../SPEC.md), [`ARCHITECTURE.md`](ARCHITECTURE.md)
  con diagramas y el contrato [`openapi.yaml`](contracts/openapi.yaml).
- Las decisiones de la entrega son las del checklist (sección 1). Aclarar que las que
  no están implementadas están marcadas **propuestas**.
- Para cerrar, el test de carga: **1 200 req/s sostenidas con p95 bajo 300 ms; el límite es la CPU y el que más gasta es el gateway**
  ([ADR-013](adr/ADR-013-capacidad-y-costos.md)).

## 5. Preguntas probables y dónde está la respuesta

| Pregunta | Respuesta corta | Dónde |
|---|---|---|
| ¿Por qué el balanceo está en el gateway y no en nginx? | Ya está en el camino de todas las requests y se puede demostrar con logs y spans | ADR-012 |
| ¿Qué pasa si se cae PostgreSQL? | `503 NO_DISPONIBLE` en 2 ms, o en 2 s si está colgada; es un punto único de falla | ADR-010 |
| ¿Por qué no reintentan ante un timeout? | La réplica puede estar procesando: reintentar suma carga a algo que ya está lento | ADR-005 |
| ¿Qué hace el sistema si `/readyz` da 503? | En compose solo marca el contenedor `unhealthy`; el reparto lo decide el sondeo de `/healthz` del gateway | ADR-010 |
| ¿Cuál es el límite del sistema? | La CPU. El gateway es el que más gasta (40 % del total por request) y reutiliza solo 2 conexiones por réplica; con el pool ajustado, su CPU baja 31 % y 2 000 req/s entran en el objetivo de latencia y errores | ADR-013 |
| ¿Dónde están la mensajería, la búsqueda y la caché? | Diseñadas y marcadas como propuestas; se implementan para la presentación | ADR-005, 006 y 007 |
| ¿Y el grupo de ropa? | FAV: sincronizamos su catálogo cada 5 min como un proveedor más, y la reposición le hace pedidos reales que seguimos por su webhook firmado. Está diseñado, no implementado | ADR-009 |
| ¿Toman bonus? | No, y quedó anunciado en esta entrega | README |
