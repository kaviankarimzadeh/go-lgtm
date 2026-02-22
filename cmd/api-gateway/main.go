// api-gateway is the single entry point for all external traffic.
//
// It serves two roles:
//  1. Web UI: serves the HTMX dashboard at GET / using Go html/template.
//  2. API proxy: forwards /customers, /products, /orders requests to the
//     respective downstream microservices via HTTP client calls.
//
// Why a proxy instead of a true reverse proxy (httputil.ReverseProxy)?
// For a learning project, a thin proxy using http.Client gives full control
// over what gets logged and makes the cross-service call pattern explicit.
// When traces are added in Phase 2, this is where traceparent headers will be
// injected into every outgoing request.
//
// Configuration (environment variables):
//
//	LISTEN_ADDR      TCP address to bind           (default ":8080")
//	OTLP_ENDPOINT    Alloy OTLP/HTTP endpoint      (default "http://localhost:4318")
//	SERVICE_NAME     OTel service name             (default "api-gateway")
//	SERVICE_VERSION  Semantic version string        (default "0.1.0")
//	ENVIRONMENT      Deployment environment         (default "local")
//	CUSTOMER_SVC_URL Base URL of customer-svc      (default "http://localhost:8083")
//	PRODUCT_SVC_URL  Base URL of product-svc       (default "http://localhost:8082")
//	ORDER_SVC_URL    Base URL of order-svc         (default "http://localhost:8081")
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/kaviankarimzadeh/go-lgtm/internal/handler"
	"github.com/kaviankarimzadeh/go-lgtm/internal/logger"
	"github.com/kaviankarimzadeh/go-lgtm/internal/store"
	"github.com/kaviankarimzadeh/go-lgtm/internal/ui"
)

func main() {
	otlpEndpoint := getEnv("OTLP_ENDPOINT", "http://localhost:4318")
	listenAddr := getEnv("LISTEN_ADDR", ":8080")
	serviceName := getEnv("SERVICE_NAME", "api-gateway")
	serviceVersion := getEnv("SERVICE_VERSION", "0.1.0")
	environment := getEnv("ENVIRONMENT", "local")
	customerSvcURL := getEnv("CUSTOMER_SVC_URL", "http://localhost:8083")
	productSvcURL := getEnv("PRODUCT_SVC_URL", "http://localhost:8082")
	orderSvcURL := getEnv("ORDER_SVC_URL", "http://localhost:8081")

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

	log.Info("starting api-gateway",
		zap.String("addr", listenAddr),
		zap.String("customer_svc", customerSvcURL),
		zap.String("product_svc", productSvcURL),
		zap.String("order_svc", orderSvcURL),
	)

	gw := &gateway{
		log:            log,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		customerSvcURL: customerSvcURL,
		productSvcURL:  productSvcURL,
		orderSvcURL:    orderSvcURL,
	}

	h := handler.New(log)
	mux := http.NewServeMux()

	// Static assets (CSS) embedded in binary.
	// Registered without a method prefix so it is method-agnostic — this avoids
	// a Go 1.22 ServeMux conflict with method-qualified patterns on child paths.
	mux.Handle("/static/", ui.StaticHandler())

	// Health check — used by Docker health check binary.
	mux.HandleFunc("GET /health", h.Health)

	// ── UI routes (return HTML for HTMX) ──────────────────────────────────
	// The root route must also be registered without a method prefix because
	// Go 1.22 ServeMux rejects a method-qualified "GET /" alongside the
	// method-agnostic "/static/" — it considers them conflicting patterns.
	// The index handler itself rejects non-GET methods explicitly.
	mux.HandleFunc("/", gw.index)
	mux.HandleFunc("GET /ui/stats", gw.uiStats)
	mux.HandleFunc("GET /ui/customers", gw.uiCustomers)
	mux.HandleFunc("GET /ui/products", gw.uiProducts)
	mux.HandleFunc("GET /ui/orders", gw.uiOrders)

	// POST /ui/* — receive HTML form submissions, forward as JSON to the
	// downstream service, then return the refreshed HTML partial so HTMX
	// can swap it directly into the tab without a full page reload.
	mux.HandleFunc("POST /ui/customers", gw.uiCreateCustomer)
	mux.HandleFunc("POST /ui/products", gw.uiCreateProduct)
	mux.HandleFunc("POST /ui/orders", gw.uiCreateOrder)

	// Trigger Error: places an order with an invalid customer ID.
	// Intentionally produces Warn logs in order-svc and customer-svc.
	mux.HandleFunc("POST /ui/trigger-error", gw.triggerError)

	// ── API proxy routes (pass-through to downstream services) ────────────
	// These routes allow the browser's Simulate Load JS to call the services
	// directly through the gateway using the standard REST paths.
	mux.HandleFunc("/customers", gw.proxyCustomers)
	mux.HandleFunc("/customers/{id}", gw.proxyCustomerByID)
	mux.HandleFunc("/products", gw.proxyProducts)
	mux.HandleFunc("/products/{id}", gw.proxyProductByID)
	mux.HandleFunc("/orders", gw.proxyOrders)
	mux.HandleFunc("/orders/{id}", gw.proxyOrderByID)

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
	log.Info("api-gateway stopped")
}

