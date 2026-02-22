// Package store provides in-memory data stores for the go-lgtm demo services.
//
// Design decisions:
//   - Each resource type (Customer, Product, Order) lives in its own file to
//     mirror the service boundary split: customer-svc owns CustomerStore,
//     product-svc owns ProductStore, order-svc owns OrderStore. The package is
//     shared via Go module imports rather than network calls, keeping the
//     in-process store simple while the HTTP service layer enforces boundaries.
//   - sync.RWMutex protects every store. Read-heavy operations (List, Get) hold
//     only a read lock so they can proceed concurrently; writes hold a full lock.
//   - IDs are generated with crypto/rand so they are unpredictable and safe to
//     expose in URLs without risk of enumeration attacks.
//   - All stores follow the same Create/List/Get/Update/Delete contract so
//     handlers can be reasoned about consistently across resources.
package store

import (
	"crypto/rand"
	"fmt"
	"sync"
)

// Customer represents a registered buyer in the e-commerce system.
//
// Structured log fields emitted by customer-svc use the same field names as
// the JSON keys here, so LogQL queries like
//
//	{service_name="customer-svc"} | json | attributes_country="Germany"
//
// work without any extra mapping.
type Customer struct {
	// ID is a randomly generated 16-byte hex string. Immutable after creation.
	ID string `json:"id"`

	// Name is the customer's display name. Required.
	Name string `json:"name"`

	// Email is the customer's email address. Required; used as a natural key in logs.
	Email string `json:"email"`

	// Country is the customer's country of residence (e.g. "Germany", "Brazil").
	// Enables "orders per country" breakdown in Grafana metric queries.
	Country string `json:"country,omitempty"`
}

// CustomerStore is a thread-safe in-memory store for Customer records.
type CustomerStore struct {
	mu        sync.RWMutex
	customers map[string]Customer
}

// NewCustomerStore creates an empty CustomerStore ready for concurrent use.
func NewCustomerStore() *CustomerStore {
	return &CustomerStore{customers: make(map[string]Customer)}
}

// Create adds a new Customer to the store, assigning a random ID.
func (s *CustomerStore) Create(c Customer) (Customer, error) {
	id, err := generateID()
	if err != nil {
		return Customer{}, fmt.Errorf("generate customer id: %w", err)
	}
	c.ID = id

	s.mu.Lock()
	s.customers[id] = c
	s.mu.Unlock()

	return c, nil
}

// List returns a snapshot of all customers. The slice is a copy; mutations
// do not affect the store. Order is not guaranteed.
func (s *CustomerStore) List() []Customer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Customer, 0, len(s.customers))
	for _, c := range s.customers {
		out = append(out, c)
	}
	return out
}

// Get retrieves the Customer with the given id.
// Returns the customer and true if found, or a zero Customer and false if not.
func (s *CustomerStore) Get(id string) (Customer, bool) {
	s.mu.RLock()
	c, ok := s.customers[id]
	s.mu.RUnlock()
	return c, ok
}

// Update replaces the Customer identified by id with the fields from c.
// The id from the URL always takes precedence over c.ID.
// Returns the updated Customer and true if found, false if not.
func (s *CustomerStore) Update(id string, c Customer) (Customer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.customers[id]; !ok {
		return Customer{}, false
	}
	c.ID = id
	s.customers[id] = c
	return c, true
}

// Delete removes the Customer with the given id.
// Returns true if the customer existed and was removed, false otherwise.
func (s *CustomerStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.customers[id]; !ok {
		return false
	}
	delete(s.customers, id)
	return true
}

// Count returns the number of customers currently in the store.
// Used by the UI dashboard to show live totals.
func (s *CustomerStore) Count() int {
	s.mu.RLock()
	n := len(s.customers)
	s.mu.RUnlock()
	return n
}

// generateID produces a random 16-byte hex string for use as a resource ID.
// Using crypto/rand ensures IDs are unpredictable, safe for URL exposure.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}
