# API Gateway Guidance

Practitioner reference for Consul Enterprise API Gateway configuration in a
shared, multi-app namespace. Use `Ctrl+F` during config review. For runnable
examples, see [Demo 05](../demos/05-api-gateway-ingress/README.md) and
[Demo 06](../demos/06-multi-app-gateway/README.md).

---

## The architectural shift

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

## Routing design

### Use hostname-based separation, not path-based fan-out

Hostname-based routing gives each app an independent certificate lifecycle,
independent route ownership, and a clean audit boundary. Path-based routing
on a shared hostname couples all of these and creates ambiguity about which
team owns which traffic.

```yaml
# Correct — one HTTPRoute per app, explicit hostname
# frontend app
hostnames: ["api.example.internal"]
# reporting app
hostnames: ["reporting.example.internal"]

# Anti-pattern — shared hostname, path-based fan-out
hostnames: ["*.example.internal"]   # ownership is ambiguous
rules:
  - matches: [{path: {value: /frontend}}]   # fragile, auditor-hostile
  - matches: [{path: {value: /reporting}}]  # cert lifecycle is coupled
```

Hostname separation also means:
- An auditor reviewing the `api.example.internal` listener config sees only
  that app's policy — nothing bleeds across from the reporting app
- A cert rotation event for one app is entirely isolated from the other
- RBAC on `HTTPRoute` objects maps cleanly to the team that owns the app

### Keep path matches narrow and explicit

A `PathPrefix: /` catch-all serving multiple apps is a misrouting risk. When
the match is ambiguous, Envoy's route evaluation order determines which app
receives the request — a property that is invisible at the Gateway API level
and easy to break with a config change.

```yaml
# Correct — narrow prefix per resource
rules:
  - matches:
      - path:
          type: PathPrefix
          value: /api/v1/accounts    # explicit, owned by accounts team

# Anti-pattern — catch-all that fans out downstream
rules:
  - matches:
      - path:
          type: PathPrefix
          value: /                   # everything lands here; routing becomes
                                     # an application-level concern, not a
                                     # gateway-level one
```

### Assign distinct listeners to distinct protocols

If any app uses gRPC, HTTP/2, or raw TCP alongside HTTP/1.1 apps, give each
protocol its own listener. Multiplexing mixed protocols on one listener creates
subtle framing bugs, complicates timeout configuration, and makes access log
analysis harder.

```yaml
listeners:
  - name: https         # HTTP/1.1 and HTTP/2 transactional apps
    protocol: HTTPS
    port: 8443
  - name: grpc          # gRPC internal services
    protocol: HTTPS     # gRPC runs over HTTP/2; declare separately
    port: 8444
  - name: tcp-legacy    # raw TCP for any non-HTTP legacy integration
    protocol: TCP
    port: 9000
```

### Restrict route binding to the local namespace

```yaml
allowedRoutes:
  namespaces:
    from: Same    # only HTTPRoutes in this namespace can bind
```

`from: All` or `from: Selector` without tight label matching allows other
namespaces to bind routes to your gateway — a lateral privilege escalation
vector. For cross-namespace references, use `ReferenceGrant` explicitly.

---

## TLS policy

### Per-hostname SNI certificates — never a shared cert

Each app's cert lifecycle (issuance, rotation, revocation) must be independent
even though the apps share a gateway process.

```yaml
# Correct — independent cert per hostname
tls:
  mode: Terminate
  certificateRefs:
    - name: api-tls-cert        # issued and rotated independently
    - name: reporting-tls-cert  # no blast radius overlap

# Anti-pattern — one cert for all apps
tls:
  certificateRefs:
    - name: shared-wildcard-cert   # one expiry or compromise event
                                   # forces simultaneous rotation for
                                   # every app on this gateway
```

If an app handles sensitive transactions, its certificate compromise should
not require a maintenance window for an unrelated reporting service on the
same gateway. Coupled cert lifecycles make that separation impossible.

### Enforce minimum TLS version and cipher policy

```yaml
# In the Consul API Gateway controller config or via GatewayTLSConfig
tlsMinVersion: "TLSv1_2"   # TLS 1.3 preferred where client support allows
tlsCipherSuites:
  - TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
  - TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
  - TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
  # Explicitly absent: RC4, 3DES, NULL, EXPORT, anon suites
```