// gateway holds the shared dependencies for all UI and proxy handlers.
type gateway struct {
	log            *zap.Logger
	httpClient     *http.Client
	customerSvcURL string
	productSvcURL  string
	orderSvcURL    string
}

// ── UI handlers ────────────────────────────────────────────────────────────

// index renders the full dashboard shell (index.html template).
// Registered without a method prefix to avoid a Go 1.22 ServeMux conflict
// with "/static/" — so we enforce GET manually here.
func (g *gateway) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats := g.fetchStats(r.Context())
	if err := ui.Render(w, "index.html", stats); err != nil {
		g.log.Error("render index", zap.Error(err))
	}
}

// uiStats renders the stats bar fragment (polled every 5 s by HTMX).
func (g *gateway) uiStats(w http.ResponseWriter, r *http.Request) {
	stats := g.fetchStats(r.Context())
	if err := ui.Render(w, "stats", stats); err != nil {
		g.log.Error("render stats", zap.Error(err))
	}
}

// uiCustomers renders the customers tab partial.
func (g *gateway) uiCustomers(w http.ResponseWriter, r *http.Request) {
	customers := getJSON[[]store.Customer](r.Context(), g.httpClient, g.customerSvcURL+"/customers")
	if err := ui.Render(w, "customers", map[string]any{"Customers": customers}); err != nil {
		g.log.Error("render customers", zap.Error(err))
	}
}

// uiProducts renders the products tab partial.
func (g *gateway) uiProducts(w http.ResponseWriter, r *http.Request) {
	products := getJSON[[]store.Product](r.Context(), g.httpClient, g.productSvcURL+"/products")
	if err := ui.Render(w, "products", map[string]any{"Products": products}); err != nil {
		g.log.Error("render products", zap.Error(err))
	}
}

// uiOrders renders the orders tab partial. It also fetches customers and
// products to populate the "Place Order" form dropdowns.
func (g *gateway) uiOrders(w http.ResponseWriter, r *http.Request) {
	orders := getJSON[[]store.Order](r.Context(), g.httpClient, g.orderSvcURL+"/orders")
	customers := getJSON[[]store.Customer](r.Context(), g.httpClient, g.customerSvcURL+"/customers")
	products := getJSON[[]store.Product](r.Context(), g.httpClient, g.productSvcURL+"/products")
	if err := ui.Render(w, "orders", map[string]any{
		"Orders":    orders,
		"Customers": customers,
		"Products":  products,
	}); err != nil {
		g.log.Error("render orders", zap.Error(err))
	}
}

