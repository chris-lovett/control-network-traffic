# API Gateway Guidance
#
# Reference document for tuning and operating Consul Enterprise API Gateways,
# particularly in the N-apps-per-namespace pattern used in this repo's
# Demo 05 and Demo 06.
#
# This is a practitioner reference — use Ctrl+F during config review.
# For narrative context, see Demo 05 and Demo 06 READMEs.

# API Gateway Guidance

Practitioner reference for Consul Enterprise API Gateway configuration.
Covers the shift from one-app-per-gateway to N-apps-per-namespace and what
that changes operationally.

Related demos:
- [Demo 05 — API Gateway Ingress](../demos/05-api-gateway-ingress/README.md)
- [Demo 06 — Multi-App Gateway](../demos/06-multi-app-gateway/README.md)

---

## The architectural shift

Previously: `1 namespace → 1 app → 1 gateway`
Now: `1 namespace → N apps → 1 shared gateway`

What was previously implicit — isolation, blast radius, trust boundary —
must now be made **explicit**. Every tuning decision below maps to one of
four concerns:

| Concern | Question to answer |
|---------|--------------------|
| **Trust** | Is every service-to-service relationship governed by an explicit intention? |
| **Resilience** | Are timeouts, retries, and circuit breakers set per-route for each app's SLA? |
| **Fairness** | Do per-route rate limits prevent any single app from degrading its neighbors? |
| **Capacity** | Is the gateway sized against aggregate traffic across all apps? |

---

## Routing design

**Use hostname-based routing, not path-based.**

Hostname separation (`api.demo.local` vs `reporting.demo.local`) gives each
app an independent cert lifecycle, independent route ownership, and a clean
audit boundary. Path-based routing on a shared hostname couples all of these.

```
# Correct — one HTTPRoute per app, explicit hostname
hostnames: ["api.demo.local"]       # frontend app
hostnames: ["reporting.demo.local"] # reporting app

# Anti-pattern — one route, path-based fan-out
hostnames: ["*.demo.local"]         # ambiguous ownership
rules:
  - matches: [{path: {value: /frontend}}]  # fragile, auditor-hostile
  - matches: [{path: {value: /reporting}}]
```

**Keep path matches narrow and explicit.**

A catch-all `PathPrefix: /` route that serves multiple apps is a misrouting
risk and makes route ownership unclear when multiple teams touch the namespace.

**Give mixed-protocol apps distinct listeners.**

If any app uses gRPC or raw TCP, declare a separate listener for it rather
than multiplexing on the HTTPS listener. Mixed protocol on one listener
creates subtle framing bugs and makes troubleshooting harder.

---

## TLS / mTLS

**Per-hostname SNI certificates — not one shared cert.**

Each app's certificate lifecycle (issuance, rotation, revocation) must be
independent even though the apps share a gateway process.

```yaml
# Correct — independent cert per hostname
tls:
  certificateRefs:
    - name: frontend-tls-cert    # rotated independently
    - name: reporting-tls-cert   # rotated independently

# Anti-pattern — one cert for all apps on the gateway
tls:
  certificateRefs:
    - name: shared-wildcard-cert  # one expiry/compromise event = blast radius
```

**Enforce TLS 1.2 minimum; prefer TLS 1.3.**

Set the minimum TLS version in the Consul API Gateway configuration. Reject
weak cipher suites (RC4, 3DES, NULL suites) explicitly.

**mTLS termination and re-encryption.**

The gateway terminates TLS from external clients. Consul's connect-inject
sidecar on each upstream pod handles mTLS for the downstream hop into the
mesh. Traffic never crosses the mesh unencrypted — the gateway is not a
TLS passthrough device for mesh-enrolled services.

**PCI/PII-scoped apps.**

Any app in PCI or PII scope must have its certificate and listener config
treated as an independent unit. Do not let compliance-scoped apps inherit
trust configuration from neighboring apps via shared listener or cert
configuration.

---

## Intentions — namespace ≠ trust boundary

This is the most important mental shift.

With one app per namespace, the namespace boundary implicitly provided
security isolation. With N apps per namespace, that guarantee is gone.
You need explicit intentions for every service pair.

