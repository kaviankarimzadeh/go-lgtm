// order-svc is the order management microservice.
//
// Responsibilities:
//   - Accept new orders and validate them against customer-svc and product-svc.
//   - Persist order state (confirmed / failed) in an in-memory store.
//
// Cross-service calls on POST /orders:
//  1. GET customer-svc/customers/{id}  — verify customer exists
//  2. GET product-svc/products/{id}    — verify product exists and has stock
//
// These calls produce correlated log lines across three services, making them
// visible as related entries in Grafana/Loki. In Phase 2 (traces) the same
// calls will produce linked spans in Tempo.
//
// Configuration (environment variables):
//
//	LISTEN_ADDR      TCP address to bind           (default ":8081")
//	OTLP_ENDPOINT    Alloy OTLP/HTTP endpoint      (default "http://localhost:4318")
//	SERVICE_NAME     OTel service name             (default "order-svc")
//	SERVICE_VERSION  Semantic version string        (default "0.1.0")
//	ENVIRONMENT      Deployment environment         (default "local")
//	CUSTOMER_SVC_URL Base URL of customer-svc      (default "http://localhost:8083")
//	PRODUCT_SVC_URL  Base URL of product-svc       (default "http://localhost:8082")
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
	listenAddr := getEnv("LISTEN_ADDR", ":8081")
	serviceName := getEnv("SERVICE_NAME", "order-svc")
	serviceVersion := getEnv("SERVICE_VERSION", "0.1.0")
	environment := getEnv("ENVIRONMENT", "local")
	customerSvcURL := getEnv("CUSTOMER_SVC_URL", "http://localhost:8083")
	productSvcURL := getEnv("PRODUCT_SVC_URL", "http://localhost:8082")

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

	log.Info("starting order-svc",
		zap.String("addr", listenAddr),
		zap.String("customer_svc_url", customerSvcURL),
		zap.String("product_svc_url", productSvcURL),
	)

	orderStore := store.NewOrderStore()
	h := handler.New(log)
	oh := handler.NewOrderHandler(log, orderStore, customerSvcURL, productSvcURL)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.Health)
	mux.HandleFunc("POST /orders", oh.CreateOrder)
	mux.HandleFunc("GET /orders", oh.ListOrders)
	mux.HandleFunc("GET /orders/{id}", oh.GetOrder)
	mux.HandleFunc("PUT /orders/{id}", oh.UpdateOrder)
	mux.HandleFunc("DELETE /orders/{id}", oh.DeleteOrder)

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
	log.Info("order-svc stopped")
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
