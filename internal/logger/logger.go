// Package logger provides a factory for constructing a production-ready Zap logger
// that simultaneously writes structured logs to stdout (for container log collectors)
// and ships them to an OpenTelemetry-compatible backend (e.g. Grafana Loki via Alloy).
//
// Design decisions:
//   - We use zapcore.NewTee to fan out to two cores: a console encoder for human-readable
//     local output, and the otelzap bridge core that converts every Zap record into an
//     OTel LogRecord and forwards it through the OTLP/HTTP exporter.
//   - Resource attributes (service.name, service.version, deployment.environment) are
//     attached at the SDK level so every exported log line carries consistent metadata
//     without the application code having to repeat them.
//   - The returned shutdown function MUST be called on process exit to flush the
//     BatchProcessor's in-memory buffer, preventing log loss during graceful shutdown.
package logger

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds the configuration required to build the logger.
// It is intentionally kept small: everything else (encoder format, log level)
// is a hardcoded best-practice default that can be expanded later.
type Config struct {
	// ServiceName is the logical name of this service, used as the OTel
	// resource attribute "service.name" and attached to every exported log record.
	ServiceName string

	// ServiceVersion follows semver and maps to "service.version".
	ServiceVersion string

	// Environment maps to "deployment.environment" (e.g. "local", "staging", "production").
	Environment string

	// OTLPEndpoint is the base URL of the OTLP HTTP endpoint to push logs to,
	// e.g. "http://alloy:4318". The SDK appends the standard "/v1/logs" path.
	OTLPEndpoint string
}

// New constructs a *zap.Logger that fans out log records to:
//  1. A JSON encoder writing to stdout (ideal for Docker / k8s log collectors).
//  2. An OTel log core that exports records via OTLP/HTTP to the configured endpoint.
//
// It returns the logger, a shutdown function that must be deferred by the caller, and
// any error that occurred during setup. On error the returned logger and shutdown are nil.
func New(ctx context.Context, cfg Config) (*zap.Logger, func(context.Context) error, error) {
	// --- Step 1: Build the OTel Resource ---
	// A Resource describes the entity producing telemetry. These attributes appear
	// on every log record exported from this process and allow Grafana to filter/group
	// logs by service name, version, or environment without extra LogQL gymnastics.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build otel resource: %w", err)
	}

	// --- Step 2: Create the OTLP/HTTP Log Exporter ---
	// The exporter serialises OTel LogRecords as protobuf and POSTs them to
	// <host:port>/v1/logs. We use HTTP (not gRPC) because it traverses
	// firewalls/proxies more easily and requires no extra proto codec dependency.
	//
	// otlploghttp.WithEndpoint expects only "host:port" — NOT a full URL.
	// The SDK constructs the full URL internally:
	//   http://<endpoint>/v1/logs   (when WithInsecure is set)
	//   https://<endpoint>/v1/logs  (default, TLS)
	//
	// We accept a full URL in cfg.OTLPEndpoint (e.g. "http://alloy:4318") for
	// operator convenience and strip the scheme here before handing it to the SDK.
	// Passing a full URL with scheme causes the SDK to double-encode "//" as "%2F%2F".
	otlpHost := strings.TrimPrefix(cfg.OTLPEndpoint, "https://")
	otlpHost = strings.TrimPrefix(otlpHost, "http://")

	exporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpoint(otlpHost),
		otlploghttp.WithInsecure(), // no TLS needed on a Docker-internal network
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create otlp log exporter: %w", err)
	}

	// --- Step 3: Build the LoggerProvider ---
	// The LoggerProvider manages the exporter lifecycle and owns the BatchProcessor.
	// BatchProcessor buffers records in memory and flushes them in bulk, which
	// reduces OTLP round-trips compared to a SimpleProcessor (one request per record).
	provider := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
	)

	// --- Step 4: Build the Console (stdout) Core ---
	// We use JSON encoding so that Docker/Kubernetes log drivers and Alloy's
	// loki.source.docker can parse log lines without additional stage processing.
	// ISO8601 timestamps are used over epoch seconds for human readability.
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder    // e.g. "2026-02-18T10:00:00.000Z"
	encoderCfg.EncodeLevel = zapcore.LowercaseLevelEncoder // e.g. "info", "error"

	consoleCore := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout), // write directly to container stdout
		zapcore.DebugLevel,         // capture all levels; filter at Grafana/Loki query time
	)

	// --- Step 5: Build the OTel Bridge Core ---
	// otelzap.NewCore wraps the LoggerProvider and converts each Zap log entry
	// (level, message, fields) into an OTel LogRecord, which is then handed off
	// to the BatchProcessor for async export via OTLP.
	otelCore := otelzap.NewCore(
		cfg.ServiceName,
		otelzap.WithLoggerProvider(provider),
	)

	// --- Step 6: Tee the two cores together ---
	// zapcore.NewTee ensures every log() call fans out to both destinations.
	// This way local Docker logs AND the Loki pipeline receive the same records.
	logger := zap.New(
		zapcore.NewTee(consoleCore, otelCore),
		zap.AddCaller(),                   // annotate log lines with file:line for debugging
		zap.AddStacktrace(zap.ErrorLevel), // stack traces only for Error+ to reduce noise
	)

	// Return provider.Shutdown as the cleanup function so callers don't need to
	// import the SDK themselves. This keeps the shutdown contract simple:
	// defer shutdown(ctx)
	return logger, provider.Shutdown, nil
}
