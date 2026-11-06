# ADR-002 — Arquitectura interna de los servicios (D2)

- **Estado:** aceptado e implementado: hexagonal en `proveedores-service` y **en capas en `stock-service`** (los dos patrones, en código)
- **Fecha:** 2026-10-22; actualizado el 2026-11-06 con `stock-service`
- **Entrega:** 2 (23/10); el segundo patrón se completa en la 3 (11/11)
- **Decisión de la consigna:** D2. Estilos o patrones elegidos, dependencias internas y por qué cada uno encaja con el problema del servicio.

## Contexto

La consigna (2.1) pide que al menos dos servicios usen patrones de arquitectura interna distintos, vistos en la materia y reconocibles en el código. API Gateway y Event-Driven son patrones a nivel sistema y no cuentan para este punto.

`proveedores-service` publica la capacidad que consume el grupo de paneles solares: `GET /api/v1/proveedores/recomendados`. Su núcleo es una regla de negocio (costo total con descuento por volumen y flete, filtro por stock, orden con desempates) que:

- tiene que poder probarse sin base de datos ni HTTP, porque es lo que otro grupo integra y lo primero que se evalúa;
- va a recibir otras fuentes de datos: hoy lee PostgreSQL, y más adelante puede sumar la caché (D7) o las listas del proveedor de ropa (D9);
- va a sumar otras entradas además del HTTP (por ejemplo, un consumidor de eventos de precios actualizados).

## Opciones evaluadas

1. **Hexagonal (puertos y adaptadores).** El dominio define lo que necesita como interfaces (`ports`); HTTP y PostgreSQL son adaptadores intercambiables.
2. **En capas clásica (handler → service → repository).** Simple y conocida, pero la capa de servicio suele depender del repositorio concreto y las reglas terminan mezcladas con SQL o con HTTP.
3. **Transaction script / todo en el handler.** Lo más rápido de escribir para un solo endpoint, pero no deja probar la regla aislada y crece mal.
4. **CQRS.** Separa lectura y escritura. Este servicio hoy solo lee, así que agregaría complejidad sin resolver ningún problema.

## Qué elegimos

**Hexagonal en `proveedores-service`.** Así se ve en el código:

```
cmd/server/                  composición: arma adaptadores, aplica migraciones, apagado ordenado
internal/domain/             Oferta, Recomendacion, CostoTotal, Rankear, errores de dominio (solo stdlib)
internal/ports/              OfertasRepo, Salud
internal/adapters/http/      adaptador de entrada: endpoint, API key, request log, problem+json
internal/adapters/postgres/  adaptador de salida: pgx, consulta con join, migraciones embebidas
```

Regla de dependencias: `adapters → ports → domain`. `domain` no importa nada fuera de la biblioteca estándar. El handler llama a `repo.OfertasPorProducto` y después a `domain.Rankear`: el dominio recibe las ofertas ya cargadas y no sabe de dónde vienen.

**En capas (handler → service → repository) en `stock-service`**, implementado desde el 06/11. Su problema es distinto: el riesgo no está en reglas de cálculo que haya que aislar, sino en ejecutar dentro de una transacción la reserva de stock sin sobreventa ([ADR-004](ADR-004-consistencia-y-concurrencia.md)). Así se ve en el código:

```
cmd/server/            composición: arma las tres capas, aplica migraciones, apagado ordenado
internal/handler/      HTTP: validación, API key, request log, problem+json, health checks
internal/service/      negocio: abre la transacción y decide; errores de negocio sin tipos HTTP
internal/repository/   datos: pool pgx, SELECT … FOR UPDATE, migraciones, span por consulta
```

Regla de dependencias: `handler → service → repository`, en un solo sentido. **`repository` es el único paquete que importa `pgx`** y el único que escribe SQL; `service` solo ve el tipo opaco `repository.Tx` —lo recibe y lo vuelve a pasar, sin poder ejecutar nada con él—, así que el límite transaccional lo abre la capa de negocio y el bloqueo de fila lo ejecuta la de datos. Es lo contrario de hexagonal: acá no hay un dominio puro al que haya que proteger de la infraestructura, hay una secuencia de pasos transaccionales que conviene leer de arriba hacia abajo.

