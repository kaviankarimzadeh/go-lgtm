// order.go implements the HTTP handlers for the /orders CRUD endpoints.
//
// order-svc is the most interesting service for observability because creating
// an order requires cross-service validation:
//  1. Call customer-svc GET /customers/{id} — verify the customer exists.
//  2. Call product-svc  GET /products/{id}  — verify the product exists and has stock.
//
// This cross-service fan-out produces correlated log lines across three services,
// which is exactly what you want to explore in Loki and (in Phase 2) visualise
// as a distributed trace in Tempo.
//
// Intentional error paths (triggerable from the UI):
//   - Non-existent customer_id → order status "failed", Warn log in order-svc
//     AND Warn log in customer-svc ("customer not found").
//   - Out-of-stock product    → order status "failed", Warn log in order-svc.
//   - Bad product_id          → same as out-of-stock path from order-svc's perspective.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/kavian/go-lgtm/internal/store"
)

// OrderHandler holds the dependencies needed by the order CRUD handlers.
// The upstream URLs are injected so they can be pointed at real service
// addresses in Docker Compose (http://customer-svc:8083) or mocked in tests.
type OrderHandler struct {
	log         *zap.Logger
	store       *store.OrderStore
	customerURL string // base URL of customer-svc, e.g. "http://customer-svc:8083"
	productURL  string // base URL of product-svc,  e.g. "http://product-svc:8082"
	httpClient  *http.Client
}

// NewOrderHandler creates an OrderHandler with the provided dependencies.
func NewOrderHandler(log *zap.Logger, s *store.OrderStore, customerURL, productURL string) *OrderHandler {
	return &OrderHandler{
		log:         log,
		store:       s,
		customerURL: strings.TrimRight(customerURL, "/"),
		productURL:  strings.TrimRight(productURL, "/"),
		httpClient:  &http.Client{Timeout: 5 * time.Second},
	}
}

// CreateOrder handles POST /orders.
// It validates the customer and product via HTTP calls to the respective services,
// then persists the order with status "confirmed" or "failed".
//
// Request body:
//
//	{"customer_id":"<id>","product_id":"<id>","quantity":2}
//
// Response: 201 Created with the new order including computed total and status.
func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())

	var o store.Order
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&o); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	o.CustomerID = strings.TrimSpace(o.CustomerID)
	o.ProductID = strings.TrimSpace(o.ProductID)
	if o.CustomerID == "" || o.ProductID == "" || o.Quantity <= 0 {
		log.Warn("missing or invalid fields",
			zap.Bool("customer_id_missing", o.CustomerID == ""),
			zap.Bool("product_id_missing", o.ProductID == ""),
			zap.Int("quantity", o.Quantity),
		)
		writeError(w, http.StatusBadRequest, "customer_id, product_id, and quantity (>0) are required")
		return
	}

	// --- Validate customer ---
	// A non-existent customer_id is a deliberate error path exposed in the UI
	// ("Trigger Error" button). It produces a Warn log here AND a Warn log in
	// customer-svc, demonstrating cross-service log correlation in Loki.
	customer, err := h.fetchCustomer(r.Context(), o.CustomerID)
	if err != nil {
		log.Warn("order validation failed: customer not found",
			zap.String("customer_id", o.CustomerID),
			zap.String("product_id", o.ProductID),
			zap.Error(err),
		)
		// Store the failed order so it appears in the UI order list with status "failed".
		o.Status = store.OrderStatusFailed
		created, _ := h.store.Create(o)
		writeJSON(w, http.StatusUnprocessableEntity, created)
		return
	}

	// --- Validate product and check stock ---
	product, err := h.fetchProduct(r.Context(), o.ProductID)
	if err != nil {
		log.Warn("order validation failed: product not found",
			zap.String("customer_id", o.CustomerID),
			zap.String("product_id", o.ProductID),
			zap.Error(err),
		)
		o.Status = store.OrderStatusFailed
		created, _ := h.store.Create(o)
		writeJSON(w, http.StatusUnprocessableEntity, created)
		return
	}

	if product.Stock < o.Quantity {
		log.Warn("order validation failed: insufficient stock",
			zap.String("product_id", o.ProductID),
			zap.Int("requested", o.Quantity),
			zap.Int("available", product.Stock),
		)
		o.Status = store.OrderStatusFailed
		created, _ := h.store.Create(o)
		writeJSON(w, http.StatusUnprocessableEntity, created)
		return
	}

	// --- Confirm the order ---
	o.Status = store.OrderStatusConfirmed
	o.Total = product.Price * o.Quantity

	created, err := h.store.Create(o)
	if err != nil {
		log.Error("failed to persist order", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "could not create order")
		return
	}

	// Structured log fields enable cross-service correlation queries in Loki:
	//   {service_name=~"order-svc|product-svc|customer-svc"} | json
	//   | attributes_customer_id="<id>"
	log.Info("order confirmed",
		zap.String("order_id", created.ID),
		zap.String("customer_id", created.CustomerID),
		zap.String("customer_name", customer.Name),
		zap.String("customer_country", customer.Country),
		zap.String("product_id", created.ProductID),
		zap.String("product_name", product.Name),
		zap.String("product_category", product.Category),
		zap.Int("quantity", created.Quantity),
		zap.Int("total", created.Total),
		zap.String("status", string(created.Status)),
	)

	writeJSON(w, http.StatusCreated, created)
}

