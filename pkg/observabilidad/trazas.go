package observabilidad

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// IniciarTrazas deja OpenTelemetry listo para el servicio: propagador W3C
// (`traceparent`) global, exportador OTLP sobre HTTP hacia el colector y
// proveedor de trazas global. Devuelve el apagado, que hay que llamar al
// terminar para vaciar los spans que quedaron en el buffer.
//
// Sin endpoint OTLP configurado no hay colector al que exportar: queda puesto
// el propagador —así el `traceparent` sigue viajando— y el apagado es un no-op.
// Es lo que hace que los binarios arranquen y los tests corran sin Jaeger.
func IniciarTrazas(ctx context.Context, servicio string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Un fallo del exportador (Jaeger caído, por ejemplo) se registra y nada
	// más: no puede tirar al servicio ni frenar una request.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Default().Warn("opentelemetry", "error", err.Error())
	}))

	if endpointOTLP() == "" {
		slog.Default().Info("trazas deshabilitadas: no hay OTEL_EXPORTER_OTLP_ENDPOINT")
		return apagadoVacio, nil
	}

	// otlptracehttp.New no abre la conexión acá: la primera exportación es la
	// que la intenta, en segundo plano y con reintentos.
	exportador, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("exportador OTLP: %w", err)
	}

	recurso, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(servicio)),
	)
	if err != nil {
		return nil, fmt.Errorf("recurso de la traza: %w", err)
	}

	proveedor := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exportador),
		sdktrace.WithResource(recurso),
	)
	otel.SetTracerProvider(proveedor)

	slog.Default().Info("trazas habilitadas", "endpoint", endpointOTLP())
	return proveedor.Shutdown, nil
}

func apagadoVacio(context.Context) error { return nil }

// endpointOTLP es el endpoint al que exportar. El exportador lee estas dos
// variables por su cuenta; acá se consultan solo para saber si hay colector.
func endpointOTLP() string {
	if e := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); e != "" {
		return e
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
}