Weak cipher suites should be rejected explicitly. Relying on a default
allowlist is not a sufficient control in environments subject to security audit.

### Use Vault Secrets Operator for certificate lifecycle on OpenShift

The gateway reads listener certs from standard Kubernetes `tls` Secrets. The
Vault Secrets Operator (VSO) is the recommended mechanism for keeping those
Secrets current from a Vault PKI backend — it handles issuance and automatic
rotation without manual `vault` CLI steps.

```yaml
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
    name: api-tls-cert
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

## Service intentions — namespace ≠ trust boundary

This is the most operationally significant change in the shared-gateway model.

With one app per namespace, the namespace boundary implicitly provided security
isolation. With multiple apps per namespace, that guarantee is gone. Every
service-to-service relationship must be explicitly permitted via intentions.

```hcl
# Correct — one intention per permitted service pair
Kind   = "service-intentions"
Name   = "backend"
Sources = [
  { Name = "api",         Action = "allow" },
  { Name = "reporting",   Action = "deny"  }  # explicit denial documented
]

# Anti-pattern — wildcard "just for the migration"
Kind   = "service-intentions"
Name   = "*"
Sources = [{ Name = "*", Action = "allow" }]
# This shortcut is applied once and never cleaned up.
# It silently removes all east-west security guarantees.
# A compromised app in the namespace can reach every other service.
```

**Define explicit pairs from day one.** The cost of setting up intentions
correctly at the start is low. The cost of discovering during an audit — or
an incident — that lateral movement was unrestricted is high.

Document intentional absences in the config. If `reporting → backend` is
absent from the intention graph, a comment should state that this is
deliberate, not an oversight. See
[`demos/06-multi-app-gateway/intentions.yaml`](../demos/06-multi-app-gateway/intentions.yaml).

---

## Per-route resilience

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

Retries must be **opt-in per route**, not a gateway-wide default. The reason
is not just performance — on mutation endpoints, retry amplification creates
duplicate operations. A payment POST retried twice is not a performance
problem, it is a data integrity problem.

```yaml
# Safe — retries scoped to read-only routes only
retries:
  numRetries: 2
  retryOn: ["5xx", "reset", "connect-failure"]
  # Applied only to routes backed by idempotent operations (GET, HEAD)
  # Never apply to routes that accept POST, PUT, PATCH, DELETE

# Anti-pattern — blanket retry policy at the gateway level
# Any global retry config that applies to all routes will eventually
# retry a non-idempotent mutation. The failure mode is silent duplication,
# not an error — making it significantly harder to detect than a hard failure.
```

The classification of routes as idempotent or non-idempotent should be
documented in the `HTTPRoute` metadata or in a comment in the config file.
Do not rely on tribal knowledge for this distinction.

### Circuit breaking — per upstream, not per gateway

Configure per-upstream circuit breakers via `ServiceDefaults`. This ensures
that one app's upstream failure exhausts only that app's connection budget —
not the shared Envoy resources of the gateway, which would degrade all apps.

```hcl
# service-defaults for each upstream independently
Kind     = "service-defaults"
Name     = "backend"        # repeated for each upstream service
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

Tune `MaxFailures` and `BaseEjectionTime` per service based on the service's
observed error budget. A low-tolerance auth service should eject faster than
a batch reporting service where occasional 5xx is expected.

### Health checking strategy

Use **passive health checking (outlier detection)** as the primary mechanism
for services with variable error rates. Active health checks (periodic probes)
are appropriate for detecting cold failures, but they do not reflect the
request-level quality that outlier detection observes.

For services known to have flappy health states — notification dispatch, async
job runners, external integration adapters — passive health checking prevents
premature ejection while still catching genuine sustained failures.

### Connection pool tuning by workload type

Connection pool limits prevent a single upstream from monopolising the
gateway's Envoy thread resources. Tune these per workload, not globally.

| Workload type | `maxConnections` | `maxPendingRequests` | `maxRequests` | Rationale |
|---------------|------------------|----------------------|---------------|-----------|
| Auth / identity | Low (50–100) | Low | Low | Short-lived, high-frequency; connection reuse is high |
| Transactional API | Medium (200–500) | Medium | Medium | Moderate concurrency, latency-sensitive |
| Reporting / analytics | High (500–1000) | High | High | Long-lived connections, high payload, bursty |
| Notification / async | Medium | High | Medium | High concurrency, fire-and-forget |

