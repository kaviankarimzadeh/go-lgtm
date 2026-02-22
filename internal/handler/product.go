// product.go implements the HTTP handlers for the /products CRUD endpoints.
// It is used by product-svc and called by order-svc to validate stock availability.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/kaviankarimzadeh/go-lgtm/internal/store"
)

// ProductHandler holds the dependencies needed by the product CRUD handlers.
type ProductHandler struct {
	log   *zap.Logger
	store *store.ProductStore
}

// NewProductHandler creates a ProductHandler with the provided logger and store.
func NewProductHandler(log *zap.Logger, s *store.ProductStore) *ProductHandler {
	return &ProductHandler{log: log, store: s}
}

// CreateProduct handles POST /products.
//
// Request body (name required; price is in cents, e.g. 1999 = $19.99):
//
//	{"name":"Wireless Headphones","category":"Electronics","price":4999,"stock":50}
//
// Response: 201 Created with the new product including its assigned ID.
func (h *ProductHandler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())

	var p store.Product
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		log.Warn("missing required field: name")
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if p.Price < 0 {
		log.Warn("invalid price", zap.Int("price", p.Price))
		writeError(w, http.StatusBadRequest, "price must be non-negative")
		return
	}
	if p.Stock < 0 {
		log.Warn("invalid stock", zap.Int("stock", p.Stock))
		writeError(w, http.StatusBadRequest, "stock must be non-negative")
		return
	}

	created, err := h.store.Create(p)
	if err != nil {
		log.Error("failed to create product", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "could not create product")
		return
	}

	// Log price and stock as structured fields to enable Grafana queries like:
	//   avg_over_time({service_name="product-svc"} | json | attributes_msg="product created"
	//     | unwrap attributes_price [$__range])
	log.Info("product created",
		zap.String("product_id", created.ID),
		zap.String("name", created.Name),
		zap.String("category", created.Category),
		zap.Int("price", created.Price),
		zap.Int("stock", created.Stock),
	)

	writeJSON(w, http.StatusCreated, created)
}

// ListProducts handles GET /products.
func (h *ProductHandler) ListProducts(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())

	products := h.store.List()
	log.Info("products listed", zap.Int("count", len(products)))
	writeJSON(w, http.StatusOK, products)
}

// GetProduct handles GET /products/{id}.
func (h *ProductHandler) GetProduct(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	product, ok := h.store.Get(id)
	if !ok {
		log.Warn("product not found", zap.String("product_id", id))
		writeError(w, http.StatusNotFound, "product not found")
		return
	}

	log.Info("product retrieved", zap.String("product_id", id))
	writeJSON(w, http.StatusOK, product)
}

// UpdateProduct handles PUT /products/{id}.
func (h *ProductHandler) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	var p store.Product
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		log.Warn("missing required field: name", zap.String("product_id", id))
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	updated, ok := h.store.Update(id, p)
	if !ok {
		log.Warn("product not found for update", zap.String("product_id", id))
		writeError(w, http.StatusNotFound, "product not found")
		return
	}

	log.Info("product updated",
		zap.String("product_id", updated.ID),
		zap.String("name", updated.Name),
		zap.String("category", updated.Category),
		zap.Int("price", updated.Price),
		zap.Int("stock", updated.Stock),
	)
	writeJSON(w, http.StatusOK, updated)
}

// DeleteProduct handles DELETE /products/{id}.
func (h *ProductHandler) DeleteProduct(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	if ok := h.store.Delete(id); !ok {
		log.Warn("product not found for deletion", zap.String("product_id", id))
		writeError(w, http.StatusNotFound, "product not found")
		return
	}

	log.Info("product deleted", zap.String("product_id", id))
	w.WriteHeader(http.StatusNoContent)
}
