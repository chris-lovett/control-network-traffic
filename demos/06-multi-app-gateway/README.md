# Demo 06: Multi-App Gateway — N Apps per Namespace

This demo extends Demo 05 by adding a **second application** (`reporting`) to
the same namespace, both served by the shared `api-gateway`. It demonstrates
the key challenges and correct patterns for multi-tenant gateway configuration.

```
                          ┌──────────────────────────────────────────┐
                          │         Consul API Gateway :8443          │
  external                │  Listener: HTTPS / TLS Terminate          │
  traffic ───────────────▶│                                           │
                          │  HTTPRoute: api.demo.local ───────────────┼──▶ frontend :8080
                          │  HTTPRoute: reporting.demo.local ─────────┼──▶ reporting :8080
                          └──────────────────────────────────────────┘
                                         │               │
                               (mesh sidecar mTLS on each downstream hop)
                                         │               │
                              ┌──────────▼──┐    ┌───────▼──────┐
                              │  api + backend│    │   reporting  │
                              └─────────────┘    └──────────────┘
```

One Gateway process. Two independent apps. Two explicit hostnames. Two
independent cert lifecycles. Two independent timeout policies. Two independent
rate-limit tiers. Explicit intentions — not namespace trust.

---

## What this demo covers

| Concept | Config resource |
|---------|----------------|
| Two apps sharing one gateway | Two `HTTPRoute` objects on one `Gateway` |
| Hostname isolation (not path-based) | `HTTPRoute.spec.hostnames` per app |
| Independent timeout policies per app | `HTTPRoute.spec.rules[].timeouts` |
| Noisy-neighbor prevention | Per-route rate limiting via `RateLimitFilter` |
| Explicit intentions — namespace ≠ trust boundary | `ServiceIntentions` per service pair |
| Independent cert lifecycles | Per-hostname `certificateRefs` on the shared listener |
| Circuit breaker isolation | `ServiceDefaults` per upstream so one app's failure can't exhaust shared Envoy resources |

---

## Prerequisites

- Demo 05 completed — the `GatewayClass` and `Gateway` from
  [`consul/config-entries/api-gateway.yaml`](../../consul/config-entries/api-gateway.yaml)
  must already be applied
- Baseline app deployed (see repo [README](../../README.md))
- Consul Enterprise 1.16+ with API Gateway controller and connect-inject enabled
- Port-forwards open (same as Demo 05):

> Terminal A — Consul API:
> ```bash
> oc port-forward svc/consul-server 8500:8500 -n consul
> ```
>
> Terminal B — API Gateway:
> ```bash
> oc port-forward svc/api-gateway 18443:8443 -n control-network-traffic
> ```

---

## Step 1 — Deploy the reporting service

The `reporting` service is a lightweight stub that represents a second app
sharing the namespace — a read-only analytics endpoint with a different traffic
profile (slow queries, large payloads, bursty) than the transactional frontend.

```bash
# Deploy the reporting stub
kubectl apply -f demos/06-multi-app-gateway/reporting-deployment.yaml
kubectl apply -f demos/06-multi-app-gateway/reporting-service.yaml

oc rollout status deployment/reporting -n control-network-traffic
```

Verify:

```bash
# Direct call to confirm the stub is healthy (bypasses the gateway)
oc port-forward svc/reporting 19080:8080 -n control-network-traffic &
curl -s http://localhost:19080/ | jq .
# Expected: {"service":"reporting","version":"v1","message":"Hello from reporting v1!"}
```

---

## Step 2 — Create the reporting TLS certificate

Each app gets its own certificate — independent of the frontend cert created
in Demo 05. This is the per-hostname SNI cert pattern: one gateway process,
two independent cert lifecycles.

```bash
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout /tmp/reporting.key -out /tmp/reporting.crt \
  -subj "/CN=reporting.demo.local"

kubectl create secret tls reporting-tls-cert \
  --cert=/tmp/reporting.crt \
  --key=/tmp/reporting.key \
  -n control-network-traffic
```

> **Why separate certs matter:** If both apps shared a single wildcard cert,
> rotating or revoking that cert for one app would force a simultaneous
> rotation for the other. In a banking context this is an operational risk —
> a compromised payment API cert should not require a maintenance window for
> the reporting service.

---

## Step 3 — Verify both HTTPRoutes are accepted

The `reporting-route` HTTPRoute was defined in `api-gateway.yaml` alongside
`frontend-route`. Confirm both are accepted:

```bash
kubectl get httproutes -n control-network-traffic
# Expected:
#   NAME              HOSTNAMES                  STATUS
#   frontend-route    ["api.demo.local"]          Accepted
#   reporting-route   ["reporting.demo.local"]    Accepted
```

---

## Step 4 — Confirm hostname isolation

Traffic to `api.demo.local` reaches `frontend`. Traffic to
`reporting.demo.local` reaches `reporting`. Neither leaks to the other.

```bash
# Hit the frontend route
curl -sk --resolve "api.demo.local:18443:127.0.0.1" \
  https://api.demo.local:18443/ | jq '.service'
# Expected: "frontend"

# Hit the reporting route
curl -sk --resolve "reporting.demo.local:18443:127.0.0.1" \
  https://reporting.demo.local:18443/ | jq '.service'
# Expected: "reporting"
```

> **Anti-pattern this replaces:** a single catch-all HTTPRoute with
> path-based routing (`/frontend/` vs `/reporting/`) on one hostname. That
> pattern makes route ownership ambiguous, couples both apps to the same
> hostname's cert, and makes it impossible to set independent timeout
> policies per app.

---

## Step 5 — Observe independent timeout policies