```hcl
# Correct — explicit pair
Kind   = "service-intentions"
Name   = "backend"
Sources = [{ Name = "api", Action = "allow" }]

# Anti-pattern — wildcard "just for the migration"
Kind   = "service-intentions"
Name   = "*"
Sources = [{ Name = "*", Action = "allow" }]
# ^ This is the shortcut that never gets cleaned up.
# It silently removes all east-west security guarantees in the namespace.
```

Define explicit pairs from day one. See
[`demos/06-multi-app-gateway/intentions.yaml`](../demos/06-multi-app-gateway/intentions.yaml)
for the full working example.

---

## Per-route resilience

### Timeout ladder

Client timeout > gateway request timeout > gateway backendRequest timeout > upstream timeout.

A mismatch in either direction produces ambiguous errors:
- Gateway timeout < upstream timeout → gateway errors before upstream responds
- Gateway timeout > client timeout → client cuts the connection before the
  gateway can return a clean error

```yaml
# Transactional app (tight SLA)
timeouts:
  request: 10s         # gateway drops connection after 10s
  backendRequest: 8s   # gateway abandons the upstream call after 8s
# Client (OCP Route / LB) should be set to ~12s

# Reporting app (tolerant SLA)
timeouts:
  request: 90s
  backendRequest: 80s
# Client should be set to ~100s
```

Set these **per HTTPRoute**, not as a gateway-wide default. A single gateway
default will be wrong for at least one of your apps.

### Retry policy

**Disable retries on non-idempotent mutation endpoints.**

Retry amplification on a POST/PATCH payment operation is a data integrity
risk, not just a performance concern. Retries must be opt-in per route.

```yaml
# Safe — retries on read-only routes only
retries:
  numRetries: 2
  retryOn: ["5xx"]
  # Only apply to routes where the upstream is idempotent (GET, HEAD)

# Anti-pattern — global retry policy applied gateway-wide
# A blanket retry policy causes duplicate transactions on mutation endpoints.
```

### Circuit breaking

Configure per-upstream circuit breakers so one app's failure cannot exhaust
the gateway's shared Envoy connection resources and degrade neighboring apps.

```yaml
# In service-defaults for each upstream
UpstreamConfig {
  Defaults {
    PassiveHealthCheck {
      Interval           = "10s"
      MaxFailures        = 3      # eject after 3 consecutive 5xx
      MaxEjectionPercent = 100
      BaseEjectionTime   = "30s"  # doubles on repeat failures
    }
  }
}
```

See [`consul/config-entries/service-defaults-backend.yaml`](../consul/config-entries/service-defaults-backend.yaml)
for the working config used in Demo 03.

---

## Per-route rate limiting

Apply rate limits at the route/service level — not just an aggregate
gateway-level limit. An aggregate limit lets one app's burst consume the
entire budget and starve the others.

```yaml
# Correct — independent budget per app
frontend-route:  200 req/min   (customer-facing transactional)
reporting-route:  20 req/min   (bursty analytics)

# Anti-pattern — one aggregate limit
gateway-level:   220 req/min   # reporting burst consumes it entirely;
                               # frontend gets 429s during a reporting job
```

See [`demos/06-multi-app-gateway/rate-limit-filters.yaml`](../demos/06-multi-app-gateway/rate-limit-filters.yaml)
for the working example.

---

## Observability

With a shared gateway, per-gateway metrics lose per-app granularity you had
for free in the 1:1 model. Rebuild it explicitly.

**Tag every metric and log line by route and upstream service** — not just
gateway instance. Use Envoy stats tags:

```yaml
# envoy_stats_tags in Consul proxy config
stats_tags:
  - tag_name: consul_source_service
  - tag_name: consul_destination_service
  - tag_name: consul_destination_namespace
```

**Structured JSON access logs** with these fields at minimum:
- `%UPSTREAM_CLUSTER%` — which service handled the request
- `%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)%`
- `%RESPONSE_CODE%`
- `%DURATION%`
- `%BYTES_SENT%` / `%BYTES_RECEIVED%`

**Distributed tracing** — inject W3C `traceparent` headers at the gateway
and propagate to all upstream services. The gateway must not be a tracing
black hole. Strip internal tracing headers before responses reach external
clients.

