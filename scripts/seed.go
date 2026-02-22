//go:build ignore

// seed.go is a standalone utility that seeds the go-lgtm API with realistic
// e-commerce data: customers, products, and orders.
//
// Tagged with //go:build ignore so it is excluded from normal `go build ./...`
// and `go test ./...` runs. Run it explicitly:
//
//	go run ./scripts/seed.go
//
// Configuration via environment variables:
//
//	API_URL    Base URL of the api-gateway (default: http://localhost:8080)
//	DELAY_MS   Milliseconds between requests (default: 50)
//
// What gets seeded:
//  1. 20 customers from diverse countries
//  2. 30 products across multiple categories
//  3. 50 orders (random customer + product combos, ~20% with invalid IDs
//     to produce warn logs — a realistic mix for Grafana dashboards)
//
// Why keep a seed script if the UI has a Simulate Load button?
//   - The seed script is idempotent and useful in CI / headless environments.
//   - It creates a deterministic baseline dataset before load testing.
//   - It seeds customers and products first, which the UI's Simulate Load needs.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ── Domain types (mirrors internal/store) ──────────────────────────────────

type customer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Country string `json:"country"`
}

type product struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Price    int    `json:"price"` // cents
	Stock    int    `json:"stock"`
}

type order struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	ProductID  string `json:"product_id"`
	Quantity   int    `json:"quantity"`
	Status     string `json:"status"`
	Total      int    `json:"total"`
}

// ── Seed data ───────────────────────────────────────────────────────────────

var customerSeed = []customer{
	{Name: "Alice Müller",    Email: "alice@example.de",  Country: "Germany"},
	{Name: "Bob Santos",      Email: "bob@example.br",    Country: "Brazil"},
	{Name: "Chloe Dupont",    Email: "chloe@example.fr",  Country: "France"},
	{Name: "David Kim",       Email: "david@example.kr",  Country: "South Korea"},
	{Name: "Emeka Okafor",    Email: "emeka@example.ng",  Country: "Nigeria"},
	{Name: "Fatima Al-Farsi", Email: "fatima@example.ae", Country: "UAE"},
	{Name: "George Papadopoulos", Email: "george@example.gr", Country: "Greece"},
	{Name: "Hana Tanaka",     Email: "hana@example.jp",   Country: "Japan"},
	{Name: "Ivan Petrov",     Email: "ivan@example.ru",   Country: "Russia"},
	{Name: "Julia Rossi",     Email: "julia@example.it",  Country: "Italy"},
	{Name: "Kaveh Moradi",    Email: "kaveh@example.ir",  Country: "Iran"},
	{Name: "Lena Kowalski",   Email: "lena@example.pl",   Country: "Poland"},
	{Name: "Miguel Torres",   Email: "miguel@example.mx", Country: "Mexico"},
	{Name: "Nora Andersen",   Email: "nora@example.no",   Country: "Norway"},
	{Name: "Omar Sheik",      Email: "omar@example.eg",   Country: "Egypt"},
	{Name: "Priya Sharma",    Email: "priya@example.in",  Country: "India"},
	{Name: "Quinn O'Brien",   Email: "quinn@example.ie",  Country: "Ireland"},
	{Name: "Riya Patel",      Email: "riya@example.ca",   Country: "Canada"},
	{Name: "Sara Lindqvist",  Email: "sara@example.se",   Country: "Sweden"},
	{Name: "Tom Nguyen",      Email: "tom@example.vn",    Country: "Vietnam"},
}

