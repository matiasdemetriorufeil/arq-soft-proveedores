# ADR-NNN — Título corto de la decisión (DN)

<!--
Plantilla de los ADR del grupo. Sale de la consigna (sección 5: opciones
evaluadas, por qué y consecuencias aceptadas) y del complemento (sección 6.1:
con qué lo medimos y qué nos haría cambiar de idea). Ejemplo completo:
ADR-002-arquitectura-interna.md.

Reglas:
- Un archivo por decisión: ADR-NNN-titulo-en-kebab-case.md, con NNN igual al
  número de la decisión de la consigna (D5 → ADR-005).
- Media carilla a una carilla. Si hace falta más, probablemente son dos decisiones.
- El revisor automático compara el ADR con el código. Lo que no esté
  implementado va con Estado "propuesto" y dice qué falta.
- Si la decisión cambia, no se borra: este ADR pasa a "reemplazado por ADR-XXX"
  y el nuevo dice a cuál reemplaza y por qué.
- Borrar estos comentarios al usarla.
-->

- **Estado:** propuesto | aceptado | aceptado e implementado | reemplazado por [ADR-XXX](ADR-XXX-....md)
- **Fecha:** AAAA-MM-DD
- **Entrega:** N (DD/MM)
- **Decisión de la consigna:** DN. Copiar los aspectos que la consigna pide justificar para esta decisión (sección 5).

## Contexto

Qué problema hay que resolver y qué lo condiciona: el requisito de la consigna,
lo que ya existe en el código, las restricciones del grupo. Sin la solución todavía.

## Opciones evaluadas

1. **Opción A.** Qué es y qué gana o pierde en nuestro caso.
2. **Opción B.** …
3. **Opción C.** …

## Qué elegimos

La opción elegida, en concreto: valores (timeouts, TTL, límites), dónde vive en
el código o en el compose y qué parte está implementada y qué parte no.

## Por qué

Los argumentos que inclinaron la balanza, atados a nuestro sistema y no genéricos.

## Consecuencias aceptadas

Los costos y limitaciones que asumimos a sabiendas.

## Con qué lo medimos

Al menos un número medido en nuestro sistema, con fecha y cómo se obtuvo. Si
todavía no se puede medir, decir qué se va a medir y con qué herramienta.

## Qué nos haría cambiar de idea

Las señales concretas (un número, un evento) que nos harían revisar la decisión.
