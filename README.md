# go-lgtm

A cloud-native Go application for learning the **Grafana LGTM** observability stack — Loki, (Grafana) Mimir, Tempo, and Grafana — one pillar at a time.

Inspired by [Jaeger HotROD](https://github.com/jaegertracing/jaeger/tree/main/examples/hotrod), this project uses a simple **e-commerce domain** (customers, products, orders) to generate realistic, cross-service telemetry you can explore interactively.

> **Current phase: Phase 1 — Logs (Loki)**

---

## Architecture

```
Browser (HTMX UI)
      │
      ▼
 api-gateway :8080   ◄─── Forms POST to /ui/* (decode form → JSON → proxy)
      │               ─── API calls /customers, /products, /orders
      │               ─── Reverse proxy to downstream services
      │
      ├─── /ui/customers ──────┐
      ├─── /ui/products ───────┤
      └─── /ui/orders ─────────┤
                               ▼
                     (decode form fields)
                     (forward as JSON)
                               │
         ┌─────────────────────┼─────────────────┐
         ▼                     ▼                 ▼
  customer-svc :8083   product-svc :8082   order-svc :8081
      (validate)           (check stock)    (cross-service
                                             validation)
  
      all services ──OTLP/HTTP──▶ Alloy :4318
                                    │
                        ┌───────────┴──────────────┐
                        ▼                          ▼
             Pipeline A (OTLP logs)     Pipeline B (container logs)
                        │                          │
                        └──────────┬───────────────┘
                                   ▼
                               Loki :3100
                                   │
                                   ▼
                             Grafana :3000
```

Each service has its own `SERVICE_NAME`, producing 4 separate Loki streams:
`api-gateway`, `order-svc`, `product-svc`, `customer-svc`.

---

## Quick Start

```bash
# Build and start the full stack
docker compose up --build

# Open the web UI
open http://localhost:8080

# Open Grafana
open http://localhost:3000
```

| Service     | URL                        | Purpose                      |
|-------------|----------------------------|------------------------------|
| Web UI      | http://localhost:8080      | HTMX dashboard               |
| Grafana     | http://localhost:3000      | Log visualisation            |
| Alloy UI    | http://localhost:12345     | Pipeline graph + debug       |
| Loki        | http://localhost:3100      | Log API (for direct testing) |

---

## Generating Telemetry

### Option A — Web UI (recommended)

Open `http://localhost:8080` and use the dashboard:

| Feature | What it does |
|---|---|
| **Add Customer / Product** | Creates a resource → `info` log in the respective service |
| **Place Order** | Validates customer + product → logs in `order-svc`, `customer-svc`, `product-svc` |
| **Trigger Error** | Places an order with an invalid customer ID → `warn` logs in `order-svc` + `customer-svc` |
| **Simulate Load** | Fires N random orders concurrently → burst of logs across all services |

**How the UI works:** Forms are submitted via HTMX to the gateway's `/ui/*` endpoints, which decode the form fields, forward them as JSON to the downstream services, then return the refreshed HTML so the table updates instantly without a full page reload.

### Option B — Seed script

```bash
# Seed 20 customers + 30 products + 50 orders (while docker compose is up)
go run ./scripts/seed.go

# Against a custom host
API_URL=http://localhost:8080 go run ./scripts/seed.go
```

### Option C — curl

```bash
# Create a customer
curl -s -X POST http://localhost:8080/customers \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com","country":"Germany"}' | jq

# Create a product
curl -s -X POST http://localhost:8080/products \
  -H "Content-Type: application/json" \
  -d '{"name":"Wireless Headphones","category":"Electronics","price":4999,"stock":50}' | jq

# Place an order (replace IDs with real ones from the responses above)
curl -s -X POST http://localhost:8080/orders \
  -H "Content-Type: application/json" \
  -d '{"customer_id":"<id>","product_id":"<id>","quantity":2}' | jq
```

---

## Project Structure

```
cmd/
  api-gateway/main.go   # HTMX web UI + API proxy  (:8080)
  order-svc/main.go     # Order management          (:8081)
  product-svc/main.go   # Product catalogue         (:8082)
  customer-svc/main.go  # Customer profiles         (:8083)
  healthcheck/main.go   # Distroless health binary

internal/
  logger/logger.go      # Zap + OTel bridge (shared by all services)
  handler/
    handler.go          # Base handler + Middleware + Health
    context.go          # Request-scoped logger context helpers
    customer.go         # Customer CRUD handlers
    product.go          # Product CRUD handlers
    order.go            # Order CRUD + cross-service validation
  store/
    customer.go         # Thread-safe in-memory CustomerStore
    product.go          # Thread-safe in-memory ProductStore
    order.go            # Thread-safe in-memory OrderStore
  ui/
    templates/          # Go html/template files (embedded in binary)
    static/style.css    # Dark-mode CSS (embedded in binary)
    ui.go               # embed.FS + template renderer

configs/
  alloy/config.alloy    # Alloy: OTLP receiver + Docker log tailing
  grafana/provisioning/ # Auto-provision Loki datasource

scripts/
  seed.go               # Standalone seed utility (//go:build ignore)

Dockerfile              # Multi-stage: 1 builder → 4 distroless final stages
docker-compose.yml      # Full stack orchestration
```

---

## API Reference

All endpoints are accessible via the api-gateway at `http://localhost:8080`.

### Customers

| Method | Path               | Description             |
|--------|--------------------|-------------------------|
| POST   | /customers         | Create a customer       |
| GET    | /customers         | List all customers      |
| GET    | /customers/{id}    | Get a customer by ID    |
| PUT    | /customers/{id}    | Update a customer       |
| DELETE | /customers/{id}    | Delete a customer       |

### Products

| Method | Path               | Description             |
|--------|--------------------|-------------------------|
| POST   | /products          | Create a product        |
| GET    | /products          | List all products       |
| GET    | /products/{id}     | Get a product by ID     |
| PUT    | /products/{id}     | Update a product        |
| DELETE | /products/{id}     | Delete a product        |

### Orders

| Method | Path               | Description             |
|--------|--------------------|-------------------------|
| POST   | /orders            | Place an order          |
| GET    | /orders            | List all orders         |
| GET    | /orders/{id}       | Get an order by ID      |
| PUT    | /orders/{id}       | Update an order         |
| DELETE | /orders/{id}       | Delete an order         |

---

## Exploring Logs in Grafana

Open `http://localhost:3000` → Explore → select **Loki**.

### Filter by service

```logql
# All logs from order-svc
{service_name="order-svc"}

# Logs from all four app services
{service_name=~"api-gateway|order-svc|product-svc|customer-svc"}
```

### Filter by log level

```logql
# Warn and error logs across all services (cross-service errors)
{service_name=~"order-svc|customer-svc"} | json | severity = "warn"

# Info logs from order-svc only
{service_name="order-svc"} | json | severity = "info"
```

### Filter by business fields

```logql
# Orders from customers in Germany
{service_name="order-svc"} | json | attributes_customer_country = "Germany"

# All confirmed orders
{service_name="order-svc"} | json | attributes_status = "confirmed"

# All failed orders (cross-service validation failures)
{service_name="order-svc"} | json | attributes_status = "failed"

# Products in the Electronics category
{service_name="product-svc"} | json | attributes_category = "Electronics"
```

### Metric queries (for Grafana panels)

```logql
# Orders per status over time
sum by (attributes_status) (
  count_over_time(
    {service_name="order-svc"} | json | attributes_status != "" [$__range]
  )
)

# Customers created per country
sum by (attributes_country) (
  count_over_time(
    {service_name="customer-svc"} | json | attributes_msg = "customer created" [$__range]
  )
)

# Products per category
sum by (attributes_category) (
  count_over_time(
    {service_name="product-svc"} | json | attributes_category != "" [$__range]
  )
)
```

### Cross-service correlation

When an order fails due to an invalid customer, you see correlated warn logs across two services:

```logql
# Show all warn logs across services (cross-service error view)
{service_name=~"order-svc|customer-svc"} | json | severity = "warn"
```

---

## Log Pipelines

Two parallel pipelines collect logs into Loki:

**Pipeline A — Application logs (OTLP)**
Each service sends structured JSON logs via OpenTelemetry SDK → Alloy → Loki.
These logs have rich attributes: `service_name`, `severity`, all business fields.

**Pipeline B — Container logs (Docker socket)**
Alloy tails `stdout`/`stderr` of every Docker container via the Docker socket.
These are the safety net: they capture startup panics, crashes, and any output
written before the OTel SDK initialises.

Label to distinguish them in Loki:
```logql
{source="docker"}    # Pipeline B (container stdout)
{exporter="OTLP"}    # Pipeline A (application logs)
```

---

## How `service_name` Works

When a log is sent via OTLP, Alloy converts the OTel resource attribute
`service.name` into a Loki stream label `service_name`. This is why you see
four distinct stream labels (`api-gateway`, `order-svc`, etc.) in the Loki
Label browser, even though they all go through the same Alloy instance.

For container logs (Pipeline B), `service_name` is derived from the container
name by Loki's automatic label detection.

---

## Roadmap

| Phase | Pillar     | Stack component | Status |
|-------|-----------|-----------------|--------|
| 1     | Logs      | Loki + Alloy    | ✅ Done |
| 2     | Traces    | Tempo           | Planned |
| 3     | Metrics   | Mimir / Prometheus | Planned |
| 4     | Profiles  | Pyroscope       | Planned |