var productSeed = []product{
	{Name: "Wireless Headphones",  Category: "Electronics", Price: 4999,  Stock: 50},
	{Name: "USB-C Hub",            Category: "Electronics", Price: 2999,  Stock: 30},
	{Name: "Mechanical Keyboard",  Category: "Electronics", Price: 8999,  Stock: 15},
	{Name: "4K Webcam",            Category: "Electronics", Price: 6499,  Stock: 20},
	{Name: "Portable SSD 1TB",     Category: "Electronics", Price: 10999, Stock: 25},
	{Name: "Running Shoes",        Category: "Sports",      Price: 7999,  Stock: 40},
	{Name: "Yoga Mat",             Category: "Sports",      Price: 2499,  Stock: 60},
	{Name: "Water Bottle 1L",      Category: "Sports",      Price: 1499,  Stock: 100},
	{Name: "Resistance Bands Set", Category: "Sports",      Price: 1999,  Stock: 45},
	{Name: "Dumbbell 10kg",        Category: "Sports",      Price: 3499,  Stock: 20},
	{Name: "Cotton T-Shirt",       Category: "Clothing",    Price: 1299,  Stock: 80},
	{Name: "Denim Jeans",          Category: "Clothing",    Price: 4999,  Stock: 35},
	{Name: "Winter Jacket",        Category: "Clothing",    Price: 12999, Stock: 10},
	{Name: "Sneakers",             Category: "Clothing",    Price: 8999,  Stock: 30},
	{Name: "Wool Scarf",           Category: "Clothing",    Price: 1999,  Stock: 55},
	{Name: "The Pragmatic Programmer", Category: "Books",   Price: 3499,  Stock: 25},
	{Name: "Clean Code",           Category: "Books",       Price: 2999,  Stock: 20},
	{Name: "Designing Data-Intensive Applications", Category: "Books", Price: 4999, Stock: 15},
	{Name: "Atomic Habits",        Category: "Books",       Price: 1799,  Stock: 40},
	{Name: "Sapiens",              Category: "Books",       Price: 1499,  Stock: 50},
	{Name: "Organic Coffee Beans", Category: "Food",        Price: 1699,  Stock: 90},
	{Name: "Dark Chocolate 70%",   Category: "Food",        Price: 499,   Stock: 200},
	{Name: "Green Tea (50 bags)",  Category: "Food",        Price: 899,   Stock: 75},
	{Name: "Olive Oil Extra Virgin", Category: "Food",      Price: 1299,  Stock: 60},
	{Name: "Granola Mix 500g",     Category: "Food",        Price: 799,   Stock: 80},
	{Name: "Desk Lamp LED",        Category: "Home",        Price: 3499,  Stock: 35},
	{Name: "Plant Pot Ceramic",    Category: "Home",        Price: 1999,  Stock: 40},
	{Name: "Scented Candle",       Category: "Home",        Price: 1299,  Stock: 65},
	{Name: "Bamboo Cutting Board", Category: "Home",        Price: 2499,  Stock: 30},
	{Name: "Cotton Towel Set",     Category: "Home",        Price: 3999,  Stock: 25},
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	apiURL := getEnv("API_URL", "http://localhost:8080")
	delayMS := getEnvInt("DELAY_MS", 50)
	delay := time.Duration(delayMS) * time.Millisecond

	client := &http.Client{Timeout: 10 * time.Second}

	fmt.Printf("Seeding go-lgtm at %s\n", apiURL)
	fmt.Println(strings.Repeat("─", 60))

	// Step 1: Create customers
	fmt.Printf("\n[1/3] Creating %d customers…\n", len(customerSeed))
	createdCustomers := make([]customer, 0, len(customerSeed))
	for _, c := range customerSeed {
		var created customer
		if err := post(client, apiURL+"/customers", c, &created); err != nil {
			fmt.Printf("  ERROR %s: %v\n", c.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-28s  %s  (%s)\n", c.Name, created.ID[:8]+"…", c.Country)
		createdCustomers = append(createdCustomers, created)
		time.Sleep(delay)
	}

	// Step 2: Create products
	fmt.Printf("\n[2/3] Creating %d products…\n", len(productSeed))
	createdProducts := make([]product, 0, len(productSeed))
	for _, p := range productSeed {
		var created product
		if err := post(client, apiURL+"/products", p, &created); err != nil {
			fmt.Printf("  ERROR %s: %v\n", p.Name, err)
			continue
		}
		fmt.Printf("  ✓ %-35s  $%s  stock:%-3d  %s\n",
			truncate(p.Name, 35), formatCents(created.Price), created.Stock, created.ID[:8]+"…")
		createdProducts = append(createdProducts, created)
		time.Sleep(delay)
	}

	if len(createdCustomers) == 0 || len(createdProducts) == 0 {
		fmt.Println("\n✗ No customers or products created — cannot place orders")
		os.Exit(1)
	}

	// Step 3: Place 50 orders (random combos, ~20% with invalid IDs for warn logs)
	nOrders := 50
	fmt.Printf("\n[3/3] Placing %d orders (~20%% will use invalid IDs to generate warn logs)…\n", nOrders)
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	confirmed, failed := 0, 0
	for i := 0; i < nOrders; i++ {
		customerID := createdCustomers[rng.Intn(len(createdCustomers))].ID
		productID := createdProducts[rng.Intn(len(createdProducts))].ID
		quantity := rng.Intn(3) + 1

		// ~20% chance of using an invalid customer ID to produce warn logs.
		if rng.Float64() < 0.2 {
			customerID = "invalid-seed-customer-id"
		}

		payload := map[string]any{
			"customer_id": customerID,
			"product_id":  productID,
			"quantity":    quantity,
		}
		var created order
		if err := post(client, apiURL+"/orders", payload, &created); err != nil {
			fmt.Printf("  [%02d] ERROR: %v\n", i+1, err)
			failed++
			continue
		}
		statusIcon := "✓"
		if created.Status == "failed" {
			statusIcon = "✗"
			failed++
		} else {
			confirmed++
		}
		fmt.Printf("  [%02d] %s  order:%s  status:%-9s  total:$%s\n",
			i+1, statusIcon, created.ID[:8]+"…", created.Status, formatCents(created.Total))
		time.Sleep(delay)
	}

	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("Done!\n")
	fmt.Printf("  Customers : %d\n", len(createdCustomers))
	fmt.Printf("  Products  : %d\n", len(createdProducts))
	fmt.Printf("  Orders    : %d confirmed, %d failed (intentional warn logs)\n", confirmed, failed)
	fmt.Printf("\nOpen Grafana at http://localhost:3000 to explore the logs.\n")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func post(client *http.Client, url string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("server error %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func formatCents(cents int) string {
	return fmt.Sprintf("%.2f", float64(cents)/100)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