The two routes have deliberately different timeout settings set in
`api-gateway.yaml`:

| Route | `request` timeout | `backendRequest` timeout | Rationale |
|-------|-------------------|--------------------------|-----------|
| `frontend-route` | 10s | 8s | Transactional — tight, fail-fast |
| `reporting-route` | 90s | 80s | Long-running queries tolerate latency |

Demonstrate that a slow reporting backend does **not** trigger the frontend
route's 10s timeout:

```bash
# Inject a 45s delay on the backend (simulates a slow reporting query)
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.delayMs=45000

oc rollout status deployment/backend -n control-network-traffic
```

```bash
# Frontend route fires its 10s timeout — correct
time curl -sk --resolve "api.demo.local:18443:127.0.0.1" \
  https://api.demo.local:18443/ | jq .
# Expected: 504 Gateway Timeout after ~10s

# Reporting route allows the slow response through (up to 90s)
time curl -sk --resolve "reporting.demo.local:18443:127.0.0.1" \
  https://reporting.demo.local:18443/ | jq .
# Expected: 200 response after ~45s (within the 90s budget)
```

Reset:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.delayMs=0

oc rollout status deployment/backend -n control-network-traffic
```

---

## Step 6 — Apply explicit service intentions

This is the critical step in a multi-app namespace. Previously, with one app
per namespace, the namespace boundary provided implicit isolation. That
assumption no longer holds. Every service-to-service relationship must be
explicitly permitted.

Apply intentions that allow only the gateway to reach each service, and each
service to reach its downstream — nothing more:

```bash
kubectl apply -f demos/06-multi-app-gateway/intentions.yaml
```

The intentions file contains:

```hcl
# Allow: api-gateway  → frontend
# Allow: api-gateway  → reporting
# Allow: frontend     → api
# Allow: api          → backend
# Deny all else (implicit default-deny)
```

Verify the intention model is working — a direct call from `reporting` to
`backend` (cross-app, not in the intention graph) should be denied:

```bash
oc exec -it deploy/reporting -n control-network-traffic -- \
  wget -qO- http://backend:8080/ 2>&1
# Expected: connection refused / permission denied (intention deny)
```

> **Anti-pattern this prevents:** wildcard `* → *` intentions within the
> namespace "just for the migration". That is the most common shortcut that
> never gets cleaned up and silently removes the mesh's security guarantees.
> Define explicit pairs from day one.

---

## Step 7 — Demonstrate noisy-neighbor isolation with rate limiting

Without per-route rate limits, one app's traffic burst can consume the
gateway's connection budget and starve the other. Apply per-route rate limit
filters to protect against this:

```bash
kubectl apply -f demos/06-multi-app-gateway/rate-limit-filters.yaml
```

The rate limit config sets:
- `frontend-route`: 200 requests/minute (transactional, customer-facing)
- `reporting-route`: 20 requests/minute (bursty, but lower frequency)

Simulate a reporting burst that would otherwise starve the frontend:

```bash
# Hammer the reporting route
for i in $(seq 1 30); do
  curl -sk --resolve "reporting.demo.local:18443:127.0.0.1" \
    https://reporting.demo.local:18443/ -o /dev/null -w "%{http_code}\n" &
done
wait
# Expected: some 429 Too Many Requests on reporting after the 20 req/min limit

# Frontend route is unaffected — its own independent budget
curl -sk --resolve "api.demo.local:18443:127.0.0.1" \
  https://api.demo.local:18443/ | jq '.service'
# Expected: "frontend" — not rate-limited
```

---

## Step 8 — Cleanup

```bash
# Remove intentions and rate limit filters
kubectl delete -f demos/06-multi-app-gateway/intentions.yaml
kubectl delete -f demos/06-multi-app-gateway/rate-limit-filters.yaml

# Remove reporting service
kubectl delete -f demos/06-multi-app-gateway/reporting-deployment.yaml
kubectl delete -f demos/06-multi-app-gateway/reporting-service.yaml

# Remove reporting cert
kubectl delete secret reporting-tls-cert -n control-network-traffic

# Remove gateway config (also removes frontend-route and reporting-route)
kubectl delete -f consul/config-entries/api-gateway.yaml
```

---

## Supporting manifests in this directory

| File | Purpose |
|------|---------|
| `reporting-deployment.yaml` | Stub reporting service deployment (connect-inject enabled) |
| `reporting-service.yaml` | ClusterIP service for the reporting stub |
| `intentions.yaml` | Explicit `ServiceIntentions` for every service pair |
| `rate-limit-filters.yaml` | Per-route `RateLimitFilter` — frontend and reporting independent budgets |

---

## Key Concepts

| Concept | Why it matters for a shared gateway |
|---------|-------------------------------------|
| Hostname-based isolation | Route ownership is unambiguous; per-route policy is independent |
| Independent timeout policies | A slow reporting query never trips the frontend's tight timeout |
| Explicit intentions | Namespace co-location is not a trust boundary — lateral movement between apps must be explicitly denied |
| Per-route rate limits | Prevents a reporting burst from exhausting the gateway connection budget and starving the transactional app |
| Per-hostname certs | Cert rotation or revocation for one app does not affect the other |
| Circuit breakers per upstream | One app's upstream failure cannot exhaust shared Envoy resources |

---

## References

- [Consul API Gateway: HTTPRoute](https://developer.hashicorp.com/consul/docs/api-gateway/configuration/routes)
- [Consul Service Intentions](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-intentions)
- `consul/config-entries/api-gateway.yaml`
- `docs/api-gateway-guidance.md` — full tuning guidance and anti-patterns
- Demo 05: [API Gateway Ingress](../05-api-gateway-ingress/README.md)
