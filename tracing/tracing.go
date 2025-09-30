/*
Copyright 2020 Kohl's Department Stores, Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
	http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package tracing provides OpenTelemetry tracing initialization and configuration
package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerProvider holds the OpenTelemetry tracer provider
var TracerProvider *sdktrace.TracerProvider

// InitTracing initializes the OpenTelemetry tracing with the specified exporter
func InitTracing(serviceName, exporterType, endpoint string, logger *slog.Logger) error {
	var err error
	var exporter sdktrace.SpanExporter

	switch exporterType {
	case "otlp", "otlp-grpc":
		exporter, err = initOTLPGRPCExporter(context.Background(), endpoint)
		if err != nil {
			return fmt.Errorf("failed to create OTLP gRPC exporter: %w", err)
		}
	case "otlp-http":
		exporter, err = initOTLPHTTPExporter(context.Background(), endpoint)
		if err != nil {
			return fmt.Errorf("failed to create OTLP HTTP exporter: %w", err)
		}
	case "jaeger", "zipkin": // For backward compatibility, these can be handled via OTLP
		exporter, err = initOTLPHTTPExporter(context.Background(), endpoint)
		if err != nil {
			return fmt.Errorf("failed to create OTLP HTTP exporter for %s: %w", exporterType, err)
		}
	case "stdout", "console":
		exporter, err = initConsoleExporter()
		if err != nil {
			return fmt.Errorf("failed to create console exporter: %w", err)
		}
	default:
		return fmt.Errorf("unsupported exporter type: %s", exporterType)
	}

	// Create resource with service name and other attributes
	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(os.Getenv("SERVICE_VERSION")),
	)

	// Create the tracer provider
	TracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Set the global trace provider
	otel.SetTracerProvider(TracerProvider)

	// Set the global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	logger.Info("OpenTelemetry tracing initialized",
		slog.String("service", serviceName),
		slog.String("exporter", exporterType),
		slog.String("endpoint", endpoint))

	return nil
}

// initOTLPGRPCExporter creates an OTLP gRPC exporter
func initOTLPGRPCExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{}
	if endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint))
	} else {
		// Use default endpoint if not provided
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	client := otlptracegrpc.NewClient(opts...)
	return otlptrace.New(ctx, client)
}

// initOTLPHTTPExporter creates an OTLP HTTP exporter
func initOTLPHTTPExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	opts := []otlptracehttp.Option{}
	if endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
	} else {
		// Use default endpoint if not provided
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	client := otlptracehttp.NewClient(opts...)
	return otlptrace.New(ctx, client)
}

// initConsoleExporter creates a console exporter for development
func initConsoleExporter() (sdktrace.SpanExporter, error) {
	return stdouttrace.New(stdouttrace.WithPrettyPrint())
}

// ShutdownTracing shuts down the tracer provider
func ShutdownTracing(ctx context.Context) error {
	if TracerProvider != nil {
		return TracerProvider.Shutdown(ctx)
	}
	return nil
}

// GetTracer returns a tracer with the given name
func GetTracer(name string) trace.Tracer {
	return otel.Tracer(name)
}