Con los dos estilos en código, el requisito 2.1 queda cubierto de forma reconocible: `proveedores-service` tiene `internal/domain` + `internal/ports` + `internal/adapters/`, y `stock-service` tiene `internal/handler` + `internal/service` + `internal/repository`.

Lo que **todavía no** está en capas porque no existe: las transiciones de la orden de reposición (`BORRADOR → SOLICITADA → CONFIRMADA`, más los finales `VENCIDA` y `EN_REVISION` que agrega [ADR-009](ADR-009-consumo-proveedor.md)) son de HU-14 y van a entrar en la misma capa de servicio.

## Por qué

- **Testeabilidad:** la regla se prueba con tests de tabla sin infraestructura, y el handler con un repositorio falso que simula 404 y 503 sin levantar Postgres.
- **Cambiar adaptadores sin tocar la regla:** agregar caché (D7) es un decorador de `OfertasRepo`; consumir otra fuente (D9) es otro adaptador. El ranking no cambia.
- **Errores con significado de negocio:** `ErrProductoNoEncontrado` y `ErrParametroInvalido` viven en el dominio. El adaptador HTTP los traduce a `404 PRODUCTO_NO_ENCONTRADO` y `400 PARAMETRO_INVALIDO`; cualquier otro error del repositorio se traduce a `503 NO_DISPONIBLE`.
- **Encaja con el tamaño del servicio:** son tres paquetes internos con una sola dependencia de runtime (`pgx`), sin frameworks.

## Consecuencias aceptadas

- Hay más archivos e indirección que en un handler único: para un solo endpoint parece de más.
- La validación de parámetros está duplicada a propósito: el handler valida para responder 400 con un detalle claro, y `Rankear` vuelve a validar para que el dominio nunca acepte entradas inválidas.
- La consulta trae todas las ofertas del SKU y el filtro de stock y el orden se hacen en memoria. Es aceptable con decenas de ofertas por producto; con miles habría que llevar el filtro a SQL, a costa de duplicar la regla.
- Los cálculos usan `float64` con un redondeo final a 2 decimales (primero se redondea a 6 decimales para absorber el ruido de punto flotante). Si se exigiera exactitud contable, habría que pasar a centavos enteros o a un tipo decimal.

## Con qué lo medimos

- Cobertura de tests (`go test ./... -cover`, 22/10): `internal/domain` **100 %** y `internal/adapters/http` **95,3 %**, sin infraestructura.
- Latencia del endpoint en local contra PostgreSQL 16 (campo `duration_ms` del log de requests, 22/10): **1 a 3 ms** por request. Es una medición puntual; el test de carga (D13) la va a reemplazar.
- Base caída: `503` en **2 ms**. Base pausada: `503` en **2,0 s**, que es el timeout configurado.
- Para comprobar el aislamiento: `go list -deps ./internal/domain` no muestra ningún paquete fuera de la biblioteca estándar.

Mediciones del 06/11 sobre `stock-service` (`go test ./... -cover`, sin infraestructura):

- Cobertura: `internal/service` **90,0 %** e `internal/handler` **89,3 %**, con un repositorio falso y un servicio falso respectivamente. 37 casos en verde.
- **Un solo paquete escribe SQL e importa pgx:** `internal/repository`. `go list -f '{{join .Imports "\n"}}' ./internal/service` no lo muestra.
- La separación se ve en que `internal/service` prueba la regla (idempotencia, stock insuficiente, SKU inexistente, una sola transacción en el camino feliz) y `internal/handler` prueba solo la traducción a `problem+json`: ningún test necesita las dos capas a la vez.

## Qué nos haría cambiar de idea

- Si el servicio pasa a tener escrituras con alto volumen de lectura (por ejemplo, actualización masiva de listas de precios mientras se consulta), evaluar CQRS con un modelo de lectura separado.
- Si la regla de ranking pasa a hacerse mayormente en SQL o en el motor de búsqueda (D6), el dominio aislado dejaría de aportar y convendría simplificar a capas.
- Si en la defensa no se reconoce la diferencia entre hexagonal y en capas en el código, reforzar la separación del segundo servicio o elegir un estilo más contrastante.
