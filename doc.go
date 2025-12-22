// Package capacity provides an adaptive HTTP client that respects
// server capacity signaling headers. It automatically adjusts
// concurrency based on X-Capacity-* headers returned by servers.
//
// The client is safe for concurrent use by multiple goroutines
// and shares capacity state across all requests to the same host.
//
// Basic usage:
//
//	client := capacitor.NewClient(nil) // wraps http.DefaultClient
//	resp, err := client.Get("https://api.example.com/data")
//
// With custom configuration:
//
//	client := capacitor.NewClient(&capacitor.Config{
//	    UserAgent:          "MyApp/1.0",
//	    InitialConcurrency: 10,
//	    MaxConcurrency:     100,
//	    MinConcurrency:     1,
//	})
package capacitor
