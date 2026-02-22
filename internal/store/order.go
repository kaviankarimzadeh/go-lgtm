package store

import (
	"fmt"
	"sync"
)

// OrderStatus represents the lifecycle state of an order.
// Using a typed string constant (not iota int) keeps the value human-readable
// in JSON logs and Loki queries without a lookup table.
type OrderStatus string

const (
	// OrderStatusPending means the order has been accepted but not yet fulfilled.
	OrderStatusPending OrderStatus = "pending"

	// OrderStatusConfirmed means stock was reserved and the order is confirmed.
	OrderStatusConfirmed OrderStatus = "confirmed"

	// OrderStatusFailed means the order could not be placed (bad customer ID,
	// out-of-stock product, etc.). order-svc emits a warn log for these cases,
	// making them visible in Grafana and triggerable from the UI "error" button.
	OrderStatusFailed OrderStatus = "failed"

	// OrderStatusCancelled means the order was explicitly cancelled after creation.
	OrderStatusCancelled OrderStatus = "cancelled"
)

// Order represents a purchase of one product by one customer.
//
// Cross-service log correlation:
//   - order-svc logs order_id + customer_id + product_id on every state change.
//   - When traces are added (Phase 2), the same order_id will appear in Tempo
//     spans, linking Loki logs to Tempo traces in Grafana's "Logs to Traces" feature.
type Order struct {
	// ID is a randomly generated 16-byte hex string. Immutable after creation.
	ID string `json:"id"`

	// CustomerID references the customer who placed the order.
	// order-svc calls customer-svc to validate this ID before confirming.
	CustomerID string `json:"customer_id"`

	// ProductID references the product being ordered.
	// order-svc calls product-svc to validate stock before confirming.
	ProductID string `json:"product_id"`

	// Quantity is the number of units ordered. Must be >= 1.
	Quantity int `json:"quantity"`

	// Status tracks the order lifecycle. See OrderStatus constants above.
	Status OrderStatus `json:"status"`

	// Total is the final price in cents (quantity × product.Price).
	// Computed by order-svc at creation time; 0 if status is "failed".
	Total int `json:"total"`
}

// OrderStore is a thread-safe in-memory store for Order records.
type OrderStore struct {
	mu     sync.RWMutex
	orders map[string]Order
}

// NewOrderStore creates an empty OrderStore ready for concurrent use.
func NewOrderStore() *OrderStore {
	return &OrderStore{orders: make(map[string]Order)}
}

// Create adds a new Order to the store, assigning a random ID.
func (s *OrderStore) Create(o Order) (Order, error) {
	id, err := generateID()
	if err != nil {
		return Order{}, fmt.Errorf("generate order id: %w", err)
	}
	o.ID = id

	s.mu.Lock()
	s.orders[id] = o
	s.mu.Unlock()

	return o, nil
}

// List returns a snapshot of all orders. Order of results is not guaranteed.
func (s *OrderStore) List() []Order {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Order, 0, len(s.orders))
	for _, o := range s.orders {
		out = append(out, o)
	}
	return out
}

// Get retrieves the Order with the given id.
func (s *OrderStore) Get(id string) (Order, bool) {
	s.mu.RLock()
	o, ok := s.orders[id]
	s.mu.RUnlock()
	return o, ok
}

// Update replaces the Order identified by id with the fields from o.
// The id from the URL always takes precedence over o.ID.
func (s *OrderStore) Update(id string, o Order) (Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orders[id]; !ok {
		return Order{}, false
	}
	o.ID = id
	s.orders[id] = o
	return o, true
}

// Delete removes the Order with the given id.
func (s *OrderStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orders[id]; !ok {
		return false
	}
	delete(s.orders, id)
	return true
}

// Count returns the number of orders currently in the store.
func (s *OrderStore) Count() int {
	s.mu.RLock()
	n := len(s.orders)
	s.mu.RUnlock()
	return n
}