// ListOrders handles GET /orders.
func (h *OrderHandler) ListOrders(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	orders := h.store.List()
	log.Info("orders listed", zap.Int("count", len(orders)))
	writeJSON(w, http.StatusOK, orders)
}

// GetOrder handles GET /orders/{id}.
func (h *OrderHandler) GetOrder(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	order, ok := h.store.Get(id)
	if !ok {
		log.Warn("order not found", zap.String("order_id", id))
		writeError(w, http.StatusNotFound, "order not found")
		return
	}

	log.Info("order retrieved", zap.String("order_id", id))
	writeJSON(w, http.StatusOK, order)
}

// UpdateOrder handles PUT /orders/{id}.
func (h *OrderHandler) UpdateOrder(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	var o store.Order
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&o); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	updated, ok := h.store.Update(id, o)
	if !ok {
		log.Warn("order not found for update", zap.String("order_id", id))
		writeError(w, http.StatusNotFound, "order not found")
		return
	}

	log.Info("order updated",
		zap.String("order_id", updated.ID),
		zap.String("status", string(updated.Status)),
	)
	writeJSON(w, http.StatusOK, updated)
}

// DeleteOrder handles DELETE /orders/{id}.
func (h *OrderHandler) DeleteOrder(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	if ok := h.store.Delete(id); !ok {
		log.Warn("order not found for deletion", zap.String("order_id", id))
		writeError(w, http.StatusNotFound, "order not found")
		return
	}

	log.Info("order deleted", zap.String("order_id", id))
	w.WriteHeader(http.StatusNoContent)
}

// fetchCustomer calls customer-svc to retrieve a customer by ID.
// Returns an error if the customer does not exist or the call fails.
// This is the cross-service call that produces correlated logs across services.
func (h *OrderHandler) fetchCustomer(ctx context.Context, id string) (store.Customer, error) {
	url := fmt.Sprintf("%s/customers/%s", h.customerURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return store.Customer{}, fmt.Errorf("build request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return store.Customer{}, fmt.Errorf("call customer-svc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return store.Customer{}, fmt.Errorf("customer %s not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return store.Customer{}, fmt.Errorf("customer-svc returned %d", resp.StatusCode)
	}

	var c store.Customer
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return store.Customer{}, fmt.Errorf("decode customer response: %w", err)
	}
	return c, nil
}

// fetchProduct calls product-svc to retrieve a product by ID.
func (h *OrderHandler) fetchProduct(ctx context.Context, id string) (store.Product, error) {
	url := fmt.Sprintf("%s/products/%s", h.productURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return store.Product{}, fmt.Errorf("build request: %w", err)
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return store.Product{}, fmt.Errorf("call product-svc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return store.Product{}, fmt.Errorf("product %s not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return store.Product{}, fmt.Errorf("product-svc returned %d", resp.StatusCode)
	}

	var p store.Product
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return store.Product{}, fmt.Errorf("decode product response: %w", err)
	}
	return p, nil
}