---

## Per-route rate limiting

Apply rate limits at the route level — not as an aggregate gateway-level limit.
An aggregate limit is shared across all apps; one app's burst exhausts the
entire budget and forces 429s onto its neighbors.

```yaml
# Correct — independent budget per route
# Transactional route: customer-facing, sustained rate
frontend-route:   200 req/min

# Reporting route: lower frequency, bursty
reporting-route:  20 req/min

# Anti-pattern — aggregate gateway limit shared across all routes
gateway-level: 220 req/min
# A reporting burst of 25 req/min would consume the entire budget,
# leaving only 195 req/min for the transactional app —
# below its normal operating rate.
```

Additional rate limiting considerations:

- **Scope limits by client identity, not just by source IP**, where the
  gateway can identify the caller. IP-based limits are ineffective when
  internal batch jobs or service accounts share an egress IP.
- **Apply different limits for read vs. write operations** on the same route
  if the downstream service has asymmetric capacity for reads and writes.
- **Do not apply CORS `allowOrigins: ["*"]`** on any listener serving
  sensitive APIs. Configure explicit origin allowlists.

---

## Header policy

### Strip internal headers before external responses

Internal tracing and routing headers must not be forwarded to external clients.

```yaml
# Add a response header filter to strip internal headers
filters:
  - type: ResponseHeaderModifier
    responseHeaderModifier:
      remove:
        - x-envoy-upstream-service-time
        - x-request-id           # internal correlation ID
        - x-b3-traceid           # internal tracing header
        - x-b3-spanid
        - x-b3-parentspanid
        - x-b3-sampled
        - x-b3-flags
```

### Inject tracing headers at the gateway

The gateway is the first mesh-aware hop for external traffic. Inject W3C
`traceparent` headers here so that every downstream service in the call chain
participates in distributed tracing from the point of ingress.

```yaml
filters:
  - type: RequestHeaderModifier
    requestHeaderModifier:
      add:
        - name: traceparent
          value: "%REQ(traceparent)%"  # preserve if already set; inject if absent
```

The gateway must not be a tracing black hole. A request that enters the mesh
without a `traceparent` header cannot be correlated across services downstream.

---

## Observability

The shared gateway model loses per-app observability granularity that existed
for free in the 1:1 model. Rebuild it explicitly.

### Envoy stats tags — tag by route and upstream, not just gateway instance

```yaml
# In Consul proxy-defaults or gateway config
config:
  envoy_stats_tags:
    - tag_name: consul_source_service
    - tag_name: consul_destination_service
    - tag_name: consul_destination_namespace
    - tag_name: consul_routing_cluster
```

Without these tags, a spike in gateway latency is unattributable — you cannot
tell which of the N apps in the namespace is responsible.

### Structured JSON access logs — minimum required fields

```
{
  "method":            "%REQ(:METHOD)%",
  "path":              "%REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%",
  "response_code":     "%RESPONSE_CODE%",
  "duration_ms":       "%DURATION%",
  "upstream_cluster":  "%UPSTREAM_CLUSTER%",
  "upstream_host":     "%UPSTREAM_HOST%",
  "route_name":        "%ROUTE_NAME%",
  "bytes_sent":        "%BYTES_SENT%",
  "bytes_received":    "%BYTES_RECEIVED%",
  "request_id":        "%REQ(X-REQUEST-ID)%",
  "user_agent":        "%REQ(USER-AGENT)%",
  "forwarded_for":     "%REQ(X-FORWARDED-FOR)%"
}
```

`route_name` and `upstream_cluster` are the two fields that restore per-app
visibility on a shared gateway. Every alert, dashboard, and runbook should
filter on these fields first.

`request_id` and `forwarded_for` are required for incident investigation and
audit trail purposes on any route handling sensitive operations.

### Distributed tracing

Inject tracing headers at ingress (see Header policy above) and verify that
all upstream services propagate them. A broken propagation chain at any hop
produces incomplete traces that are useless for root cause analysis.

---

## Sizing and scaling

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

## Config ownership

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

## Namespace grouping criteria

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

### 1 — Wildcard intentions "just for the migration"

