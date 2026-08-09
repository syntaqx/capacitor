# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-08-09

Initial release of capacitor, an adaptive HTTP client for Go that adjusts
per-host concurrency based on server capacity and rate-limit signals.

### Added

- `Wrap(client)` builder with opt-in signal handlers: `WithRateLimitHeaders`,
  `WithHTTPStatusHandling`, `WithCapacityHeaders`, `WithGOAWAY`, `WithDefaults`,
  and `WithHandler` for custom `SignalHandler` implementations.
- Adaptive per-host concurrency driven by:
  - Rate-limit headers (`X-RateLimit-*`, `RateLimit-*`, `CF-RateLimit-*`).
  - HTTP status codes (429, 503, 420) and `Retry-After`.
  - Application-level capacity headers (`X-Capacity-*`).
  - HTTP/2 GOAWAY frames.
- Server-signalled block windows are enforced: requests fail fast with
  `*CapacityError` wrapping `ErrBlocked` until the retry window elapses.
- Per-host state inspection via `Client.State` and `Client.Stats`, plus
  `OnStateChange` and `OnSignal` callbacks.
- Configurable concurrency bounds (`WithConcurrency`), acquire timeout
  (`WithAcquireTimeout`), user agent, and custom pool grouping (`WithKeyFunc`,
  `HostKeyFunc`, `PathPrefixKeyFunc`, `ExactPathKeyFunc`).
- Bounded host tracking for long-running clients (`WithMaxTrackedHosts`,
  `WithHostIdleTTL`) that evicts only idle hosts.
- Standalone resizable, context-aware `Semaphore`.
- `Config`/`NewClient` for explicit, programmatic configuration.
- Zero external dependencies (standard library only).

[0.1.0]: https://github.com/syntaqx/capacitor/releases/tag/v0.1.0
