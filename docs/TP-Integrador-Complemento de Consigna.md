# TP Integrador — Arquitectura de Software 2026 (UCC)

> **Documento consolidado del grupo.** Reúne la consigna oficial, la presentación de la cátedra, las notas que tomamos en clase, el audio del profe y el chat con IA que usó un compañero.
> Fecha de corte: **22/10/2026**. La Entrega 2 es **mañana, viernes 23/10**.
>
> 🔎 Fuentes: **[C]** consigna oficial · **[P]** presentación de los profes · **[N]** notas de clase · **[A]** audio del profe · **[IA]** chat con IA. Lo marcado **[IA]** son propuestas que todavía no validó la cátedra.
>
> ⚖️ **Prioridad:** si dos fuentes no coinciden, **manda la consigna oficial [C]**. Lo que viene de otras fuentes y pide **más** que la consigna figura como **recomendado**, no como obligatorio. Este documento es un resumen de apoyo y **no reemplaza la consigna**.

---

## 0. Resumen en 30 segundos

- Hay que construir un **sistema de microservicios** propio: **mínimo 3 servicios + API Gateway + frontend web**. El sistema tiene que levantar con **un solo comando** (Docker) en cualquier máquina.
- Tiene que estar **testeado**, ser **observable** y **tolerar fallas**. Además se **integra con otro grupo**: publicamos una capacidad y consumimos la de otro grupo.
- **Nuestra integración (audio del profe):** el grupo de **paneles solares** consume nuestro **catálogo de proveedores con precios**, y nosotros consumimos el **catálogo de ropa** de otro grupo. El profe pidió **ampliar la idea con un contador de stock**.
- **Backend todo en Go**, con una base **relacional** y otra **no relacional**, y todo en **Docker**.
- La nota se compone de las 3 entregas, la presentación grupal (Game Day en vivo) y la **defensa individual** en el final. Pesa más **por qué** hicimos cada cosa que la cantidad de código.

---

## 1. Nuestro proyecto

### 1.1 Integración asignada [A]

| Rol | Con quién | Qué intercambiamos |
|---|---|---|
| **Somos proveedores de** | Grupo de **paneles solares** | Ellos consumen nuestro **catálogo de proveedores con precios** (recomendación de proveedores). |
| **Somos consumidores de** | Grupo de **catálogo de ropa** | Consumimos su catálogo. La tienda de ropa funciona como **un proveedor más** dentro de nuestro sistema, y **contabilizamos el stock** de lo que le compramos. |

Indicación extra del profe: *"tenemos que ampliar la idea, por ejemplo con contador de stock y más cosas"*.

### 1.2 Indicaciones del profe en clase [N]

| # | Indicación | Qué implica para nosotros |
|---|---|---|
| 1 | **Backend todo en Go** | Todos los microservicios se escriben en Go. Las propuestas de la IA con Node/Python/Java **no aplican**. |
| 2 | **Una base relacional y otra no relacional** | Por ejemplo, PostgreSQL + MongoDB. |
| 3 | **"No inventar caché, usar la que existe"** | Usar una caché estándar (Redis). Nada de implementar una caché casera. |
| 4 | **Demoras de actualización cortas** | El retraso entre un cambio en los datos y su aparición (en la búsqueda, por eventos, etc.) tiene que ser bajo y estar definido. |
| 5 | **Aislar los problemas** | Si se cae algo, el resto sigue funcionando (resiliencia, degradación controlada). |
| 6 | **Documentación y métricas de todo, incluso de los errores** | Pensadas para mantenimiento: saber qué pasó y qué hacer. |
| 7 | **Docker sí o sí** | Para que funcione igual en todas las computadoras. |
| 8 | **README con manual de uso** | Además de todo lo obligatorio, el README tiene que incluir un manual de uso. |

### 1.3 Dominio propuesto [IA — a validar]

