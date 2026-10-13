# Implications of a future OpenTelemetry (OTEL) migration

The collectors are Prometheus-native today: `prometheus.Collector` implementations
registered against controller-runtime's Prometheus registry. If Meridio-2 later moves
toward OTEL, the implications differ sharply by scope.

## Option 1: OTEL Collector scraping `/metrics` (no code change)

Run an OTEL Collector with its `prometheusreceiver` pointed at the existing HTTPS
`/metrics` endpoint and export OTLP downstream. Purely a deployment change, and the
reason the current design is not a dead end.

## Option 2: in-process Prometheus to OTEL bridge (moderate)

Feed the existing registry into an OTEL `MeterProvider` via
`go.opentelemetry.io/contrib/bridges/prometheus` and export OTLP. The collector code
stays; only the registration wiring changes, in each binary that registers
collectors. The bridge is a migration aid rather than a full translation, so check
its current documentation for whether any of our metrics are affected before relying
on it.

## Option 3: rewriting instrumentation to the OTEL metrics API (largest)

The pull-based `Collect()` logic maps reasonably onto OTEL **observable** (async)
gauge callbacks, so the "read state when asked" shape survives. Two things do not port
cleanly.

### The cache-sync fail-fast contract

The controller-manager and network-sidecar collectors emit
`prometheus.NewInvalidMetric(...)` when a scrape arrives before the informer cache has
synced, which makes client_golang's handler return HTTP 500 to the scraper. That is a
deliberate, actionable error instead of a partial or blocking scrape. See
`internal/metrics/util/cache_sync.go` and those collectors' `Collect` methods.

OTEL observable callbacks have **no equivalent**: a callback cannot fail a collection
back to the reader, so it cannot turn "cache not synced" into a scrape error. This
behavior needs redesigning rather than porting, for instance as a separate
`cache_synced` gauge.

The router collector is already shaped that way and would port cleanly. It reads BIRD
over `birdc` rather than an informer cache, and deliberately reports failure through
its own `router_metrics_collect_success` gauge instead of failing the scrape, because
failing it would stale every other series on the endpoint. That per-source success
gauge is close to the redesign the other collectors would need.

### controller-runtime's own metrics stay Prometheus-native

The framework registers reconcile and workqueue metrics against its Prometheus
registry and does not emit OTEL. Even after rewriting our collectors, those built-ins
still require the Prometheus registry, bridged or scraped. Changing only our code
cannot fully leave Prometheus.

## Naming and semantics

Prometheus `snake_case` names differ from OTEL dotted names and units, and
`distributiongroup_max_endpoints` uses the Prometheus `+Inf` sentinel for unbounded
strategies. Renaming is a breaking change for any dashboards or alerts built on the
current names.
