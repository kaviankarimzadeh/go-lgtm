package store

import (
	"fmt"
	"sync"
)

// Product represents an item available for purchase in the catalogue.
//
// Structured log fields emitted by product-svc match these JSON keys, enabling
// LogQL filters like:
//
//	{service_name="product-svc"} | json | attributes_category="Electronics"
type Product struct {
	// ID is a randomly generated 16-byte hex string. Immutable after creation.
	ID string `json:"id"`

	// Name is the product's display name. Required.
	Name string `json:"name"`

	// Category groups products for Grafana breakdown queries (e.g. "Electronics",
	// "Clothing", "Books", "Food"). Logged as attributes_category in Loki.
	Category string `json:"category,omitempty"`

	// Price is the unit price in USD, stored as cents to avoid float precision
	// issues. E.g. $19.99 is stored as 1999.
	// For display, divide by 100.
	Price int `json:"price"`

	// Stock is the current available inventory count.
	// order-svc checks this before accepting a new order.
	// A zero value means out-of-stock; order-svc will reject the order and emit
	// a warn log — useful for deliberately triggering error paths in the UI.
	Stock int `json:"stock"`
}

// ProductStore is a thread-safe in-memory store for Product records.
type ProductStore struct {
	mu       sync.RWMutex
	products map[string]Product
}

// NewProductStore creates an empty ProductStore ready for concurrent use.
func NewProductStore() *ProductStore {
	return &ProductStore{products: make(map[string]Product)}
}

// Create adds a new Product to the store, assigning a random ID.
func (s *ProductStore) Create(p Product) (Product, error) {
	id, err := generateID()
	if err != nil {
		return Product{}, fmt.Errorf("generate product id: %w", err)
	}
	p.ID = id

	s.mu.Lock()
	s.products[id] = p
	s.mu.Unlock()

	return p, nil
}

// List returns a snapshot of all products. Order is not guaranteed.
func (s *ProductStore) List() []Product {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Product, 0, len(s.products))
	for _, p := range s.products {
		out = append(out, p)
	}
	return out
}

// Get retrieves the Product with the given id.
func (s *ProductStore) Get(id string) (Product, bool) {
	s.mu.RLock()
	p, ok := s.products[id]
	s.mu.RUnlock()
	return p, ok
}

// Update replaces the Product identified by id with the fields from p.
func (s *ProductStore) Update(id string, p Product) (Product, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.products[id]; !ok {
		return Product{}, false
	}
	p.ID = id
	s.products[id] = p
	return p, true
}

// Delete removes the Product with the given id.
func (s *ProductStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.products[id]; !ok {
		return false
	}
	delete(s.products, id)
	return true
}

// Count returns the number of products currently in the store.
func (s *ProductStore) Count() int {
	s.mu.RLock()
	n := len(s.products)
	s.mu.RUnlock()
	return n
}