**Plataforma de comparación y recomendación de proveedores, con gestión de stock y reposición.**

1. Importamos las **listas de precios de distintos proveedores**, cada una en su propio formato. Una de las fuentes es el **catálogo de ropa** del otro grupo.
2. Unificamos los productos en un **catálogo único** que mapea los códigos de cada proveedor a un mismo producto.
3. **Comparamos y rankeamos** proveedores por costo final (descuentos, flete, impuestos).
4. Llevamos el **stock** propio. Cuando un producto baja de su mínimo, se genera una **orden de reposición** con el mejor proveedor.
5. **Exponemos** al grupo de paneles solares la consulta de proveedores recomendados con precios.

**Acción principal con reglas de negocio** (sin esto el sistema sería "solo un ABM", y eso no alcanza):
- Reserva o descuento de stock **sin sobreventa**, con control de concurrencia.
- Detección del **punto de reorden** (stock por debajo del mínimo).
- Generación y confirmación de la **orden de reposición** con estados: `BORRADOR → SOLICITADA → CONFIRMADA`.

> ⚠️ En el último resumen, la IA describió el sistema como una "plataforma mayorista de paneles solares". Eso **no coincide con el audio del profe**: los paneles solares son **el grupo que nos consume**, no nuestro dominio. Ver la sección 10.

### 1.4 Requisitos del dominio [C — sección 1]

- Tiene que ser **distinto al dominio del TP de Desarrollo de Software (DSW)** y **distinto al de los demás grupos**.
- La propuesta tiene que estar **aprobada por los profesores**. Si el dominio cambia respecto del aprobado el 2/10, hay que **volver a confirmarlo con ellos**.
- No puede ser solo altas, bajas, modificaciones y consultas. La **acción principal** tiene que involucrar **restricciones, estados, validaciones o consecuencias relevantes** para el negocio.

---

## 2. Cronograma [C][P]

| Fecha | Instancia | Qué hay que tener |
|---|---|---|
| **Vie 2/10** | Arranque | Grupo confirmado, dominio aprobado, repo creado y accesible para la cátedra. ✅ |
| **Vie 9/10** | **Entrega 1 — Diseño y contrato con mock** | README, SPEC, `ARCHITECTURE.md` con diagramas de contexto y contenedores, límites de servicios y propiedad de los datos, contrato de nuestra capacidad publicado con un **mock que ya responde** (todavía sin base real), servicios levantando con un comando. ADRs D1 y D8, más versiones iniciales de D3 y D5. ✅ |
| **Vie 23/10** | **Entrega 2 — Dominio propio de punta a punta** | Ver el checklist de la sección 11. |
| Mié 28/10 | Parcial teórico | Sin entrega de TP. |
| Mié 4/11 | Recuperatorio | Sin entrega de TP. |
| **Vie 6/11** | **Entrega 3 — Conexión real** [P] | El mock se reemplaza por el **consumo real** del otro grupo. Nuestra capacidad **se sube a la nube**. Se **mide la caché** (antes y después) y se **corre el test de carga**. Queda escrito el **plan ante la caída de cada dependencia**. Ese día se sortea la fecha de presentación y **el código queda congelado**. |
| **Mié 11/11 o Vie 13/11** | **Presentación grupal — Game Day en vivo** | Tablero y alertas configurados, con una **caída simulada en el momento** (propia y/o del proveedor). Ver la sección 3.2. |
| Final | **Defensa individual** | Ver la sección 3.3. |

**En cada entrega:** se deja un **tag en el repo** y se hace una **demo de 5 minutos** en el práctico [P].

---

## 3. Requisitos técnicos

### 3.1 Checklist general [C][P]

