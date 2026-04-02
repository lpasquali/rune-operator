package telemetry

import (
	"context"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var newOTLPTraceExporter = otlptracegrpc.New
var newResource = resource.New
var shutdownTracerProvider = func(tp *sdktrace.TracerProvider, ctx context.Context) error {
	return tp.Shutdown(ctx)
}

func SetupOTel(serviceName string) func() {
	ctx := context.Background()
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func() {}
	}

	exporter, err := newOTLPTraceExporter(ctx)
	if err != nil {
		log.Printf("otel exporter setup failed: %v", err)
		return func() {}
	}

	res, err := newResource(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		log.Printf("otel resource setup failed, using empty resource: %v", err)
		res = resource.Empty()
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracerProvider(tp, shutdownCtx); err != nil {
			log.Printf("otel shutdown failed: %v", err)
		}
	}
}
