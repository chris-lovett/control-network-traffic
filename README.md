# control-network-traffic

A demo repository for **Consul Enterprise traffic control patterns** on OpenShift.
It provides a simple three-tier Go microservices application and step-by-step demo
guides for blue/green deployments, canary releases, circuit breaking, chaos
engineering, and API Gateway ingress patterns.

---

## Architecture

```
                         ┌─────────────────────────────────┐
  external               │       Consul API Gateway         │
  traffic  ─────────────▶│  Listener: HTTPS :8443           │
                         │  HTTPRoute: api.demo.local ───────┼──▶ ┌──────────┐      ┌─────┐      ┌──────────────┐
                         │  HTTPRoute: reporting.demo.local ─┼──▶ │ frontend │─────▶│ api │─────▶│  backend v1  │
                         └─────────────────────────────────┘      └──────────┘      └─────┘      └──────────────┘
                                                                   ┌──────────┐                   ┌──────────────┐
                                                                   │reporting │                   │  backend v2  │ ← optional
                                                                   └──────────┘                   └──────────────┘
```

Each pod runs an Envoy sidecar injected by Consul connect-inject.
All inter-service calls are mTLS-secured via the mesh. The API Gateway
handles north-south TLS termination; service-to-service trust is governed
by explicit `ServiceIntentions` — not namespace co-location.

Full architecture details: [`docs/architecture.md`](docs/architecture.md)

---

## Repository Structure

```
.
├── charts/
│   └── control-network-traffic/   # Helm chart (all three services + reporting stub)
├── consul/
│   └── config-entries/            # ServiceResolver, Splitter, Router, Defaults,
│                                  # api-gateway (GatewayClass + Gateway + HTTPRoutes)
├── demos/
│   ├── 01-blue-green/             # Blue/green walkthrough
│   ├── 02-canary/                 # Canary walkthrough
│   ├── 03-circuit-breaking/       # Circuit breaker walkthrough
│   ├── 04-chaos/                  # Chaos engineering walkthrough
│   ├── 05-api-gateway-ingress/    # API Gateway: north-south ingress, TLS, timeout ladder
│   └── 06-multi-app-gateway/      # Multi-app namespace: intentions, rate limiting, isolation
│       ├── reporting-deployment.yaml
│       ├── reporting-service.yaml
│       ├── intentions.yaml        # Explicit ServiceIntentions per service pair
│       └── rate-limit-filters.yaml # Per-route rate limits (noisy-neighbor prevention)
├── docs/
│   ├── architecture.md
│   └── api-gateway-guidance.md   # Tuning reference + anti-patterns for shared gateways
├── scripts/
│   ├── deploy-baseline.sh
│   ├── check-health.sh
│   └── cleanup.sh
└── services/
    ├── backend/                   # Go service – leaf, chaos toggles
    ├── api/                       # Go service – middle tier
    └── frontend/                  # Go service – entry point
```

---

## Prerequisites

| Tool | Version | Required for |
|------|---------|-------------|
| OpenShift | 4.12+ | All demos |
| Helm | 3.10+ | All demos |
| Consul Enterprise | 1.16+ (with connect inject enabled) | All demos |
| Consul Enterprise | 1.16+ (with API Gateway controller enabled) | Demos 05, 06 |
| Kubernetes Gateway API CRDs | Installed by Consul API Gateway controller | Demos 05, 06 |
| Go (optional, for local dev) | 1.22+ | Local builds only |

---

## Quickstart

### 1. Log in to OpenShift

```bash
oc login <your-cluster-api-url>
```

### 2. Create the GHCR image pull secret

The container images are hosted on GitHub Container Registry (ghcr.io) and require
a GitHub Personal Access Token (PAT) with the `read:packages` scope.

1. [Create a PAT](https://github.com/settings/tokens) with `read:packages` scope.
2. Create the secret and link it to the default service account:

```bash
oc create secret docker-registry ghcr-pull-secret \
  --docker-server=ghcr.io \
  --docker-username=<your-github-username> \
  --docker-password=<your-github-pat> \
  -n control-network-traffic

oc secrets link default ghcr-pull-secret --for=pull -n control-network-traffic
```

> **Note:** The namespace must exist before running these commands. If it doesn't,
> create it first with `oc new-project control-network-traffic`.

### 3. Deploy the baseline application

```bash
./scripts/deploy-baseline.sh
```

This creates the `control-network-traffic` namespace and installs the Helm chart
with frontend, api, and backend (v1).

### 4. Verify the request chain

```bash
./scripts/check-health.sh
```

Expected output includes:

```json
{
  "service": "frontend", "version": "v1",
  "api": {
    "service": "api", "version": "v1",
    "backend": { "service": "backend", "version": "v1" }
  }
}
```

### 5. Clean up

```bash
./scripts/cleanup.sh
```

---

## Demo Roadmap

| # | Demo | Consul Feature | Status |
|---|------|---------------|--------|
| 01 | [Blue/Green](demos/01-blue-green/README.md) | ServiceResolver + ServiceRouter | ✅ Ready |
| 02 | [Canary](demos/02-canary/README.md) | ServiceSplitter | ✅ Ready |
| 03 | [Circuit Breaking](demos/03-circuit-breaking/README.md) | ServiceDefaults (outlier detection) | ✅ Ready |
| 04 | [Chaos Engineering](demos/04-chaos/README.md) | Fault injection + FAILURE_RATE/DELAY_MS | ✅ Ready |
| 05 | [API Gateway Ingress](demos/05-api-gateway-ingress/README.md) | GatewayClass + Gateway + HTTPRoute + per-route timeouts | ✅ Ready |
| 06 | [Multi-App Gateway](demos/06-multi-app-gateway/README.md) | N-app namespace, explicit intentions, per-route rate limiting | ✅ Ready |

---

## Helm Chart

See [`charts/control-network-traffic/README.md`](charts/control-network-traffic/README.md)
for full install/upgrade/uninstall instructions and configurable values.

---

## References

- [Consul: Canary Deployments with Service Splitters](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-splitters-canary-deployment)
- [Consul: Circuit Breaking with Envoy](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-mesh-circuit-breaking)
- [Consul: Chaos Engineering](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/introduction-chaos-engineering)
- [Consul: API Gateway](https://developer.hashicorp.com/consul/docs/api-gateway)
- [Kubernetes Gateway API Spec](https://gateway-api.sigs.k8s.io/)
- [API Gateway Tuning & Anti-Pattern Guidance](docs/api-gateway-guidance.md)