| Área | Requisito |
|---|---|
| **Servicios** | Mínimo **3 microservicios + API Gateway**. Cada servicio es **dueño de sus datos**. |
| **Patrones** | **Al menos 2 servicios con patrones de arquitectura interna diferentes**, vistos en la materia, aplicados de forma reconocible y justificados (qué resuelve cada uno). |
| **Frontend** | Interfaz web que cubra todas las funcionalidades: **login, búsqueda, detalle, acción principal y "mis reservas"** (o su equivalente, por ejemplo "mis órdenes"). |
| **Entrada única** | El Gateway es el único punto de entrada al backend, con **autenticación, direccionamiento y control de tráfico** (rate limiting). |
| **Balanceo de carga** | Al menos un servicio con **2 o más instancias**. El balanceo tiene que **detectar cuándo una instancia no está disponible**, y la distribución tiene que **verse en la observabilidad**. |
| **Comunicación síncrona** | Definir **timeout**, manejo de errores, respuestas tardías o ausentes y **política de reintentos**. |
| **Comunicación asíncrona** | Mensajería con un **evento de dominio publicado y consumido**, **consumidor idempotente** y tratamiento de **mensajes fallidos** (DLQ). |
| **Búsqueda** | **Motor de búsqueda real** (un `LIKE` en SQL no cuenta), con **paginación, filtros del dominio y al menos un orden**. El índice tiene que mantenerse **sincronizado**, con un **retraso máximo tolerable definido** y un mecanismo de **actualización o reconstrucción del índice**. |
| **Caché** | Caché en un **flujo de lectura pesado**, justificada por una **necesidad observable**, con **métricas antes y después**. |
| **Persistencia** | **2 tipos de almacenamiento: uno relacional y uno no relacional**, elegidos según el patrón de acceso de cada servicio. |
| **Consistencia** | Al menos **una operación con garantías fuertes**: que no pueda duplicarse ni perderse información. Para nosotros, la reserva o descuento de stock. |
| **Resiliencia** | Identificar las dependencias de cada servicio y definir qué pasa si fallan o responden lento. **Al menos un mecanismo de protección** (circuit breaker, fallback, bulkhead, etc.), demostrado con una **caída provocada** y registrado en el `POSTMORTEM.md`. |
| **Observabilidad** | Mínimo de la sección 2.10: **logs estructurados**, **métricas** y **un tablero** del estado general. Para la Entrega 2 se piden además **logs correlacionados y una traza distribuida**; para la defensa, **trazas completas y una alerta**. |
| **Testing** | 1) **Unitarios** sobre las reglas de negocio, con una cobertura que represente calidad. 2) **Integración por servicio contra una dependencia real** (base de datos o broker). 3) **Carga**, con resultados documentados y analizados. 4) **Test de contrato** sobre la capacidad que consumimos. |
| **Infra / repo** | **Repo público**, versión final en `main`, estrategia de ramas y revisiones definida. **Un solo comando** levanta todo, sin configuraciones manuales. **Sin secretos en el repo**. **Pipeline CI que corre los tests en cada push** [P]. |
| **Nube** | Para la presentación, la capacidad propia tiene que estar **desplegada en la nube con URL pública** [C]. El resto puede quedar local [P]. ⚠️ La sección 4.1 exige que la primera versión operativa esté en un **"entorno accesible"**, sin fecha. Otra versión del enunciado (la del PDF) dice "a partir de M2", es decir, la **Entrega 2**. **Hay que confirmarlo con el profe** (ver la sección 10). |

### 3.2 Presentación grupal (11 o 13/11) [C]

- Frontend completo, desde el login hasta la funcionalidad compartida.
- Gateway con autenticación, direccionamiento y control de tráfico.
- Al menos 3 servicios operativos.
- Capacidad propia **desplegada en la nube con URL pública**.
- Capacidad del proveedor **incorporada al flujo principal**.
- 2 tipos de almacenamiento integrados.
- Caché con **medición comparativa antes y después**.
- 2 patrones de arquitectura interna reconocibles.
- Evento de dominio con **consumidor idempotente** y manejo de mensajes no procesables.
- Búsqueda con paginación, filtros, ordenamiento y mecanismo de actualización o reconstrucción del índice.
- Decisiones verificadas con **evidencia medida en el sistema**.
- Sección de **limitaciones conocidas y deuda técnica** en `ARCHITECTURE.md`.

