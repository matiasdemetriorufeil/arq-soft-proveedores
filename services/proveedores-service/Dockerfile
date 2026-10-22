# syntax=docker/dockerfile:1

# --- build ---
FROM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/proveedores-service ./cmd/server

# --- runtime ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/proveedores-service /proveedores-service

ENV PORT=8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/proveedores-service"]