// uiCreateCustomer handles POST /ui/customers.
// It decodes the HTML form (application/x-www-form-urlencoded submitted by HTMX),
// forwards the data as JSON to customer-svc, then returns the refreshed customers
// partial so HTMX can swap it directly into the tab.
func (g *gateway) uiCreateCustomer(w http.ResponseWriter, r *http.Request) {
	log := g.log.With(zap.String("ui_action", "create_customer"))

	if err := r.ParseForm(); err != nil {
		log.Warn("failed to parse form", zap.Error(err))
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	payload := map[string]any{
		"name":    r.FormValue("name"),
		"email":   r.FormValue("email"),
		"country": r.FormValue("country"),
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		g.customerSvcURL+"/customers", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Error("customer-svc call failed", zap.Error(err))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Warn("customer-svc rejected create", zap.Int("status", resp.StatusCode))
	}

	// Always re-render the full customers partial so the table updates.
	customers := getJSON[[]store.Customer](r.Context(), g.httpClient, g.customerSvcURL+"/customers")
	if err := ui.Render(w, "customers", map[string]any{"Customers": customers}); err != nil {
		log.Error("render customers", zap.Error(err))
	}
}

// uiCreateProduct handles POST /ui/products.
func (g *gateway) uiCreateProduct(w http.ResponseWriter, r *http.Request) {
	log := g.log.With(zap.String("ui_action", "create_product"))

	if err := r.ParseForm(); err != nil {
		log.Warn("failed to parse form", zap.Error(err))
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	price, _ := strconv.Atoi(r.FormValue("price"))
	stock, _ := strconv.Atoi(r.FormValue("stock"))

	payload := map[string]any{
		"name":     r.FormValue("name"),
		"category": r.FormValue("category"),
		"price":    price,
		"stock":    stock,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		g.productSvcURL+"/products", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Error("product-svc call failed", zap.Error(err))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Warn("product-svc rejected create", zap.Int("status", resp.StatusCode))
	}

	products := getJSON[[]store.Product](r.Context(), g.httpClient, g.productSvcURL+"/products")
	if err := ui.Render(w, "products", map[string]any{"Products": products}); err != nil {
		log.Error("render products", zap.Error(err))
	}
}

// uiCreateOrder handles POST /ui/orders.
func (g *gateway) uiCreateOrder(w http.ResponseWriter, r *http.Request) {
	log := g.log.With(zap.String("ui_action", "create_order"))

	if err := r.ParseForm(); err != nil {
		log.Warn("failed to parse form", zap.Error(err))
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	quantity, _ := strconv.Atoi(r.FormValue("quantity"))

	payload := map[string]any{
		"customer_id": r.FormValue("customer_id"),
		"product_id":  r.FormValue("product_id"),
		"quantity":    quantity,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		g.orderSvcURL+"/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Error("order-svc call failed", zap.Error(err))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Warn("order-svc rejected create", zap.Int("status", resp.StatusCode))
	}

	orders := getJSON[[]store.Order](r.Context(), g.httpClient, g.orderSvcURL+"/orders")
	customers := getJSON[[]store.Customer](r.Context(), g.httpClient, g.customerSvcURL+"/customers")
	products := getJSON[[]store.Product](r.Context(), g.httpClient, g.productSvcURL+"/products")
	if err := ui.Render(w, "orders", map[string]any{
		"Orders":    orders,
		"Customers": customers,
		"Products":  products,
	}); err != nil {
		log.Error("render orders", zap.Error(err))
	}
}

// triggerError places an order with a deliberately invalid customer ID.
// This produces a Warn log in order-svc ("customer not found") and in
// customer-svc ("customer not found"), demonstrating cross-service error
// correlation in Loki.
func (g *gateway) triggerError(w http.ResponseWriter, r *http.Request) {
	log := g.log.With(zap.String("action", "trigger_error"))

	// Fetch a real product so the product validation passes.
	products := getJSON[[]store.Product](r.Context(), g.httpClient, g.productSvcURL+"/products")
	productID := "invalid-product-id"
	if len(products) > 0 {
		productID = products[0].ID
	}

	payload := map[string]any{
		"customer_id": "intentional-invalid-customer-id",
		"product_id":  productID,
		"quantity":    1,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost,
		g.orderSvcURL+"/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		log.Error("trigger-error request failed", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	log.Info("trigger-error order placed",
		zap.Int("upstream_status", resp.StatusCode),
	)
	w.WriteHeader(http.StatusOK)
}

// ── API proxy handlers ─────────────────────────────────────────────────────
// Each proxy handler forwards the request body and method to the appropriate
// downstream service and streams the response back to the client.
// Having all traffic pass through the gateway means a single Loki label
// ({service_name="api-gateway"}) gives you an aggregate view of all calls.

func (g *gateway) proxyCustomers(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.customerSvcURL+"/customers")
}

func (g *gateway) proxyCustomerByID(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.customerSvcURL+"/customers/"+r.PathValue("id"))
}

func (g *gateway) proxyProducts(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.productSvcURL+"/products")
}