### 3.3 Defensa individual (final) [C]

- Operación crítica con las garantías de consistencia y concurrencia definidas.
- Tests unitarios y un test de integración contra una dependencia real.
- Test de contrato sobre la capacidad consumida.
- Balanceo operativo con 2 o más instancias, verificación de disponibilidad y evidencia de la distribución.
- Simulación de la caída de una dependencia propia y/o del proveedor.
- Test de carga con análisis de capacidad y **el principal límite identificado**.
- Comportamiento documentado ante la caída de cada dependencia relevante.
- Logs, métricas, trazas, tablero y alerta.
- Ensayo de caída controlada con su `POSTMORTEM.md`.

> ⚠️ El revisor automático compara lo que **dicen los ADRs** con lo que **hace el código**. **Esas diferencias son las primeras preguntas del final** [P].

---

## 4. Integración entre grupos — reglas [C][P]

**Como proveedores (nuestra capacidad):**
- La capacidad tiene que poder usarse **sin explicaciones privadas**, solo con el contrato y la documentación.
- El contrato es **formal, versionado y en formato estándar procesable** (por ejemplo, OpenAPI). Va en `docs/contracts/` y tiene que indicar:
  - operaciones disponibles;
  - datos de entrada y salida;
  - **errores** y cómo interpretarlos;
  - **autenticación e idempotencia**;
  - **versión vigente**;
  - **ejemplos** suficientes para implementar y probar.
- Desde la primera versión operativa, la capacidad tiene que seguir funcionando **hasta el final de la evaluación**.
- **No se puede cambiar la API sin avisar y versionar** [P].

**Como consumidores (catálogo de ropa):**
- La capacidad tiene que formar parte de un **flujo importante del negocio**. Una llamada aislada, una pantalla de prueba o una integración **sin consecuencias sobre el negocio** no cuentan. Para nosotros, el catálogo de ropa tiene que **impactar en la comparación de precios y en el stock/reposición**, no quedar solo importado.
- La llamada sale **desde el microservicio responsable**, nunca desde el frontend ni a través de nuestro propio Gateway.
- Hay que trabajar **solo con su contrato publicado**. Si algo no está claro, se **abre un issue** en su repo [P].
- Obligatorio: **test de contrato**, **timeouts**, manejo de errores, **definir qué ve el usuario si el proveedor se cae**, y demostrarlo en el Game Day.
- Si el grupo proveedor abandona, **la cátedra levanta un mock** y no nos perjudica [P].

---

## 5. Documentación obligatoria [C]

| Archivo | Contenido mínimo |
|---|---|
| `README.md` | Dominio, objetivo, flujo principal, **cómo correrlo localmente**, cómo acceder a la parte desplegada, dónde está el resto de la documentación y **manual de uso** [N]. |
| `SPEC.md` (o equivalente) | Funcionalidades, reglas de negocio y **criterios de aceptación**. Pueden ser historias de usuario, casos de uso o backlog. |
| `docs/ARCHITECTURE.md` | Texto y diagramas (contexto + contenedores): servicios, responsabilidades, datos de cada uno, comunicaciones y despliegue. Al final, **limitaciones y deuda técnica**. |
| `docs/adr/ADR-XXX.md` | Un archivo por decisión. |
| `docs/contracts/README.md` | Contrato versionado, ejemplos y errores. Todo lo que el otro grupo necesita para consumirnos. |
| `docs/POSTMORTEM.md` | Caída provocada: impacto, línea temporal, detección, respuesta, causa y acciones de mejora. |
| Uso de IA (1 carilla) [P] | Cómo vamos a trabajar con IA y por qué. Al final, qué funcionó y qué cambiamos. |

