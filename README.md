# Capacitor

[![Go Reference](https://pkg.go.dev/badge/github.com/syntaqx/capacitor.svg)](https://pkg.go.dev/github.com/syntaqx/capacitor)
[![Go Report Card](https://goreportcard.com/badge/github.com/syntaqx/capacitor)](https://goreportcard.com/report/github.com/syntaqx/capacitor)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

An adaptive HTTP client for Go that automatically adjusts concurrency based on rate limiting and capacity signaling headers. Perfect for building well-behaved clients that respect server load.

## Features

- **Wraps any `http.Client`** - bring your own client with custom settings
- **Opt-in signal handlers** - no handlers by default, add only what you need
- **Unified rate limit handling** - one handler covers GitHub, Twitter, Cloudflare, IETF standard
- **Application-level capacity** - custom `X-Capacity-*` headers for fine-grained control
- **Per-host state tracking** - each server gets independent limits
- **Goroutine-safe** - share one client across your entire application
- **Dynamic resizing** - concurrency limits adjust in real-time

## Installation

```bash
go get github.com/syntaqx/capacitor
```

## Quick Start

```go
package main

import (
    "fmt"
    "net/http"
    "time"

    "github.com/syntaqx/capacitor"
)

func main() {
    // Wrap your existing http.Client (or nil for default)
    client := capacitor.Wrap(&http.Client{
        Timeout: 30 * time.Second,
    }).
        WithRateLimitHeaders().    // X-RateLimit-*, RateLimit-*, CF-RateLimit-*
        WithHTTPStatusHandling().  // 429, 503, Retry-After
        Build()

    // Use it like a normal http.Client
    resp, err := client.Get("https://api.github.com/users/octocat")
    if err != nil {
        panic(err)
    }
    defer resp.Body.Close()

    fmt.Println("Status:", resp.StatusCode)
}
```

## API

### Wrapping a Client

```go
// Wrap an existing client
client := capacitor.Wrap(myHTTPClient).
    WithRateLimitHeaders().
    Build()

// Or use nil for http.DefaultClient
client := capacitor.Wrap(nil).
    WithDefaults().
    Build()
```

### Available Handlers

| Method                     | Handles                                                                                                              |
| -------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `WithRateLimitHeaders()`   | `X-RateLimit-*`, `RateLimit-*`, `CF-RateLimit-*` (GitHub, Twitter, Cloudflare, IETF standard - all case-insensitive) |
| `WithHTTPStatusHandling()` | 429, 503, 420 status codes + Retry-After header                                                                      |
| `WithCapacityHeaders()`    | `X-Capacity-*` application-level headers                                                                             |
| `WithGOAWAY()`             | HTTP/2 GOAWAY frame handling                                                                                         |
| `WithDefaults()`           | `WithHTTPStatusHandling()` + `WithRateLimitHeaders()`                                                                |
| `WithAll()`                | All built-in handlers                                                                                                |
| `WithHandler(h)`           | Add a custom `SignalHandler` implementation                                                                          |

### No Handlers = Passthrough

```go
// This behaves exactly like a regular http.Client
// Concurrency defaults to 100 - no signals means no throttling
client := capacitor.Wrap(nil).Build()
```

## Capacity Headers

For application-level capacity signaling, enable `WithCapacityHeaders()`:

| Header                             | Description                                              |
| ---------------------------------- | -------------------------------------------------------- |
| `X-Capacity-Status`                | Server status: `healthy`, `busy`, `degraded`, `at_limit` |
| `X-Capacity-Suggested-Concurrency` | Recommended concurrent requests for this client          |
| `X-Capacity-Tasks-Running`         | Number of server instances currently running             |
| `X-Capacity-Tasks-Desired`         | Target number of server instances                        |
| `X-Capacity-Worker-Load-Factor`    | Current server load (0.0 - 1.0+)                         |

## Configuration

```go
client := capacitor.Wrap(myHTTPClient).
    WithConcurrency(10, 1, 100). // initial, min, max
    WithTimeout(30 * time.Second).
    WithRateLimitHeaders().
    OnStateChange(func(host string, state *capacitor.State) {
        log.Printf("Host %s: concurrency now %d", host, state.CurrentConcurrency)
    }).
    OnSignal(func(host string, signal *capacitor.Signal) {
        log.Printf("Signal from %s: %s (remaining: %d)", host, signal.Source, signal.Remaining)
    }).
    Build()
```

## Inspecting State

```go
// Get state for a specific host
state := client.GetState("https://api.example.com")
fmt.Printf("Status: %s, Concurrency: %d\n", state.Status, state.CurrentConcurrency)

// Get stats for all known hosts
for host, stats := range client.GetStats() {
    fmt.Printf("%s: %d in-use, %d available, %d waiting\n",
        host, stats.InUse, stats.Available, stats.Waiting)
}
```

## Error Handling

When a request can't acquire a concurrency slot in time:

```go
resp, err := client.Get(url)
if err != nil {
    var capErr *capacitor.CapacityError
    if errors.As(err, &capErr) {
        // Server is at capacity, back off
        log.Printf("Capacity exceeded for %s: %v", capErr.Host, capErr.Err)
        log.Printf("Current limit: %d", capErr.State.CurrentConcurrency)
    }
}
```

## Server Implementation

For servers to participate in capacity signaling, they need to return the appropriate headers. See the [Laravel Capacity Signaling](https://github.com/aspyn-io/aspyn-laravel) package for a reference implementation.

Example response headers:

```http
HTTP/1.1 200 OK
X-Capacity-Status: healthy
X-Capacity-Tasks-Running: 10
X-Capacity-Tasks-Desired: 10
X-Capacity-Suggested-Concurrency: 50
X-Capacity-Worker-Load-Factor: 0.45
```

## Use Cases

- **API Clients** - Automatically back off when services are overloaded
- **Web Scrapers** - Respect target server capacity
- **Microservices** - Prevent cascading failures during traffic spikes
- **Batch Processing** - Maximize throughput without overwhelming backends

## Testing

Run unit tests:

```bash
go test ./...
```

Run integration tests (hits real endpoints):

```bash
go test -tags=integration -v ./...
```

Override the default test URL:

```bash
CAPACITOR_TEST_URL=https://api.github.com go test -tags=integration -v ./...
```

## License

MIT License - see [LICENSE](LICENSE) for details.
