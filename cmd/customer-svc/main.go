// customer-svc is the customer profile microservice.
//
// Responsibilities:
//   - CRUD operations on Customer records (in-memory store).
//   - Validates customer existence for order-svc.
//
// Configuration (environment variables):
//
//	LISTEN_ADDR     TCP address to bind (default ":8083")
//	OTLP_ENDPOINT   Alloy OTLP/HTTP endpoint (default "http://localhost:4318")
//	SERVICE_NAME    OTel service name shown in Loki (default "customer-svc")
//	SERVICE_VERSION Semantic version string     (default "0.1.0")
//	ENVIRONMENT     Deployment environment      (default "local")
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/kaviankarimzadeh/go-lgtm/internal/handler"
	"github.com/kaviankarimzadeh/go-lgtm/internal/logger"
	"github.com/kaviankarimzadeh/go-lgtm/internal/store"
)

func main() {
	otlpEndpoint := getEnv("OTLP_ENDPOINT", "http://localhost:4318")
	listenAddr := getEnv("LISTEN_ADDR", ":8083")
	serviceName := getEnv("SERVICE_NAME", "customer-svc")
	serviceVersion := getEnv("SERVICE_VERSION", "0.1.0")
	environment := getEnv("ENVIRONMENT", "local")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log, shutdownLogger, err := logger.New(ctx, logger.Config{
		ServiceName:    serviceName,
		ServiceVersion: serviceVersion,
		Environment:    environment,
		OTLPEndpoint:   otlpEndpoint,
	})
	if err != nil {
		_, _ = os.Stderr.WriteString("failed to initialise logger: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownLogger(shutdownCtx); err != nil {
			log.Error("failed to shut down logger", zap.Error(err))
		}
	}()

	log.Info("starting customer-svc", zap.String("addr", listenAddr))

	customerStore := store.NewCustomerStore()
	h := handler.New(log)
	ch := handler.NewCustomerHandler(log, customerStore)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("POST /customers", ch.CreateCustomer)
	mux.HandleFunc("GET /customers", ch.ListCustomers)
	mux.HandleFunc("GET /customers/{id}", ch.GetCustomer)
	mux.HandleFunc("PUT /customers/{id}", ch.UpdateCustomer)
	mux.HandleFunc("DELETE /customers/{id}", ch.DeleteCustomer)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           h.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-serverErr:
		log.Error("server error", zap.Error(err))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", zap.Error(err))
	}
	log.Info("customer-svc stopped")
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