> La documentación **se evalúa junto con el código** y tiene que actualizarse **durante** el desarrollo, no al final.

---

## 6. ADRs (registros de decisiones)

### 6.1 Plantilla [C sección 5 + P]

Media carilla por decisión, con:
1. **Contexto**
2. **Opciones evaluadas** [C]
3. **Qué elegimos**
4. **Por qué** [C]
5. **Consecuencias aceptadas** [C] (qué costos o limitaciones asumimos)
6. **Con qué lo medimos** [P] (un número medido en nuestro sistema)
7. **Qué nos haría cambiar de idea** [P]

Las decisiones se registran **durante** el diseño y la implementación, y se actualizan en el hito que corresponde [C].

Si una decisión cambia, **no se borra**: el ADR viejo se marca como *reemplazado* y el nuevo indica a cuál reemplaza y por qué.

### 6.2 Decisiones y cuándo se entregan [C]

| # | Decisión | Qué hay que justificar | E1 (9/10) | E2 (23/10) | Final |
|---|---|---|---|---|---|
| D1 | Límites de los servicios | Criterio de separación, relaciones entre servicios, propiedad de los datos | ✅ | Validar | |
| D2 | Arquitectura interna | Patrones de cada servicio y por qué encajan | | ✅ | |
| D3 | Persistencia | Tipo de base por servicio, patrones de acceso, limitaciones | Inicial | Validar | |
| D4 | Consistencia y concurrencia | Operación crítica, duplicados, pérdida, fallas parciales | | | ⚠️ Sin fecha asignada; hacerla antes de la presentación |
| D5 | Comunicación | Síncrono vs. asíncrono, timeouts, reintentos, eventos, idempotencia | Inicial | Validar | |
| D6 | Búsqueda | Qué se indexa, consultas, reconstrucción, retraso aceptable | | ✅ | |
| D7 | Caché | Qué se guarda, TTL, invalidación, fallas, medición | | ✅ | |
| D8 | Contrato propio | Diseño, publicación, compatibilidad, versionado | ✅ | Actualizar si cambió | |
| D9 | Consumo del proveedor | Adaptación al contrato, errores, indisponibilidad | | ✅ | |
| D10 | Resiliencia | Mecanismos para limitar la propagación de fallas o degradar | | ✅ | |
| D11 | Observabilidad | Logs, métricas, trazas, tableros, SLO y alertas | | v1 | Completa |
| D12 | Balanceo de carga | Servicio elegido, estrategia, health checks, caída de una instancia | | ✅ | |
| D13 | Capacidad y costos | Resultados de carga, límite principal, escalado, costos | | ✅ | |

---

## 7. Arquitectura propuesta [IA — a validar con el grupo]

### 7.1 Servicios

| Servicio | Responsabilidad | Almacenamiento |
|---|---|---|
| `import-service` | Ingesta y normalización de listas de precios de proveedores (CSV/Excel con formatos variables) y del catálogo de ropa consumido. | **MongoDB** (esquemas semiestructurados) |
| `catalog-service` | Catálogo maestro unificado; mapeo de códigos de cada proveedor a un mismo producto. | **PostgreSQL** |
| `pricing-search-service` | Cálculo del costo final, ranking de proveedores y búsqueda con filtros. **Probablemente sea el que expone nuestra capacidad.** | **Elasticsearch/Meilisearch** + **Redis** (caché) |
| `stock-service` | Stock, reservas sin sobreventa, punto de reorden y órdenes de reposición. | **PostgreSQL** (transacciones ACID) |
| `api-gateway` | Entrada única: JWT, ruteo y rate limiting. | — |

### 7.2 Eventos (RabbitMQ)

| Evento | Publica | Consume | Efecto |
|---|---|---|---|
| `ListaPreciosActualizadaEvent` | import-service | pricing-search-service | Reindexa los precios de ese proveedor. |
| `StockBajoMinimoEvent` | stock-service | pricing-search-service | Precalcula la cotización con el mejor proveedor para reponer. |

