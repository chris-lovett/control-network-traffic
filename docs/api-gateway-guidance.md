# API Gateway Guidance

Practitioner reference for Consul Enterprise API Gateway configuration in a
shared, multi-app namespace. For runnable examples see
[Demo 05](../demos/05-api-gateway-ingress/README.md) and
[Demo 06](../demos/06-multi-app-gateway/README.md).

---

## Contents

### [Orientation](#orientation)
*Read first — architectural context and quick navigation.*
- [The architectural shift](#the-architectural-shift)
- [Quick reference — concern to config](#quick-reference--concern-to-config)

### [Tuning principles](#tuning-principles)
*Read when building or reviewing a gateway configuration.*
- [Routing design](#routing-design)
- [TLS policy](#tls-policy)
- [Service intentions — namespace ≠ trust boundary](#service-intentions--namespace--trust-boundary)
- [Per-route resilience](#per-route-resilience)
- [Per-route rate limiting](#per-route-rate-limiting)
- [Header policy](#header-policy)
- [Observability](#observability)
- [Sizing and scaling](#sizing-and-scaling)
- [Config ownership](#config-ownership)
- [Namespace grouping criteria](#namespace-grouping-criteria)

### [Anti-patterns](#anti-patterns)
*Read when reviewing a PR or troubleshooting a live issue.*
- [Anti-pattern index](#anti-pattern-index)
- [1 — Wildcard intentions](#1--wildcard-intentions-just-for-the-migration)
- [2 — Single catch-all route](#2--single-catch-all-route-serving-multiple-apps)
- [3 — Shared TLS certificate](#3--shared-tls-certificate-across-unrelated-apps)
- [4 — Blanket timeout/retry policy](#4--blanket-timeoutretry-policy-applied-gateway-wide)
- [5 — Aggregate-only rate limiting](#5--aggregate-only-rate-limiting)
- [6 — Gateway used for east-west traffic](#6--using-the-api-gateway-for-east-west-traffic)
- [7 — Mixed compliance scopes](#7--mixing-compliance-scopes-without-a-deliberate-decision)
- [8 — Under-sized gateway](#8--under-sizing-the-shared-gateway)
- [9 — No config ownership](#9--no-clear-config-ownership)
- [10 — Route sprawl](#10--route-sprawl-as-a-false-isolation-mechanism)
- [11 — Retrying non-idempotent operations](#11--retrying-non-idempotent-operations)

---

## Orientation

### The architectural shift

```
Before:  1 namespace → 1 app → 1 gateway   (isolation was free)
After:   1 namespace → N apps → 1 gateway  (isolation must be explicit)
```

Moving from a dedicated gateway per app to a shared gateway per namespace
changes the operational contract for every property that used to be automatic:
trust, blast radius, cert lifecycle, traffic fairness, and capacity planning.
None of these are implicit anymore. The sections below address each one.

Every tuning decision in this document maps to one of four concerns:

| Concern | The question that must have an answer |
|---------|---------------------------------------|
| **Trust** | Is every service-to-service relationship governed by an explicit intention — not namespace co-location? |
| **Resilience** | Are timeouts, retries, and circuit breakers configured per-route to match each app's actual SLA? |
| **Fairness** | Do per-route rate limits prevent any single app from degrading its neighbors? |
| **Capacity** | Is the gateway sized against aggregate traffic across all apps, with peak-timing analysis done? |

---

### Quick reference — concern to config

Jump directly to the section and Consul resource that addresses each concern.

| Concern | Section | Primary Consul resource |
|---------|---------|------------------------|
| **Trust** — explicit service-to-service permissions | [Service intentions](#service-intentions--namespace--trust-boundary) | [`ServiceIntentions`](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-intentions) |
| **Routing** — hostname isolation, protocol separation | [Routing design](#routing-design) | [`HTTPRoute`](https://developer.hashicorp.com/consul/docs/north-south/api-gateway) |
| **TLS** — per-hostname certs, cipher policy, Vault | [TLS policy](#tls-policy) | [`VaultPKISecret`](https://developer.hashicorp.com/vault/docs/platform/k8s/vso/api-reference#vaultpkisecret) |
| **Resilience** — timeouts, retries, circuit breaking | [Per-route resilience](#per-route-resilience) | [`ServiceDefaults`](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-defaults) |
| **Fairness** — noisy-neighbor prevention | [Per-route rate limiting](#per-route-rate-limiting) | [`RouteRetryFilter`](https://developer.hashicorp.com/consul/docs/north-south/api-gateway) |
| **Observability** — per-app metrics, access logs, tracing | [Observability](#observability) | `proxy-defaults` + Envoy stats tags |
| **Capacity** — HPA, xDS memory, aggregate sizing | [Sizing and scaling](#sizing-and-scaling) | `HorizontalPodAutoscaler` |
| **Ownership** — RBAC, CODEOWNERS, GitOps | [Config ownership](#config-ownership) | `CODEOWNERS` + `ReferenceGrant` |
| **Grouping** — which apps belong together | [Namespace grouping criteria](#namespace-grouping-criteria) | Consul Enterprise namespaces |

**Reviewing a PR or troubleshooting?** Go directly to the [Anti-pattern index](#anti-pattern-index).

---

## Tuning principles

### Routing design

### Use hostname-based separation, not path-based fan-out

Hostname-based routing gives each app an independent certificate lifecycle,
independent route ownership, and a clean audit boundary. Path-based routing
on a shared hostname couples all of these and creates ambiguity about which
team owns which traffic.

**Correct — one `HTTPRoute` per app, explicit hostname:**

```yaml
# File: consul/config-entries/api-gateway.yaml
# Each app gets its own HTTPRoute bound to a distinct hostname.
# See the full resource in consul/config-entries/api-gateway.yaml.
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: api-route
  namespace: control-network-traffic
spec:
  hostnames: ["api.example.internal"]       # transactional app
  # ...
---
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: reporting-route
  namespace: control-network-traffic
spec:
  hostnames: ["reporting.example.internal"] # reporting app
  # ...
```

Hostname separation means:
- An auditor reviewing the `api.example.internal` listener sees only that
  app's policy — nothing from the reporting app
- A cert rotation event for one app is fully isolated from the other
- RBAC on `HTTPRoute` objects maps cleanly to the team that owns the app

**❌ Do not do this — shared hostname with path-based fan-out:**

```yaml
# ❌ DO NOT USE — path-based fan-out on a shared hostname
# Route ownership is ambiguous. Cert lifecycle is coupled.
# Auditors cannot assess one app's policy without reading the other's.
spec:
  hostnames: ["*.example.internal"]
  rules:
    - matches: [{path: {value: /frontend}}]
    - matches: [{path: {value: /reporting}}]
```

### Keep path matches narrow and explicit

A `PathPrefix: /` catch-all serving multiple apps is a misrouting risk. When
the match is ambiguous, Envoy's route evaluation order determines which app
receives the request — a property invisible at the Gateway API level and easy
to break with a config change.

**Correct — narrow path prefix scoped to the owning service:**

```yaml
# File: consul/config-entries/api-gateway.yaml
rules:
  - matches:
      - path:
          type: PathPrefix
          value: /api/v1     # explicit; owned by the api team
```

**❌ Do not do this — catch-all that delegates routing downstream:**

```yaml
# ❌ DO NOT USE — gateway-level routing is bypassed entirely
# Per-route policy (timeouts, rate limits) cannot be applied.
# route_name in access logs is meaningless for attribution.
rules:
  - matches:
      - path:
          type: PathPrefix
          value: /           # all traffic lands here regardless of destination
```

### Assign distinct listeners to distinct protocols

If any app uses gRPC, HTTP/2, or raw TCP alongside HTTP/1.1 apps, give each
protocol its own listener. Multiplexing mixed protocols on one listener creates
subtle framing bugs, complicates timeout configuration, and makes access log
analysis harder.

```yaml
# File: consul/config-entries/api-gateway.yaml — Gateway spec listeners field
spec:
  listeners:
    - name: https         # HTTP/1.1 and HTTP/2 transactional apps
      protocol: HTTPS
      port: 8443
    - name: grpc          # gRPC services — declare separately even though
      protocol: HTTPS     # gRPC also uses HTTP/2; keeps policy independent
      port: 8444
    - name: tcp-legacy    # raw TCP for any non-HTTP legacy integration
      protocol: TCP
      port: 9000
```

### Restrict route binding to the local namespace

Set `allowedRoutes` on the `Gateway` listener to prevent other namespaces
from binding routes to your gateway without explicit permission.

```yaml
# File: consul/config-entries/api-gateway.yaml — Gateway spec listeners field
spec:
  listeners:
    - name: https
      protocol: HTTPS
      port: 8443
      allowedRoutes:
        namespaces:
          from: Same    # only HTTPRoutes in this namespace can bind
```

`from: All` is the unsafe default — it allows any namespace to bind routes
to your gateway. Use `ReferenceGrant` explicitly when cross-namespace binding
is genuinely required.

---

### TLS policy

### Per-hostname SNI certificates — never a shared cert

Each app's cert lifecycle (issuance, rotation, revocation) must be independent
even though the apps share a gateway process.

**Correct — one `certificateRef` per hostname in `consul/config-entries/api-gateway.yaml`:**

```yaml
# File: consul/config-entries/api-gateway.yaml — Gateway spec listeners[].tls field
spec:
  listeners:
    - name: https
      protocol: HTTPS
      port: 8443
      tls:
        mode: Terminate
        certificateRefs:
          - name: api-tls-cert        # VaultPKISecret for api.example.internal
          - name: reporting-tls-cert  # VaultPKISecret for reporting.example.internal
          # Each cert is issued and rotated independently via VSO —
          # see the VaultPKISecret resource in the TLS section below.
```

**❌ Do not do this — single wildcard cert shared across all apps:**

```yaml
# ❌ DO NOT USE — one expiry or compromise event forces simultaneous
# rotation across every app on this gateway.
        tls:
          certificateRefs:
            - name: shared-wildcard-cert
```

If an app handles sensitive transactions, its certificate compromise must not
require a maintenance window for an unrelated reporting service on the same
gateway. Coupled cert lifecycles make that separation impossible.

### Enforce minimum TLS version and cipher policy

Set minimum TLS version and cipher suites on the `GatewayTLSConfig` resource
in Consul Enterprise. Relying on defaults is not a sufficient control in
environments subject to security audit.

```yaml
# File: consul/config-entries/api-gateway.yaml
# GatewayTLSConfig is a Consul-specific resource applied alongside the Gateway.
# Apply with: kubectl apply -f consul/config-entries/api-gateway.yaml
apiVersion: consul.hashicorp.com/v1alpha1
kind: GatewayTLSConfig
metadata:
  name: api-gateway-tls
  namespace: control-network-traffic
spec:
  tlsMinVersion: "TLSv1_2"   # TLS 1.3 preferred where client support allows
  tlsCipherSuites:
    - TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
    - TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
    - TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
    # Explicitly absent: RC4, 3DES, NULL, EXPORT, anon suites
```

### Use Vault Secrets Operator for certificate lifecycle on OpenShift

The gateway reads listener certs from standard Kubernetes `tls` Secrets. The
Vault Secrets Operator (VSO) is the recommended mechanism for keeping those
Secrets current from a Vault PKI backend — it handles issuance and automatic
rotation without manual `vault` CLI steps.

```yaml
# File: demos/05-api-gateway-ingress/frontend-vault-pki-secret.yaml
# Apply with: kubectl apply -f demos/05-api-gateway-ingress/frontend-vault-pki-secret.yaml
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultPKISecret
metadata:
  name: api-tls-cert
  namespace: control-network-traffic
spec:
  mount: pki
  role: <your-pki-role>
  commonName: api.example.internal
  altNames:
    - api.example.internal
  destination:
    name: api-tls-cert       # must match the certificateRef name in api-gateway.yaml
    create: true
    type: kubernetes.io/tls
  expiryOffset: 30d
  ttl: 90d
```

The gateway hot-reloads certificates when the Secret is updated — VSO
rotation events do not require a gateway restart or connection drain.

> **Note:** Vault configured as the Consul service mesh CA
> (`connect.ca_provider = "vault"`) issues **leaf certificates for sidecar
> mTLS**. That is a separate system from the gateway listener TLS cert
> described here. Both use Vault, but they operate independently.

### mTLS termination and downstream re-encryption

The gateway terminates TLS from external clients at the listener. Consul's
connect-inject sidecar on each upstream pod handles mTLS for the downstream
hop into the mesh. Traffic never crosses the mesh unencrypted — the gateway
must not be configured as a TLS passthrough device for mesh-enrolled services.

### Compliance-scoped app isolation

Any app in a regulated compliance scope must have its certificate and listener
config treated as an independent unit. Do not allow co-location to create
implicit trust inheritance — a compliance-scoped app must not inherit TLS
policy from a neighboring app simply because they share a gateway.

---

### Service intentions — namespace ≠ trust boundary

This is the most operationally significant change in the shared-gateway model.

With one app per namespace, the namespace boundary implicitly provided security
isolation. With multiple apps per namespace, that guarantee is gone. Every
service-to-service relationship must be explicitly permitted via intentions.

**Correct — one `ServiceIntentions` resource per destination, explicit pairs:**

```hcl
# File: demos/06-multi-app-gateway/intentions.yaml
# Apply with: kubectl apply -f demos/06-multi-app-gateway/intentions.yaml
Kind = "service-intentions"
Name = "backend"
Sources = [
  {
    Name   = "api"
    Action = "allow"    # api is permitted to call backend
  },
  {
    # Explicit denial — reporting must not call backend directly.
    # This is intentional, not an oversight.
    Name   = "reporting"
    Action = "deny"
  }
]
```

**Define explicit pairs from day one.** The cost of setting up intentions
correctly at the start is low. The cost of discovering during an audit or
incident that lateral movement was unrestricted is high.

Document intentional absences. If `reporting → backend` is absent from the
intention graph, a comment should state that this is deliberate. See the
full working example:
[`demos/06-multi-app-gateway/intentions.yaml`](../demos/06-multi-app-gateway/intentions.yaml).

**❌ Do not do this — wildcard intentions applied "just for the migration":**

```hcl
# ❌ DO NOT USE — applied once, never cleaned up
# Every service in the namespace can reach every other service.
# A compromised app has unrestricted lateral movement.
Kind   = "service-intentions"
Name   = "*"
Sources = [{ Name = "*", Action = "allow" }]
```

---

### Per-route resilience

### Timeout ladder

The single most common source of ambiguous gateway errors is a misaligned
timeout ladder. The correct ordering is:

```
client timeout  >  gateway request timeout  >  gateway backendRequest timeout  >  upstream timeout
```

If any tier is inverted, either the client cuts the connection before the
gateway can return a clean error, or the gateway times out before the upstream
has had a chance to respond.

```yaml
# Transactional / authentication routes — tight SLA, fail fast
timeouts:
  request: 10s          # gateway drops the client connection after 10s
  backendRequest: 8s    # gateway abandons the upstream call after 8s
# Set your OCP Route / load balancer client timeout to ~12s

# Reporting / analytics routes — tolerant SLA, long-running queries
timeouts:
  request: 90s
  backendRequest: 80s
# Set client timeout to ~100s
```

Set these **per `HTTPRoute`**, never as a gateway-wide default. A single
default will be wrong for at least one app in the namespace.

### Retry policy — idempotency is a hard requirement

Retries must be **opt-in per route**, not a gateway-wide default. On mutation
endpoints, retry amplification creates duplicate operations. A payment POST
retried twice is not a performance problem — it is a data integrity problem.

**Correct — `RouteRetryFilter` scoped only to idempotent routes:**

```yaml
# File: demos/06-multi-app-gateway/rate-limit-filters.yaml
# Only attach this filter to routes where ALL operations are idempotent (GET, HEAD).
# Never attach to routes accepting POST, PUT, PATCH, or DELETE.
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: reporting-retry
  namespace: control-network-traffic
spec:
  numRetries: 2
  retryOn:
    - "5xx"
    - "reset"
    - "connect-failure"
```

The classification of each route as idempotent or non-idempotent must be
documented in a comment in the config file. Do not rely on tribal knowledge.

**❌ Do not do this — blanket retry policy via `ServiceDefaults` wildcard:**

```hcl
# ❌ DO NOT USE — wildcard applies to every upstream including payment services
# Silent duplicate transactions are the failure mode, not a visible error.
Kind = "service-defaults"
Name = "*"
UpstreamConfig {
  Defaults {
    Limits { MaxRetries = 3 }
  }
}
```

### Circuit breaking — per upstream, not per gateway

Configure per-upstream circuit breakers via `ServiceDefaults`. This ensures
that one app's upstream failure exhausts only that app's connection budget —
not the shared Envoy resources of the gateway, which would degrade all apps.

```hcl
# File: consul/config-entries/service-defaults-backend.yaml
# Apply with: consul config write consul/config-entries/service-defaults-backend.yaml
# Repeat this resource for each upstream service — do not use Name = "*"
Kind     = "service-defaults"
Name     = "backend"
Protocol = "http"

UpstreamConfig {
  Defaults {
    PassiveHealthCheck {
      Interval           = "10s"
      MaxFailures        = 3       # eject after 3 consecutive 5xx responses
      MaxEjectionPercent = 100     # allow ejecting all hosts if all are failing
      BaseEjectionTime   = "30s"   # doubles on each consecutive ejection event
    }
  }
}
```

Tune `MaxFailures` and `BaseEjectionTime` per service based on observed error
budget. A low-tolerance auth service should eject faster than a batch reporting
service where occasional 5xx is expected. See the working example in
[`consul/config-entries/service-defaults-backend.yaml`](../consul/config-entries/service-defaults-backend.yaml).

### Health checking strategy

Consul configures passive health checking (outlier detection) via the
`PassiveHealthCheck` block in `ServiceDefaults` (shown above). Use this as
the **primary mechanism** for services with variable error rates — it reflects
actual request outcomes, not synthetic probe results.

Active health checks (periodic probes via `consul.hashicorp.com/connect-service`
readiness probes on the pod) are appropriate for detecting cold start failures
but do not reflect per-request quality. Use both together: active checks detect
a pod that is not ready, passive checks detect a pod that is returning errors.

For services with flappy health states (notification dispatch, async job
runners, external integration adapters) increase `Interval` and `MaxFailures`
to prevent premature ejection while still catching genuine sustained failures:

```hcl
# File: consul/config-entries/service-defaults-<service-name>.yaml
# Tolerant settings for a service with expected transient errors
Kind     = "service-defaults"
Name     = "notification-service"
Protocol = "http"

UpstreamConfig {
  Defaults {
    PassiveHealthCheck {
      Interval           = "30s"   # evaluate less frequently
      MaxFailures        = 10      # tolerate more transient errors before ejecting
      MaxEjectionPercent = 50      # never eject more than half the pool
      BaseEjectionTime   = "60s"
    }
  }
}
```

### Connection pool tuning by workload type

Connection pool limits are set in `ServiceDefaults` under
`UpstreamConfig.Defaults.Limits`. Tune per service — not globally via a
wildcard `ServiceDefaults` — to prevent one app's upstream from monopolising
the gateway's Envoy thread resources.

| Workload type | `MaxConnections` | `MaxPendingRequests` | `MaxRequests` | Rationale |
|---------------|------------------|----------------------|---------------|-----------|
| Auth / identity | Low (50–100) | Low | Low | Short-lived, high-frequency; connection reuse is high |
| Transactional API | Medium (200–500) | Medium | Medium | Moderate concurrency, latency-sensitive |
| Reporting / analytics | High (500–1000) | High | High | Long-lived connections, high payload, bursty |
| Notification / async | Medium | High | Medium | High concurrency, fire-and-forget |

```hcl
# File: consul/config-entries/service-defaults-<service-name>.yaml
Kind     = "service-defaults"
Name     = "reporting-backend"
Protocol = "http"

UpstreamConfig {
  Defaults {
    Limits {
      MaxConnections    = 800
      MaxPendingRequests = 400
      MaxRequests       = 800
    }
  }
}
```

---

### Per-route rate limiting

Apply rate limits at the route level — not as an aggregate gateway-level limit.
An aggregate limit is shared across all apps; one app's burst exhausts the
entire budget and forces 429s onto its neighbors.

**Correct — independent `RouteRetryFilter` per `HTTPRoute`**

Each route gets its own rate limit budget. A reporting burst hits the
reporting ceiling; the transactional route's budget is unaffected.

```yaml
# File: demos/06-multi-app-gateway/rate-limit-filters.yaml
# Apply with: kubectl apply -f demos/06-multi-app-gateway/rate-limit-filters.yaml

# Transactional route — higher sustained rate, customer-facing
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: api-ratelimit
  namespace: control-network-traffic
spec:
  rateLimit:
    requestsPerUnit: 400
    unit: MINUTE
---
# Reporting route — lower frequency, bursty; capped independently
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: reporting-ratelimit
  namespace: control-network-traffic
spec:
  rateLimit:
    requestsPerUnit: 50
    unit: MINUTE
```

Each `RouteRetryFilter` is then referenced from the `filters` field of the
corresponding `HTTPRoute` rule. See
[`demos/06-multi-app-gateway/rate-limit-filters.yaml`](../demos/06-multi-app-gateway/rate-limit-filters.yaml)
for the full working example including the `kubectl patch` commands to attach
filters to existing routes.

**Do not do this — aggregate gateway-level limit shared across all routes:**

```yaml
# ❌ DO NOT USE — one budget shared across all routes
# If reporting fires 450 req/min against a 500 req/min gateway limit,
# only 50 req/min remains for all transactional routes.
# The transactional app receives 429s. The reporting job does not.
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: gateway-ratelimit   # attached to Gateway, not to individual HTTPRoutes
spec:
  rateLimit:
    requestsPerUnit: 500
    unit: MINUTE
```

Additional rate limiting considerations:

- **Scope limits by client identity, not just by source IP.** IP-based limits
  are ineffective when internal batch jobs or service accounts share an egress
  IP. Use service identity (mTLS principal) where available.
- **Apply different limits for read vs. write operations** on the same route
  if the downstream service has asymmetric capacity for reads and writes.
- **Do not apply CORS `allowOrigins: ["*"]`** on any listener serving
  sensitive APIs. Configure explicit origin allowlists per route.

---

### Header policy

Both header filters below are added to the `filters` field of an `HTTPRoute`
rule, inside `consul/config-entries/api-gateway.yaml`.

### Strip internal headers before external responses

Internal tracing and routing headers must not be forwarded to external clients.
Add a `ResponseHeaderModifier` filter to each `HTTPRoute` that handles
external-facing traffic.

```yaml
# File: consul/config-entries/api-gateway.yaml — inside HTTPRoute spec.rules[]
rules:
  - matches:
      - path:
          type: PathPrefix
          value: /api/v1
    backendRefs:
      - name: frontend
        port: 8080
    filters:
      - type: ResponseHeaderModifier
        responseHeaderModifier:
          remove:
            - x-envoy-upstream-service-time
            - x-request-id        # internal correlation ID — do not expose externally
            - x-b3-traceid        # Zipkin tracing header
            - x-b3-spanid
            - x-b3-parentspanid
            - x-b3-sampled
            - x-b3-flags
```

### Inject tracing headers at the gateway

The gateway is the first mesh-aware hop for external traffic. Inject a W3C
`traceparent` header on the request so every downstream service in the call
chain can participate in distributed tracing from the point of ingress.

```yaml
# File: consul/config-entries/api-gateway.yaml — inside HTTPRoute spec.rules[]
# Add alongside the ResponseHeaderModifier above on the same route rule.
    filters:
      - type: RequestHeaderModifier
        requestHeaderModifier:
          set:
            # set (not add): preserve an existing traceparent if the caller
            # already provided one; inject a new one if absent.
            - name: traceparent
              value: "%REQ(traceparent)%"
```

A request that enters the mesh without a `traceparent` header cannot be
correlated across services downstream. Do not skip this on any route.

---

### Observability

The shared gateway model loses per-app observability granularity that existed
for free in the 1:1 model. Rebuild it explicitly.

### Envoy stats tags — tag by route and upstream, not just gateway instance

Stats tags are set in the `config` block of a `ProxyDefaults` config entry.
Without them, a spike in gateway latency is unattributable across the N apps
sharing the namespace.

```hcl
# File: consul/config-entries/proxy-defaults.yaml
# Apply with: consul config write consul/config-entries/proxy-defaults.yaml
Kind = "proxy-defaults"
Name = "global"

Config {
  envoy_stats_tags = [
    "consul_source_service",
    "consul_destination_service",
    "consul_destination_namespace",
    "consul_routing_cluster"
  ]
}
```

### Structured JSON access logs — minimum required fields

Access log format is configured in the `config` block of `ProxyDefaults`
alongside stats tags, or in the Consul API Gateway controller Helm values.
The format below uses Envoy command operators.

```hcl
# File: consul/config-entries/proxy-defaults.yaml — add to the Config block
Config {
  # ... stats tags above ...
  envoy_gateway_stats_prefix = "api_gateway"
  envoy_tracing_json = "{ ... }"   # optional — configure your tracing provider

  # Access log format — structured JSON with per-app attribution fields
  envoy_extra_static_clusters_json = ""   # placeholder if needed
}
```

Minimum required fields in the access log JSON template:

| Field | Envoy operator | Purpose |
|-------|---------------|---------|
| `method` | `%REQ(:METHOD)%` | HTTP verb |
| `path` | `%REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%` | Request path |
| `response_code` | `%RESPONSE_CODE%` | HTTP status returned to client |
| `duration_ms` | `%DURATION%` | Total request duration |
| `upstream_cluster` | `%UPSTREAM_CLUSTER%` | **Identifies which app handled the request** |
| `route_name` | `%ROUTE_NAME%` | **Identifies which HTTPRoute matched** |
| `upstream_host` | `%UPSTREAM_HOST%` | Specific pod that handled the request |
| `bytes_sent` | `%BYTES_SENT%` | Response payload size |
| `bytes_received` | `%BYTES_RECEIVED%` | Request payload size |
| `request_id` | `%REQ(X-REQUEST-ID)%` | Correlation ID for incident investigation |
| `forwarded_for` | `%REQ(X-FORWARDED-FOR)%` | Original client IP for audit trail |
| `user_agent` | `%REQ(USER-AGENT)%` | Caller identification |

`upstream_cluster` and `route_name` are the two fields that restore per-app
visibility on a shared gateway. Every alert, dashboard, and runbook should
filter on these fields first.

`request_id` and `forwarded_for` are required for incident investigation and
audit trail on any route handling sensitive operations.

### Distributed tracing

Tracing header injection is configured in the `RequestHeaderModifier` filter
on each `HTTPRoute` (see [Header policy](#header-policy) above). Verify
end-to-end propagation by confirming that every upstream service reads and
forwards the `traceparent` header on outbound calls.

A broken propagation chain at any single hop produces incomplete traces. The
symptom is a trace that appears to terminate at the service that dropped the
header — the downstream call chain is invisible.

---

### Sizing and scaling

With multiple apps per namespace, the gateway cannot be sized off any single
app's historical baseline.

### Re-baseline HPA thresholds against aggregate traffic

```
aggregate_peak_rps = sum of peak RPS across all apps in the namespace
```

Before setting this number, verify whether peaks overlap:

| App type | Typical peak window |
|----------|---------------------|
| Customer-facing transactional | Business hours (09:00–17:00) |
| Batch reporting / analytics | Off-hours (22:00–06:00) |
| Internal back-office APIs | Business hours |
| Notification / async dispatch | Event-driven, unpredictable |

If reporting peaks off-hours while transactional peaks during business hours,
the aggregate peak is **not additive** — size against the larger of the two
single-app peaks plus a headroom margin, not the sum. If peaks overlap,
the sum applies.

### Account for xDS config payload growth

Each additional app in the namespace adds listeners, clusters, and endpoints
to the gateway's xDS config. This has two implications:

1. **Gateway pod memory** — budget proportionally more memory per additional
   app. Monitor `envoy_server_memory_allocated` on the gateway pod.
2. **Consul server control plane load** — wider xDS watch subscriptions from
   a larger gateway config increase the volume of xDS updates the Consul
   servers must compute and push. Monitor Consul server CPU and xDS stream
   metrics when adding apps.

### HPA configuration

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: api-gateway
  namespace: control-network-traffic
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api-gateway
  minReplicas: 2          # never below 2; single replica = single point of failure
  maxReplicas: 10
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 60   # scale before saturation, not at it
```

`minReplicas: 1` is never acceptable for a gateway serving multiple apps.
A single-replica gateway is a single point of failure for every app in the
namespace simultaneously.

---

### Config ownership

Multiple app teams now write config into the same namespace. Without explicit
ownership conventions, teams collide on shared resources and config provenance
becomes opaque.

### RBAC split: platform owns the gateway; app teams own their routes

```
Gateway (GatewayClass, Gateway)  →  platform team
HTTPRoute for app A              →  app A team
HTTPRoute for app B              →  app B team
ServiceIntentions                →  platform team (app teams request via PR)
TLS certificates (VaultPKISecret)→  security / platform team
```

This split reflects the blast radius: a misconfigured `Gateway` affects all
apps; a misconfigured `HTTPRoute` affects only the owning app.

### CODEOWNERS

```
# Gateway and TLS — platform team owns
consul/config-entries/api-gateway.yaml          @platform-team
demos/*/intentions.yaml                         @platform-team

# Per-app routes — owned by respective app teams
demos/06-multi-app-gateway/reporting-*.yaml     @reporting-team
```

### GitOps conventions

- All Gateway API resources (`GatewayClass`, `Gateway`, `HTTPRoute`) are
  versioned GitOps artifacts. No ad-hoc `kubectl apply` in production.
- Use `ReferenceGrant` when any route or backend reference crosses a namespace
  boundary — cross-namespace trust must be explicit, not implicit.
- Name routes after the app they serve: `api-route`, `reporting-route`.
  Opaque names like `route-1` make CODEOWNERS and audit trail matching
  impossible.

---

### Namespace grouping criteria

Not all apps belong in the same namespace. Grouping by business domain alone
is insufficient — the gateway enforces a shared operational and security
boundary across everything in the namespace.

| Criterion | Why it matters for the gateway |
|-----------|-------------------------------|
| **Traffic profile** | Timeout, retry, and circuit breaker policies can be standardized within the group |
| **Compliance scope** | A regulated app grouped with non-regulated apps can expand audit scope to its neighbors |
| **Blast radius tolerance** | A degraded gateway degrades all apps in the namespace equally |
| **Rate limit tier** | Apps with similar client profiles can share per-route limit budgets |
| **Certificate / trust lifecycle** | Co-located apps should have independent, non-overlapping cert rotation schedules |
| **Peak traffic timing** | Determines whether aggregate sizing is additive or time-separated |
| **Incident response ownership** | Apps with different on-call teams behind one gateway complicate incident triage |

Before grouping apps, explicitly answer: if this gateway becomes unavailable,
who is affected, and do all those affected teams have the same availability
requirement? If not, reconsider the grouping.

---

## Anti-patterns

Each entry follows the same structure: **What it looks like** (the config as
it appears in practice) → **What breaks** (the specific failure mode) →
**Correct pattern** (the deployable fix with the specific Consul resource).

### Anti-pattern index

| # | Anti-pattern | Symptom that surfaces it |
|---|-------------|--------------------------|
| [1](#1--wildcard-intentions-just-for-the-migration) | Wildcard intentions | Unrestricted lateral movement; fails security audit |
| [2](#2--single-catch-all-route-serving-multiple-apps) | Single catch-all route | No per-app policy; misrouting; meaningless access logs |
| [3](#3--shared-tls-certificate-across-unrelated-apps) | Shared TLS certificate | Coordinated multi-team outage on every cert event |
| [4](#4--blanket-timeoutretry-policy-applied-gateway-wide) | Blanket timeout/retry policy | Silent duplicate transactions; auth timeouts 45× too long |
| [5](#5--aggregate-only-rate-limiting) | Aggregate-only rate limiting | Well-behaved apps get 429s during a neighbor's burst |
| [6](#6--using-the-api-gateway-for-east-west-traffic) | Gateway used for east-west | Latency, choke point, intention bypass, broken observability |
| [7](#7--mixing-compliance-scopes-without-a-deliberate-decision) | Mixed compliance scopes | Unregulated apps pulled into audit scope |
| [8](#8--under-sizing-the-shared-gateway) | Under-sized gateway | All apps in namespace go offline simultaneously under load |
| [9](#9--no-clear-config-ownership) | No config ownership | Silent cross-team config changes; untraceable incidents |
| [10](#10--route-sprawl-as-a-false-isolation-mechanism) | Route sprawl | Operational overhead without real isolation |
| [11](#11--retrying-non-idempotent-operations) | Retrying non-idempotent ops | Silent duplicate operations; surfaces during reconciliation |

---

### 1 — Wildcard intentions "just for the migration"

**What it looks like:**

```hcl
# ServiceIntentions applied namespace-wide during migration
Kind = "service-intentions"
Name = "*"
Sources = [{ Name = "*", Action = "allow" }]
```

**What breaks:** Every service in the namespace can reach every other service
unconditionally. A compromised or misconfigured app has unrestricted lateral
movement. This config is applied once, never revisited, and survives long after
the migration is complete.

**Correct pattern:** One `ServiceIntentions` resource per destination service,
with explicit source entries for each permitted caller. Absent pairs are denied
by Consul's default-deny model — document them as deliberate in a comment.

```hcl
# consul/config-entries/intentions.yaml
Kind = "service-intentions"
Name = "backend"
Sources = [
  {
    Name   = "api"
    Action = "allow"
  },
  {
    # Explicit denial — reporting is not permitted to call backend directly.
    # Cross-app calls must go through the api service.
    Name   = "reporting"
    Action = "deny"
  }
]
```

Reference: [`demos/06-multi-app-gateway/intentions.yaml`](../demos/06-multi-app-gateway/intentions.yaml)
| [Consul ServiceIntentions config entry](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-intentions)

---

### 2 — Single catch-all route serving multiple apps

**What it looks like:**

```yaml
# One HTTPRoute, one backend — all apps behind a shared dispatcher
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: catch-all
spec:
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /        # matches everything
      backendRefs:
        - name: router-service
          port: 8080        # this service inspects the path and re-routes internally
```

**What breaks:** Per-route policy (timeouts, retries, rate limits) cannot be
applied at the gateway — it all inherits from the single route. `route_name`
in access logs is meaningless for attribution. Intentions cannot be enforced
per-app at the gateway boundary. One misconfigured backend takes all traffic
with it.

**Correct pattern:** One `HTTPRoute` per app, bound to an explicit hostname.
Per-route timeouts, retry policy, and rate limit filters then apply
independently to each app.

```yaml
# HTTPRoute for the transactional app
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: api-route
spec:
  hostnames: ["api.example.internal"]
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /api/v1
      backendRefs:
        - name: frontend
          port: 8080
      timeouts:
        request: 10s
        backendRequest: 8s
---
# HTTPRoute for the reporting app — independent timeout, independent rate limit
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: reporting-route
spec:
  hostnames: ["reporting.example.internal"]
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /reports
      backendRefs:
        - name: reporting
          port: 8080
      timeouts:
        request: 90s
        backendRequest: 80s
```

Reference: [`consul/config-entries/api-gateway.yaml`](../consul/config-entries/api-gateway.yaml)
| [Consul HTTPRoute configuration](https://developer.hashicorp.com/consul/docs/north-south/api-gateway)

---

### 3 — Shared TLS certificate across unrelated apps

**What it looks like:**

```yaml
# Single certificateRef for all apps on the gateway
tls:
  mode: Terminate
  certificateRefs:
    - name: shared-wildcard-cert   # *.example.internal covers all apps
```

**What breaks:** Any cert lifecycle event — rotation, revocation, expiry,
OCSP stapling update — forces a simultaneous change across every app sharing
that cert. A security incident requiring emergency revocation on one app
becomes a coordinated multi-team maintenance window.

**Correct pattern:** One `VaultPKISecret` and one `certificateRef` per
hostname. Each app's cert is issued, rotated, and revoked independently via
the Vault Secrets Operator.

```yaml
# Gateway listener — per-hostname certificateRefs
tls:
  mode: Terminate
  certificateRefs:
    - name: api-tls-cert          # VaultPKISecret for api.example.internal
    - name: reporting-tls-cert    # VaultPKISecret for reporting.example.internal
---
# VaultPKISecret for each hostname — independent TTL and rotation schedule
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultPKISecret
metadata:
  name: api-tls-cert
  namespace: control-network-traffic
spec:
  mount: pki
  role: <your-pki-role>
  commonName: api.example.internal
  destination:
    name: api-tls-cert
    create: true
    type: kubernetes.io/tls
  ttl: 90d
  expiryOffset: 30d    # VSO renews 30 days before expiry, no gateway restart needed
```

Reference: [Vault Secrets Operator — VaultPKISecret](https://developer.hashicorp.com/vault/docs/platform/k8s/vso/api-reference#vaultpkisecret)
| [Demo 05 Step 1](../demos/05-api-gateway-ingress/README.md)

---

### 4 — Blanket timeout/retry policy applied gateway-wide

**What it looks like:**

```yaml
# ProxyDefaults sets a global request timeout inherited by all routes
Kind = "proxy-defaults"
Name = "global"
Config {
  local_request_timeout_ms = 30000   # 30s for everything — satisfies reporting,
                                     # but auth and transactions should be 2–10s
}
```

```yaml
# Global retry policy in service-defaults — applies to every upstream
Kind = "service-defaults"
Name = "*"              # wildcard — all services
UpstreamConfig {
  Defaults {
    Limits { MaxRetries = 3 }   # retries POST /payments 3 times on 5xx
  }
}
```

**What breaks:** A 30s global timeout means auth failures are invisible for
30 seconds before the client sees an error. A global retry policy on a
wildcard `service-defaults` retries `POST /payments` on every 5xx — producing
silent duplicate transactions.

**Correct pattern:** Set `timeouts` on each `HTTPRoute` individually. Scope
retry config to specific upstreams via named (not wildcard) `ServiceDefaults`,
and only on services where all operations are idempotent.

```yaml
# Per-route timeout on the HTTPRoute — not in proxy-defaults
rules:
  - timeouts:
      request: 5s            # auth route: fail fast
      backendRequest: 4s

# Per-upstream retry in ServiceDefaults — named, not wildcard
Kind = "service-defaults"
Name = "reporting-backend"   # explicit service name, not "*"
Protocol = "http"
UpstreamConfig {
  Defaults {
    Limits { MaxRetries = 2 }   # safe: reporting endpoints are all GET
  }
}
# No retry config on payment-service ServiceDefaults — absence is intentional
```

Reference: [Consul ServiceDefaults — UpstreamConfig](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-defaults)
| [Consul ProxyDefaults](https://developer.hashicorp.com/consul/docs/connect/config-entries/proxy-defaults)

---

### 5 — Aggregate-only rate limiting

**What it looks like:**

```yaml
# One RateLimitFilter attached to the Gateway — shared across all routes
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: gateway-ratelimit
spec:
  rateLimit:
    requestsPerUnit: 500
    unit: MINUTE
# Applied to the Gateway, not to individual HTTPRoutes
```

**What breaks:** The 500 req/min budget is shared. A reporting batch job
that fires 450 req/min consumes 90% of the budget, leaving 50 req/min for
all transactional routes — well below their normal operating rate. The
transactional app gets 429s. The reporting job does not.

**Correct pattern:** Attach a `RouteRetryFilter` (or equivalent rate limit
filter) to each `HTTPRoute` independently with a budget appropriate to that
route's traffic profile.

```yaml
# Per-route rate limit on the transactional HTTPRoute
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: api-ratelimit
  namespace: control-network-traffic
spec:
  rateLimit:
    requestsPerUnit: 400    # transactional: higher sustained rate
    unit: MINUTE
---
# Per-route rate limit on the reporting HTTPRoute
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: reporting-ratelimit
  namespace: control-network-traffic
spec:
  rateLimit:
    requestsPerUnit: 50     # reporting: lower frequency, bursty — capped independently
    unit: MINUTE
```

Reference: [`demos/06-multi-app-gateway/rate-limit-filters.yaml`](../demos/06-multi-app-gateway/rate-limit-filters.yaml)
| [Consul API Gateway — Route filters](https://developer.hashicorp.com/consul/docs/north-south/api-gateway)

---

### 6 — Using the API Gateway for east-west traffic

**What it looks like:**

```yaml
# reporting calls backend by routing out through the gateway and back in
# BACKEND_URL set to the external gateway address instead of a mesh upstream
env:
  - name: BACKEND_URL
    value: "https://api.example.internal/internal/backend"  # exits mesh → gateway → re-enters
```

**What breaks:** The request leaves the mesh, traverses the gateway's TLS
termination and L7 policy stack, and re-enters — adding latency, consuming
gateway connection budget, and routing the call through north-south policy
(rate limits, timeouts) designed for external traffic. Consul's east-west
observability (sidecar metrics, intention audit) is bypassed entirely.

**Correct pattern:** East-west calls stay in the mesh via Envoy upstream
proxies declared in the pod annotation. Intentions govern access; sidecars
handle mTLS.

```yaml
# Pod annotation — declare the upstream so Consul injects the local proxy port
annotations:
  consul.hashicorp.com/connect-service-upstreams: "backend:8081"

# Application calls the local upstream proxy, not the gateway
env:
  - name: BACKEND_URL
    value: "http://127.0.0.1:8081"   # Envoy sidecar handles mTLS to backend
```

The corresponding `ServiceIntentions` entry for `reporting → backend` governs
whether this call is permitted — independently of what the gateway allows.

Reference: [`consul/config-entries/service-defaults-backend.yaml`](../consul/config-entries/service-defaults-backend.yaml)
| [Consul connect-service-upstreams annotation](https://developer.hashicorp.com/consul/docs/k8s/annotations-and-labels)

---

### 7 — Mixing compliance scopes without a deliberate decision

**What it looks like:**

```yaml
# Namespace contains both a PCI-scoped payment service and an unregulated
# internal reporting tool, behind the same Gateway with a shared listener
listeners:
  - name: https
    port: 8443
    tls:
      certificateRefs:
        - name: payment-tls-cert    # PCI-scoped
        - name: reporting-tls-cert  # unregulated
# Both apps share the same gateway process, listener config, and access logs
```

**What breaks:** Auditors assess the scope of shared infrastructure, not
individual applications in isolation. A gateway that terminates TLS for a
PCI-scoped app and an unregulated app simultaneously can bring the unregulated
app — and its team, its change process, its logs — into PCI audit scope.

**Correct pattern:** Compliance-scoped apps belong in a dedicated namespace
with their own gateway. If co-location is genuinely required, document the
scoping decision explicitly and confirm with your compliance team before
deployment — not after an audit finding.

```yaml
# Separate namespace and gateway for PCI-scoped services
# Namespace: payments-namespace  →  Gateway: payments-gateway
# Namespace: internal-namespace  →  Gateway: internal-gateway

# If co-location is required, document the decision:
# compliance-scope-decision.md — records the deliberate choice to co-locate,
# the compliance team sign-off, and the compensating controls in place.
```

Reference: [Consul Enterprise namespaces](https://developer.hashicorp.com/consul/docs/enterprise/namespaces)
| [Namespace grouping criteria](#namespace-grouping-criteria) (this document)

---

### 8 — Under-sizing the shared gateway

**What it looks like:**

```yaml
# HPA copied from the old single-app deployment without adjustment
spec:
  minReplicas: 1      # single replica — was fine for one app, not for N
  maxReplicas: 3      # ceiling sized against one app's historical peak
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 80   # scaling triggers at saturation, not before it
```

**What breaks:** `minReplicas: 1` means a single gateway pod serves all apps
in the namespace — one pod failure takes everything offline simultaneously.
`maxReplicas: 3` sized against one app's peak is insufficient when N apps run
concurrently. A CPU target of 80% means scaling is reactive; by the time a
new pod is ready, the existing pod is already dropping requests.

**Correct pattern:** Re-baseline HPA thresholds against aggregate traffic.
Set `minReplicas: 2` as the floor. Scale at 60% CPU utilization so new pods
are provisioned before saturation, not during it. Size `maxReplicas` against
the sum of all apps' peak RPS (or the largest single-app peak if peaks do not
overlap — see [Sizing and scaling](#sizing-and-scaling)).

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: api-gateway
  namespace: control-network-traffic
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api-gateway
  minReplicas: 2          # floor: never a single point of failure
  maxReplicas: 10         # ceiling: sized against aggregate peak across all apps
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 60   # proactive: scale before saturation
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: 70   # memory grows with xDS config as apps are added
```

Also set explicit resource requests and limits on the gateway pod to give the
scheduler accurate placement data and prevent the gateway from being evicted
under node memory pressure:

```yaml
# In the gateway Deployment podSpec
resources:
  requests:
    cpu: "500m"       # baseline per replica — adjust from observed p50 usage
    memory: "256Mi"   # baseline; add ~50Mi per additional app in the namespace
  limits:
    cpu: "2000m"
    memory: "1Gi"
```

Monitor `envoy_server_memory_allocated` on the gateway pod and
`consul.runtime.sys_bytes` on Consul servers as apps are added to the
namespace. Both will grow with each new app's xDS config contribution.

Reference: [Kubernetes HPA documentation](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/)
| [Sizing and scaling](#sizing-and-scaling) (this document)

---

### 9 — No clear config ownership

**What it looks like:**

```yaml
# HTTPRoute with no owner signal — any team can modify it
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: route-1          # opaque name; no team attribution
  namespace: control-network-traffic
  # No labels, no annotations, no CODEOWNERS entry
```

**What breaks:** Without explicit ownership, any team can modify any route.
A timeout change intended for the reporting route accidentally modifies the
transactional route. A `ServiceIntentions` update opens a path the owning
team didn't intend. During an incident, `git log` shows "update config" with
no way to determine which team made a change or why.

**Correct pattern:** Name routes after the app they serve. Use `CODEOWNERS`
to enforce PR review by the owning team. Apply labels for programmatic
ownership queries.

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: reporting-route          # name identifies the owning app unambiguously
  namespace: control-network-traffic
  labels:
    app.kubernetes.io/name: reporting
    app.kubernetes.io/managed-by: reporting-team
  annotations:
    consul.example.internal/owner: "reporting-team"
    consul.example.internal/oncall: "reporting-oncall"
```

```
# .github/CODEOWNERS
# Gateway and shared infra — platform team approval required
consul/config-entries/api-gateway.yaml             @platform-team
demos/*/intentions.yaml                            @platform-team

# Per-app routes — approval by the owning app team
**/reporting-route.yaml                            @reporting-team
**/api-route.yaml                                  @api-team
```

Reference: [GitHub CODEOWNERS syntax](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
| [Config ownership](#config-ownership) (this document)

---

### 10 — Route sprawl as a false isolation mechanism

**What it looks like:**

```yaml
# One listener per app to simulate old 1:1 isolation
listeners:
  - name: app-a-listener
    port: 8443
    tls:
      certificateRefs: [{name: app-a-cert}]
  - name: app-b-listener
    port: 8444
    tls:
      certificateRefs: [{name: app-b-cert}]
  - name: app-c-listener
    port: 8445
    tls:
      certificateRefs: [{name: app-c-cert}]
  # ... one more per app added to the namespace
```

**What breaks:** Each additional listener adds a TLS context, a port to
secure at the network layer, and a separate access log stream to monitor.
The isolation this provides is superficial — all listeners still run in the
same gateway process and share the same Envoy worker threads. Actual isolation
comes from intentions and per-route policy, not from port separation.

**Correct pattern:** One HTTPS listener on one port. Use hostname-based
`HTTPRoute` separation for routing isolation, `ServiceIntentions` for trust
isolation, and per-route filters for policy isolation. Add additional listeners
only when protocol diversity requires it (e.g., a separate TCP listener for
a non-HTTP legacy service).

```yaml
# One HTTPS listener serves all apps via SNI hostname routing
listeners:
  - name: https
    protocol: HTTPS
    port: 8443
    tls:
      mode: Terminate
      certificateRefs:
        - name: app-a-cert    # SNI routes to the correct cert automatically
        - name: app-b-cert
        - name: app-c-cert
# Apps are isolated by HTTPRoute hostname, intentions, and per-route policy —
# not by port. The listener count reflects protocol diversity, not app count.
```

Reference: [`consul/config-entries/api-gateway.yaml`](../consul/config-entries/api-gateway.yaml)
| [Consul Gateway listener configuration](https://developer.hashicorp.com/consul/docs/north-south/api-gateway)

---

### 11 — Retrying non-idempotent operations

**What it looks like:**

```yaml
# HTTPRoute with a retry filter applied — no idempotency check
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: payment-retry
spec:
  numRetries: 3
  retryOn:
    - "5xx"
    - "reset"
# Attached to the payments HTTPRoute — which accepts POST /payments
```

**What breaks:** A `POST /payments` that returns 500 after the upstream has
already initiated the transaction gets retried up to 3 times. Each retry may
produce an additional transaction. The failure mode is not an error visible
in logs — it is a silent duplicate that surfaces later during reconciliation.
At the gateway layer there is no visibility into whether the upstream
processed the request before the 500 was returned.

**Correct pattern:** Retries are opt-in per route, scoped only to routes
where every operation is idempotent. Non-idempotent routes must have retries
explicitly absent or explicitly disabled. Document the classification.

```yaml
# Reporting route — retries safe, all operations are GET
apiVersion: consul.hashicorp.com/v1alpha1
kind: RouteRetryFilter
metadata:
  name: reporting-retry
spec:
  numRetries: 2
  retryOn: ["5xx", "reset", "connect-failure"]
# Only attached to reporting-route HTTPRoute — not to payment-route

---
# Payment route — no RouteRetryFilter attached
# Absence is intentional: POST /payments is non-idempotent.
# If a retry filter is ever proposed for this route, it requires
# explicit sign-off confirming idempotency guarantees are in place upstream.
```

Reference: [Consul API Gateway — RouteRetryFilter](https://developer.hashicorp.com/consul/docs/north-south/api-gateway)
| [Per-route resilience — Retry policy](#per-route-resilience) (this document)
