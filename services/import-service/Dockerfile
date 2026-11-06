# syntax=docker/dockerfile:1

# Ojo: el contexto de build es la raíz del repo, no este directorio. El módulo
# depende de pkg/observabilidad y pkg/mensajeria por ruta relativa, así que las
# tres carpetas tienen que entrar en el contexto. El go.work no se copia a
# propósito: sin él la imagen no arrastra los otros servicios.
#
#   docker build -f services/import-service/Dockerfile -t arq-soft/import-service .
#
# La misma imagen trae el mock de FAV (/fav-mock): el compose lo usa con otro
# entrypoint mientras no tengamos la URL desplegada de FAV.

# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY pkg/observabilidad/go.mod pkg/observabilidad/go.sum ./pkg/observabilidad/
COPY pkg/mensajeria/go.mod pkg/mensajeria/go.sum ./pkg/mensajeria/
COPY services/import-service/go.mod services/import-service/go.sum ./services/import-service/
WORKDIR /src/services/import-service
RUN go mod download

WORKDIR /src
COPY pkg/observabilidad/ ./pkg/observabilidad/
COPY pkg/mensajeria/ ./pkg/mensajeria/
COPY services/import-service/ ./services/import-service/

WORKDIR /src/services/import-service
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/import-service ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sonda ./cmd/sonda \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fav-mock ./cmd/fav-mock

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/import-service /import-service
COPY --from=build /out/sonda /sonda
COPY --from=build /out/fav-mock /fav-mock

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/import-service"]