---

## Sizing and scaling

With multiple apps per namespace, you cannot size the gateway off any single
app's historical baseline. Re-baseline against aggregate traffic.

**Checklist:**
- [ ] Sum peak RPS across all apps in the namespace
- [ ] Analyze whether peaks overlap (batch reporting at night vs. customer
      transactions during business hours — aggregate peak may not be additive)
- [ ] Set HPA `minReplicas` and `maxReplicas` off the aggregate baseline
- [ ] Account for xDS config payload growth — more listeners, clusters, and
      endpoints means higher gateway pod memory usage
- [ ] Monitor Consul server load — wider xDS watch subscriptions from a
      larger gateway config increase control plane load

---

## Config ownership

Multiple app teams now write config into the same namespace. Without
explicit ownership conventions, teams collide on shared resources.

**Conventions:**
- Name routes after the app they serve: `frontend-route`, `reporting-route`
- Use `CODEOWNERS` to assign per-route ownership to the relevant app team
- Treat all Gateway API resources (`GatewayClass`, `Gateway`, `HTTPRoute`)
  as versioned GitOps artifacts — no ad-hoc `kubectl apply` in production
- Use `ReferenceGrant` when routes or backend references cross namespace
  boundaries — this makes cross-namespace trust explicit, not implicit
- The Gateway itself is platform-team owned; individual `HTTPRoute` objects
  are app-team owned — keep that RBAC split clean

---

## Namespace grouping criteria

Not all apps belong in the same namespace. Use these criteria when deciding
what to group together — "similar business domain" is not sufficient.

| Criterion | Why it matters |
|-----------|----------------|
| **Traffic profile** | Timeout/retry policies can be standardized within the group |
| **Compliance scope** | PCI-scoped apps grouped with non-regulated apps can pull everything into audit scope |
| **Blast radius tolerance** | A degraded gateway affects all apps in the namespace equally |
| **Rate limit tier** | Apps with similar client profiles can share limit policies |
| **Certificate / trust lifecycle** | Co-located apps should not have tightly coupled cert rotation events |
| **Peak traffic timing** | Determines whether aggregate peak is additive or spread across time windows |

---

## Anti-patterns

### 1. Wildcard intentions "just for the migration"
The most common shortcut that never gets cleaned up. Permissive `* → *`
intentions within the namespace silently remove all east-west security
guarantees. Define explicit pairs from day one.

### 2. Single catch-all route serving multiple apps off ambiguous path matching
A `PathPrefix: /` route that fans out to multiple apps is a misrouting risk
and makes route ownership unclear.

### 3. Shared TLS certificates across unrelated apps
Couples rotation and revocation lifecycles. A single cert expiry or
compromise event triggers a blast radius across all apps sharing it.

### 4. Blanket timeout/retry policy applied gateway-wide
One timeout value is too tight for reporting and too loose for auth. A global
retry policy causes duplicate transactions on mutation endpoints.

### 5. Aggregate-only rate limiting with no per-route limits
A gateway-wide limit lets one app's burst consume the entire budget and starve
its neighbors. Per-route limits are not optional in a shared-gateway model.

### 6. Using the API Gateway for east-west traffic
The API Gateway is for north-south ingress only. Internal service-to-service
traffic between apps in the namespace must go through the mesh via sidecar
proxies with intentions — not be routed back through the gateway.

### 7. Mixing compliance scopes without a deliberate decision
A PCI-in-scope app grouped with non-regulated apps behind shared
infrastructure can pull everything into audit scope. This must be an explicit
architectural decision.

### 8. Under-sizing the shared gateway
Sizing for "what one app used to need" turns the gateway into a bottleneck
for all apps simultaneously. Re-baseline HPA thresholds off aggregate traffic.

### 9. No clear config ownership
Multiple teams writing config into the same namespace without naming
conventions, RBAC, and GitOps boundaries leads to config collisions and
unclear provenance.

### 10. Route sprawl as false isolation
Creating excessive listeners or ports to simulate the old 1:1 model adds
operational complexity without real security benefit. Use explicit intentions
and per-route policies instead.
