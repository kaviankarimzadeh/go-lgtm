// product-svc is the product catalogue microservice.
//
// Responsibilities:
//   - CRUD operations on Product records (in-memory store).
//   - Exposes stock levels queried by order-svc during order validation.
//
// Configuration (environment variables):
//
//	LISTEN_ADDR     TCP address to bind (default ":8082")
//	OTLP_ENDPOINT   Alloy OTLP/HTTP endpoint (default "http://localhost:4318")
//	SERVICE_NAME    OTel service name shown in Loki (default "product-svc")
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

	"github.com/kavian/go-lgtm/internal/handler"
	"github.com/kavian/go-lgtm/internal/logger"
	"github.com/kavian/go-lgtm/internal/store"
)

func main() {
	otlpEndpoint := getEnv("OTLP_ENDPOINT", "http://localhost:4318")
	listenAddr := getEnv("LISTEN_ADDR", ":8082")
	serviceName := getEnv("SERVICE_NAME", "product-svc")
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

	log.Info("starting product-svc", zap.String("addr", listenAddr))

	productStore := store.NewProductStore()
	h := handler.New(log)
	ph := handler.NewProductHandler(log, productStore)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("POST /products", ph.CreateProduct)
	mux.HandleFunc("GET /products", ph.ListProducts)
	mux.HandleFunc("GET /products/{id}", ph.GetProduct)
	mux.HandleFunc("PUT /products/{id}", ph.UpdateProduct)
	mux.HandleFunc("DELETE /products/{id}", ph.DeleteProduct)

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
	log.Info("product-svc stopped")
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