```hcl
# This gets applied once and never cleaned up
Sources = [{ Name = "*", Action = "allow" }]
```

This is the most common migration shortcut. It feels temporary. It is
permanent. It silently removes all east-west security guarantees in the
namespace — a compromised app can reach every other service without
restriction. Define explicit pairs from day one.

---

### 2 — Single catch-all route serving multiple apps

```yaml
# One route, all apps behind it
rules:
  - matches:
      - path: {type: PathPrefix, value: /}
    backendRefs:
      - name: router-service   # routes internally by inspecting the request
```

The gateway's role is to route requests — delegating that decision to a
downstream service removes L7 policy, observability, and rate limiting from
the gateway layer entirely. Route ownership becomes invisible and misconfigured
intentions cannot be caught at the gateway boundary.

---

### 3 — Shared TLS certificate across unrelated apps

A single certificate shared between apps couples their rotation and revocation
events. A cert rotation that requires OCSP stapling update, or a revocation
triggered by a security incident on one app, forces a simultaneous change for
every app sharing that cert. On a shared gateway serving multiple teams, this
becomes a coordination problem with every cert event.

---

### 4 — Blanket timeout/retry policy applied gateway-wide

A global timeout set to satisfy the most tolerant app (reporting, 90s) causes
clients for the tightest SLA app (auth, 2s) to wait 45x longer than acceptable
before receiving an error. A global retry policy that satisfies the most
resilient app will retry mutations on every other app. Neither can be correct
simultaneously for all apps. Set per route.

---

### 5 — Aggregate-only rate limiting

A single gateway-level rate limit shared across all routes allows any one app
to consume the entire budget. The failure mode is not that the misbehaving app
gets rate-limited — it is that the well-behaved apps get 429s instead. The
noisy neighbor degrades silently while the visible symptom falls on its
neighbors.

---

### 6 — Using the API Gateway for east-west traffic

The API Gateway is a north-south ingress device. Internal service-to-service
calls between apps in the namespace must flow through the service mesh via
sidecar proxies governed by intentions. Routing east-west traffic back through
the gateway adds a round-trip, creates a choke point, breaks Consul's
observability model (east-west metrics disappear into gateway metrics), and
bypasses the intention enforcement that sidecars provide.

---

### 7 — Mixing compliance scopes without a deliberate decision

A compliance-scoped app grouped with non-regulated apps behind shared gateway
infrastructure can expand audit scope to its neighbors. This is not a
theoretical risk — auditors assess the scope of shared infrastructure, not
just the application itself. The decision to mix scopes must be explicit and
documented; discovering it during an audit is not acceptable.

---

### 8 — Under-sizing the shared gateway

Sizing gateway replicas against a single app's historical baseline and then
adding N more apps creates a single point of failure for all of them
simultaneously. The failure mode is not gradual degradation — it is a shared
gateway that falls over under combined peak load, taking every app in the
namespace offline at once.

---

### 9 — No clear config ownership

When multiple teams write config into the same namespace without CODEOWNERS,
naming conventions, and GitOps boundaries, the following failure modes occur:

- A team modifies an `HTTPRoute` they do not own, changing timeout policy for
  another app silently
- A `ServiceIntentions` change intended for one app accidentally opens a path
  to another
- An incident requires tracing which team last changed a config entry, and
  git history is the only record — but the commit message just says "update"

Establish ownership conventions before the second team touches the namespace.

---

### 10 — Route sprawl as a false isolation mechanism

Creating one listener or port per app to simulate the old 1:1 isolation model
defeats the operational purpose of consolidation and introduces new risks: more
listeners means more TLS contexts to manage, more ports to secure at the
network layer, and more config surface for misconfiguration. Use explicit
intentions and per-route policy to achieve isolation. The gateway's listener
count should reflect protocol diversity, not app count.

---

### 11 — Retrying non-idempotent operations

Retrying a failed POST, PATCH, or DELETE is not a resilience improvement — it
is a reliability hazard. A payment that fails with a 500 and is retried twice
may produce one, two, or three side effects depending on where in the request
lifecycle the failure occurred. At the gateway layer, there is no visibility
into whether the upstream processed the request before failing. If retries are
configured globally or without explicit idempotency consideration per route,
this failure mode is guaranteed to occur eventually.