func (g *gateway) proxyProductByID(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.productSvcURL+"/products/"+r.PathValue("id"))
}

func (g *gateway) proxyOrders(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.orderSvcURL+"/orders")
}

func (g *gateway) proxyOrderByID(w http.ResponseWriter, r *http.Request) {
	g.proxy(w, r, g.orderSvcURL+"/orders/"+r.PathValue("id"))
}

// proxy forwards r to targetURL, copying the method, body, and Content-Type,
// then writes the upstream status code and body back to w.
// It logs the upstream call as a structured Zap field so the gateway's Loki
// stream shows which service was called, the method, and the result status.
func (g *gateway) proxy(w http.ResponseWriter, r *http.Request, targetURL string) {
	start := time.Now()

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		g.log.Error("proxy: build request", zap.String("target", targetURL), zap.Error(err))
		http.Error(w, "gateway error", http.StatusBadGateway)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		g.log.Error("proxy: upstream call failed",
			zap.String("target", targetURL),
			zap.String("method", r.Method),
			zap.Error(err),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	g.log.Info("proxy: upstream call",
		zap.String("target", targetURL),
		zap.String("method", r.Method),
		zap.Int("status", resp.StatusCode),
		zap.Duration("latency", time.Since(start)),
	)

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ── Helper types and fetch utilities ──────────────────────────────────────

// statsData holds the counts shown in the dashboard stat cards.
type statsData struct {
	Customers int
	Products  int
	Orders    int
}

// fetchStats fetches live counts from all three services.
// Errors are swallowed and result in a 0 count (graceful degradation).
func (g *gateway) fetchStats(ctx context.Context) statsData {
	customers := getJSON[[]store.Customer](ctx, g.httpClient, g.customerSvcURL+"/customers")
	products := getJSON[[]store.Product](ctx, g.httpClient, g.productSvcURL+"/products")
	orders := getJSON[[]store.Order](ctx, g.httpClient, g.orderSvcURL+"/orders")
	return statsData{
		Customers: len(customers),
		Products:  len(products),
		Orders:    len(orders),
	}
}

// fetchJSON is a convenience wrapper that also passes g.httpClient.
func (g *gateway) fetchJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// getJSON is a package-level generic helper that calls url and decodes JSON into T.
// Returns the zero value of T on any error so callers never need to nil-check.
// Go does not allow generic methods, so this is a free function.
func getJSON[T any](ctx context.Context, client *http.Client, url string) T {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return zero
	}
	resp, err := client.Do(req)
	if err != nil {
		return zero
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero
	}
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return zero
	}
	return v
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// fmtPrice formats an integer price in cents as a USD dollar string.
// Used as a template function passed to html/template.
func fmtPrice(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}
