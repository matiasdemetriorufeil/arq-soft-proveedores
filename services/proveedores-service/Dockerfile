# syntax=docker/dockerfile:1

# Ojo: el contexto de build es la raíz del repo, no este directorio. El módulo
# del servicio depende de pkg/observabilidad por ruta relativa, así que las dos
# carpetas tienen que entrar en el contexto. El go.work no se copia a propósito:
# sin él la imagen no arrastra el módulo del gateway.
#
#   docker build -f services/proveedores-service/Dockerfile -t arq-soft/proveedores-service .

# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src

# Primero solo los manifiestos: mientras no cambien, la capa de dependencias
# descargadas se reusa entre builds.
COPY pkg/observabilidad/go.mod pkg/observabilidad/go.sum ./pkg/observabilidad/
COPY services/proveedores-service/go.mod services/proveedores-service/go.sum ./services/proveedores-service/
WORKDIR /src/services/proveedores-service
RUN go mod download

WORKDIR /src
COPY pkg/observabilidad/ ./pkg/observabilidad/
COPY services/proveedores-service/ ./services/proveedores-service/

WORKDIR /src/services/proveedores-service
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/proveedores-service ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sonda ./cmd/sonda

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/proveedores-service /proveedores-service
# La imagen distroless no trae shell ni cliente HTTP: el healthcheck del
# contenedor es este binario, que pregunta por /readyz y sale 0 o 1.
COPY --from=build /out/sonda /sonda

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/proveedores-service"]
