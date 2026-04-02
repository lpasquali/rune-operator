package telemetry

import (
	"context"
	"errors"
	"os"
	"testing"

	otlptrace "go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestSetupOTelNoEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}

func TestSetupOTelWithEndpoint(t *testing.T) {
	old := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	t.Cleanup(func() { _ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", old) })
	_ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")

	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}

func TestSetupOTelWithInvalidEndpoint(t *testing.T) {
	old := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	t.Cleanup(func() { _ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", old) })
	_ = os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://[::1")

	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}

func TestSetupOTelExporterCreationError(t *testing.T) {
	old := newOTLPTraceExporter
	t.Cleanup(func() { newOTLPTraceExporter = old })

	newOTLPTraceExporter = func(context.Context, ...otlptracegrpc.Option) (*otlptrace.Exporter, error) {
		return nil, errors.New("boom")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}

func TestSetupOTelShutdownErrorBranch(t *testing.T) {
	oldShutdown := shutdownTracerProvider
	t.Cleanup(func() { shutdownTracerProvider = oldShutdown })

	shutdownTracerProvider = func(_ *sdktrace.TracerProvider, _ context.Context) error {
		return errors.New("shutdown-failed")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}

func TestSetupOTelResourceCreationErrorBranch(t *testing.T) {
	oldResource := newResource
	oldShutdown := shutdownTracerProvider
	t.Cleanup(func() {
		newResource = oldResource
		shutdownTracerProvider = oldShutdown
	})

	newResource = func(context.Context, ...resource.Option) (*resource.Resource, error) {
		return nil, errors.New("resource-failed")
	}
	shutdownTracerProvider = func(_ *sdktrace.TracerProvider, _ context.Context) error {
		return nil
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4317")
	shutdown := SetupOTel("test-service")
	if shutdown == nil {
		t.Fatalf("expected shutdown function")
	}
	shutdown()
}