### 7.3 Capacidad expuesta (borrador)

- `GET /api/v1/proveedores/recomendados?producto=...` → proveedores rankeados con precio.
- Contrato en `docs/contracts/openapi.yaml`, con versionado en la URL (`/v1/`).

### 7.4 Estructura del repo

```
├── README.md
├── SPEC.md
├── docker-compose.yml        # un solo comando levanta todo
├── .github/workflows/        # CI: tests en cada push
├── api-gateway/
├── frontend/
├── services/
│   ├── import-service/       # Go + MongoDB
│   ├── catalog-service/      # Go + PostgreSQL
│   ├── pricing-search-service/  # Go + motor de búsqueda + Redis
│   └── stock-service/        # Go + PostgreSQL
└── docs/
    ├── ARCHITECTURE.md
    ├── POSTMORTEM.md
    ├── contracts/  (README.md, openapi.yaml)
    └── adr/        (ADR-001.md ... ADR-013.md)
```

### 7.5 Stack sugerido

Go · PostgreSQL · MongoDB · Redis · RabbitMQ · Elasticsearch o Meilisearch · Gateway (Kong / Traefik / Nginx) · Prometheus + Grafana · Jaeger u OpenTelemetry · k6 para carga · Docker Compose.

---

## 8. IA y evaluación [P]

1. **Pueden usar IA.** La cátedra pone un archivo en la raíz del repo que hace que la IA **registre los prompts y no decida por nosotros**.
2. **Cada decisión se respalda con un número medido en nuestro sistema.** "Lo dijo la IA" no es justificación.
3. Hay que escribir una carilla sobre cómo trabajamos con IA.
4. **Ninguna IA conoce la API del otro grupo**: hay que leer su documentación.
5. Un **revisor automático** corre en cada entrega y marca qué falta y las diferencias entre ADRs y código.

---

## 9. Bonus (hasta 2, máximo 1 punto cada uno) [C][P]

Se **anuncian en la Entrega 2** y se suman al alcance documentado. Para puntuar, cada bonus tiene que estar integrado, documentado, testeado y defendido.

Opciones: agente de IA con un protocolo estándar (por ejemplo, MCP) · tiempo real · capa de agregación (BFF) · orquestación (Kubernetes) · tests e2e · autoescalado · búsqueda semántica · serverless · deploy en la nube.

---

## 10. ⚠️ Puntos a confirmar e inconsistencias detectadas

