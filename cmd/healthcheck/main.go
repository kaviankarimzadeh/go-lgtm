// Package main is a minimal HTTP health check binary designed to run inside
// a distroless container where no shell tools (wget, curl) are available.
//
// Docker's HEALTHCHECK instruction requires an executable that exits 0 when
// the service is healthy and non-zero otherwise. Distroless images contain
// nothing but the application binary, so we compile a dedicated tiny binary
// that does exactly one thing: GET /health and check the response.
//
// This binary is compiled in the builder stage and copied into the runtime
// image alongside the main application binary (see Dockerfile).
//
// Usage (set by Dockerfile HEALTHCHECK):
//
//	/healthcheck
//
// The target URL is controlled by the HEALTH_URL environment variable,
// defaulting to http://localhost:8080/health so it works without any
// additional configuration.
package main

import (
	"net/http"
	"os"
)

func main() {
	url := os.Getenv("HEALTH_URL")
	if url == "" {
		url = "http://localhost:8080/health"
	}

	// A short timeout prevents the health check from hanging and blocking
	// Docker's health check timeout, which would also mark the container unhealthy.
	client := &http.Client{Timeout: 3_000_000_000} // 3 seconds in nanoseconds

	resp, err := client.Get(url) //nolint:noctx
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Any 2xx status is considered healthy.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		os.Exit(1)
	}

	os.Exit(0)
}
