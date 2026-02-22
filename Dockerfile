# =============================================================================
# Stage 1 — Builder
# =============================================================================
# One shared builder stage compiles all five binaries (4 services + healthcheck).
# This means a single `go mod download` cache layer is shared across all builds,
# and all source code is compiled in one RUN layer — Docker layer cache is
# invalidated only when go.mod/go.sum or source files change.
#
# CGO_ENABLED=0 produces fully static binaries that have no C runtime dependency,
# which is required for the distroless final stages.
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Cache module downloads separately from source so that changes to .go files
# do not invalidate the module cache layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build flags:
#   -trimpath  removes absolute file paths from panic traces (reproducibility + security)
#   -s -w      strips symbol table and DWARF info (~30% smaller binary)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /api-gateway   ./cmd/api-gateway

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /order-svc     ./cmd/order-svc

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /product-svc   ./cmd/product-svc

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /customer-svc  ./cmd/customer-svc

# The healthcheck binary performs a single GET /health and exits 0 on success.
# It is used by Docker's HEALTHCHECK instruction in every service because the
# distroless runtime image has no shell (wget/curl/sh are unavailable).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /healthcheck   ./cmd/healthcheck


# =============================================================================
# Stage 2a — api-gateway runtime image
# =============================================================================
# Each service gets its own minimal distroless final stage so that the Docker
# Compose services pull separate images and the images stay as small as possible.
# Only the relevant binary + healthcheck are copied from the builder.
FROM gcr.io/distroless/static-debian12:nonroot AS api-gateway
COPY --from=builder /api-gateway  /api-gateway
COPY --from=builder /healthcheck  /healthcheck
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/api-gateway"]


# =============================================================================
# Stage 2b — order-svc runtime image
# =============================================================================
FROM gcr.io/distroless/static-debian12:nonroot AS order-svc
COPY --from=builder /order-svc   /order-svc
COPY --from=builder /healthcheck /healthcheck
EXPOSE 8081
USER nonroot:nonroot
ENTRYPOINT ["/order-svc"]


# =============================================================================
# Stage 2c — product-svc runtime image
# =============================================================================
FROM gcr.io/distroless/static-debian12:nonroot AS product-svc
COPY --from=builder /product-svc /product-svc
COPY --from=builder /healthcheck /healthcheck
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/product-svc"]


# =============================================================================
# Stage 2d — customer-svc runtime image
# =============================================================================
FROM gcr.io/distroless/static-debian12:nonroot AS customer-svc
COPY --from=builder /customer-svc /customer-svc
COPY --from=builder /healthcheck  /healthcheck
EXPOSE 8083
USER nonroot:nonroot
ENTRYPOINT ["/customer-svc"]
