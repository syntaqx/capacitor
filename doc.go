// Package capacitor provides an adaptive HTTP client that respects server
// capacity and rate-limit signals. It wraps any http.Client and adjusts the
// number of concurrent requests it sends to each host based on the signals a
// server returns: rate-limit headers (X-RateLimit-*, RateLimit-*, CF-RateLimit-*),
// HTTP status codes (429, 503, Retry-After), application-level capacity headers
// (X-Capacity-*), and HTTP/2 GOAWAY frames.
//
// Signal handling is opt-in: a wrapped client with no handlers behaves exactly
// like the http.Client it wraps. Add only the handlers you need.
//
// Clients are safe for concurrent use by multiple goroutines and track capacity
// state independently per host.
//
// The builder API is the recommended entry point:
//
//	client := capacitor.Wrap(nil).
//	    WithRateLimitHeaders().   // X-RateLimit-*, RateLimit-*, CF-RateLimit-*
//	    WithHTTPStatusHandling(). // 429, 503, 420, Retry-After
//	    Build()
//
//	resp, err := client.Get("https://api.example.com/data")
//
// For explicit configuration, construct a Config and use NewClient:
//
//	client := capacitor.NewClient(&capacitor.Config{
//	    UserAgent:          "MyApp/1.0",
//	    InitialConcurrency: 10,
//	    MaxConcurrency:     100,
//	    MinConcurrency:     1,
//	    SignalHandlers:     capacitor.DefaultSignalHandlers(),
//	})
package capacitor