| # | Tema | Problema | Qué hacer |
|---|---|---|---|
| 1 | **Nuestro dominio** | La IA terminó describiéndonos como "plataforma mayorista de paneles solares". Según el audio, **los paneles solares son el grupo que nos consume**; nosotros somos el comparador/recomendador de proveedores. | Confirmar en el grupo y corregir README, SPEC y ADRs. Si el dominio difiere del aprobado, **reconfirmarlo con los profes** (sección 1 de la consigna). |
| 2 | **Rubro del stock** | "Contabilizar el stock de eso" podría referirse a lo que compramos al proveedor de ropa o a un stock multirubro. | Definir qué productos maneja `stock-service`. |
| 3 | **Los 2 patrones** | La IA propuso *API Gateway* y *Event-Driven* como los dos patrones, pero la consigna pide **patrones de arquitectura interna distintos en 2 servicios** (D2). Esos dos son patrones a nivel sistema. | Usar patrones internos, por ejemplo **hexagonal / puertos y adaptadores** en `stock-service` y **en capas** o **CQRS** en `pricing-search-service`. |
| 4 | **Lenguaje** | La IA sugirió Node/Python/Java. El profe dijo **todo en Go**. | Todo en Go. |
| 5 | **Entrega 2: alcance** | **Obligatorio según la consigna (6.3):** al menos 1 servicio operativo, la funcionalidad a compartir funcionando, al menos 1 almacenamiento, logs correlacionados con traza y los ADRs. La presentación además pide **2 patrones, 2 almacenamientos, mensajería y búsqueda**. | Cumplir sí o sí lo de la consigna; lo de la presentación es **recomendado** (ver la sección 11). |
| 6 | **Entrega 3 (6/11)** | Aparece solo en la presentación; la consigna escrita no la lista. | Tomarla como válida. |
| 7 | **Asignación de proveedores** | La presentación dice "planilla compartida"; la consigna dice "asignan los profes". | Ya resuelto por el audio del profe. |
| 8 | **D4 (consistencia)** | No está asignada a ninguna entrega. | Hacerla junto con la Entrega 2 o 3; se evalúa en la defensa. |
| 9 | **Formato de ADR** | La consigna menciona una "sección 8" que no existe. | Usar la plantilla de la sección 6.1: los campos que exige la consigna (sección 5: opciones, por qué, **consecuencias aceptadas**) más los de la presentación (medición y qué nos haría cambiar). |
| 10 | **Balanceo de carga** | La IA casi no lo trató. | Elegir el servicio (por ejemplo, `pricing-search-service` con 2 réplicas detrás del Gateway, con health checks). |
| 11 | **Capacidad accesible: ¿desde cuándo?** | La consigna (4.1) pide un "entorno accesible" sin fecha. La versión del PDF dice "a partir de M2" (Entrega 2). La presentación dice que sube a la nube en la Entrega 3. | **Preguntarle al profe ya.** Mientras tanto, tenerla accesible lo antes posible: el grupo de paneles solares la necesita para integrarse. |
| 12 | **Flujo de la integración consumida** | Si el catálogo de ropa solo se importa y no afecta nada, puede considerarse una integración "sin consecuencias sobre el negocio" (4.1). | Hacer que impacte en la comparación y en el stock/reposición, con timeouts, reintentos y qué ve el usuario si el grupo de ropa se cae. |

---

## 11. ✅ Checklist Entrega 2 — Vie 23/10

**Código — obligatorio según la consigna (6.3)**
- [ ] **Al menos un servicio operativo**.
- [ ] Nuestra **funcionalidad a compartir funcionando**, ya no el mock.
- [ ] **Al menos un tipo de almacenamiento** integrado al flujo.
- [ ] `docker compose up` levanta todo (2.12).

**Código — recomendado según la presentación [P]**
- [ ] El flujo principal corre **de punta a punta en local**.
- [ ] PostgreSQL **y** MongoDB integrados.
- [ ] **Mensajería interna** funcionando: un evento publicado y consumido en RabbitMQ.
- [ ] **Motor de búsqueda** con filtros, paginación y orden.
- [ ] **2 patrones de arquitectura interna** reconocibles en el código.
- [ ] Capacidad accesible para el grupo de paneles solares (confirmar con el profe; ver la sección 10, punto 11).

**Observabilidad**
- [ ] Logs **estructurados en JSON** y **correlacionados** (trace/correlation ID).
- [ ] Una **traza distribuida** del flujo principal (Gateway → servicios → evento).

**Documentación**
- [ ] ADRs nuevos: **D2, D6, D7, D9, D10, D12, D13**.
- [ ] Validar **D1, D3 y D5**; primera versión de **D11**; actualizar **D8** si cambió el contrato.
- [ ] **Anunciar los bonus** (si vamos a hacer alguno).
- [ ] README, SPEC y ARCHITECTURE actualizados.

**Entrega**
- [ ] **Tag en el repo** (por ejemplo, `entrega-2`).
- [ ] Ensayar la **demo de 5 minutos**:
  1. `git checkout <tag>`
  2. `docker compose up`
  3. Flujo principal funcionando.
  4. Evento publicado y consumido.
  5. Traza con el ID viajando entre servicios.
