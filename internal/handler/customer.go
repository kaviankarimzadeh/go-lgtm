// customer.go implements the HTTP handlers for the /customers CRUD endpoints.
// It is used by customer-svc and also called indirectly by order-svc to validate
// that a customer exists before confirming an order.
//
// Logging strategy:
//   - Required-field validation failures are logged at Warn (client error, not server fault).
//   - Store errors are logged at Error (unexpected internal failure).
//   - Successful operations are logged at Info with all queryable fields as Zap fields,
//     so Loki/LogQL can answer questions like "how many customers from Germany?".
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/kaviankarimzadeh/go-lgtm/internal/store"
)

// CustomerHandler holds the dependencies needed by the customer CRUD handlers.
type CustomerHandler struct {
	log   *zap.Logger
	store *store.CustomerStore
}

// NewCustomerHandler creates a CustomerHandler with the provided logger and store.
func NewCustomerHandler(log *zap.Logger, s *store.CustomerStore) *CustomerHandler {
	return &CustomerHandler{log: log, store: s}
}

// CreateCustomer handles POST /customers.
//
// Request body (name and email required):
//
//	{"name":"Alice","email":"alice@example.com","country":"Germany"}
//
// Response: 201 Created with the new customer object including its assigned ID.
func (h *CustomerHandler) CreateCustomer(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())

	var c store.Customer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	c.Name = strings.TrimSpace(c.Name)
	c.Email = strings.TrimSpace(c.Email)
	if c.Name == "" || c.Email == "" {
		log.Warn("missing required fields",
			zap.Bool("name_missing", c.Name == ""),
			zap.Bool("email_missing", c.Email == ""),
		)
		writeError(w, http.StatusBadRequest, "name and email are required")
		return
	}

	created, err := h.store.Create(c)
	if err != nil {
		log.Error("failed to create customer", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "could not create customer")
		return
	}

	// Emit structured fields so Loki can answer "customers created per country"
	// with:  sum by (attributes_country) (count_over_time({service_name="customer-svc"} | json | attributes_msg="customer created" [$__range]))
	log.Info("customer created",
		zap.String("customer_id", created.ID),
		zap.String("name", created.Name),
		zap.String("email", created.Email),
		zap.String("country", created.Country),
	)

	writeJSON(w, http.StatusCreated, created)
}

// ListCustomers handles GET /customers.
func (h *CustomerHandler) ListCustomers(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())

	customers := h.store.List()
	log.Info("customers listed", zap.Int("count", len(customers)))
	writeJSON(w, http.StatusOK, customers)
}

// GetCustomer handles GET /customers/{id}.
func (h *CustomerHandler) GetCustomer(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	customer, ok := h.store.Get(id)
	if !ok {
		log.Warn("customer not found", zap.String("customer_id", id))
		writeError(w, http.StatusNotFound, "customer not found")
		return
	}

	log.Info("customer retrieved", zap.String("customer_id", id))
	writeJSON(w, http.StatusOK, customer)
}

// UpdateCustomer handles PUT /customers/{id}.
func (h *CustomerHandler) UpdateCustomer(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	var c store.Customer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&c); err != nil {
		log.Warn("invalid request body", zap.String("reason", err.Error()))
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	c.Name = strings.TrimSpace(c.Name)
	c.Email = strings.TrimSpace(c.Email)
	if c.Name == "" || c.Email == "" {
		log.Warn("missing required fields",
			zap.String("customer_id", id),
			zap.Bool("name_missing", c.Name == ""),
			zap.Bool("email_missing", c.Email == ""),
		)
		writeError(w, http.StatusBadRequest, "name and email are required")
		return
	}

	updated, ok := h.store.Update(id, c)
	if !ok {
		log.Warn("customer not found for update", zap.String("customer_id", id))
		writeError(w, http.StatusNotFound, "customer not found")
		return
	}

	log.Info("customer updated",
		zap.String("customer_id", updated.ID),
		zap.String("name", updated.Name),
		zap.String("country", updated.Country),
	)
	writeJSON(w, http.StatusOK, updated)
}

// DeleteCustomer handles DELETE /customers/{id}.
func (h *CustomerHandler) DeleteCustomer(w http.ResponseWriter, r *http.Request) {
	log := fromContext(r.Context())
	id := r.PathValue("id")

	if ok := h.store.Delete(id); !ok {
		log.Warn("customer not found for deletion", zap.String("customer_id", id))
		writeError(w, http.StatusNotFound, "customer not found")
		return
	}

	log.Info("customer deleted", zap.String("customer_id", id))
	w.WriteHeader(http.StatusNoContent)
}
