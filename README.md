# control-network-traffic

A demo repository for **Consul Enterprise traffic control patterns** on OpenShift.
It provides a simple three-tier Go microservices application and step-by-step demo
guides for blue/green deployments, canary releases, circuit breaking, and chaos
engineering.

---

## Architecture

```
  external        ┌──────────┐      ┌─────┐      ┌──────────────┐
  traffic  ──────▶│ frontend │─────▶│ api │─────▶│  backend v1  │
                  └──────────┘      └─────┘      └──────────────┘
                                                  ┌──────────────┐
                                                  │  backend v2  │ ← optional
                                                  └──────────────┘
```

Each pod runs an Envoy sidecar injected by Consul connect-inject.
All inter-service calls are mTLS-secured and observable.

Full architecture details: [`docs/architecture.md`](docs/architecture.md)

---

## Repository Structure

```
.
├── charts/
│   └── control-network-traffic/   # Helm chart (all three services)
├── consul/
│   └── config-entries/            # ServiceResolver, Splitter, Router, Defaults
├── demos/
│   ├── 01-blue-green/             # Blue/green walkthrough
│   ├── 02-canary/                 # Canary walkthrough
│   ├── 03-circuit-breaking/       # Circuit breaker walkthrough
│   └── 04-chaos/                  # Chaos engineering walkthrough
├── docs/
│   └── architecture.md
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

| Tool | Version |
|------|---------|
| OpenShift | 4.12+ |
| Helm | 3.10+ |
| Consul Enterprise | 1.16+ (with connect inject enabled) |
| Go (optional, for local dev) | 1.22+ |

---

## Quickstart

### 1. Log in to OpenShift

```bash
oc login <your-cluster-api-url>
```

### 2. Deploy the baseline application

```bash
./scripts/deploy-baseline.sh
```

This creates the `control-network-traffic` namespace and installs the Helm chart
with frontend, api, and backend (v1).

### 3. Verify the request chain

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

### 4. Clean up

```bash
./scripts/cleanup.sh
```

---

## Demo Roadmap

| # | Demo | Consul Feature | Status |
|---|------|---------------|--------|
| 01 | [Blue/Green](demos/01-blue-green/README.md) | ServiceResolver + ServiceRouter | ✅ Ready |
| 02 | [Canary](demos/02-canary/README.md) | ServiceSplitter | 📋 Scaffolded |
| 03 | [Circuit Breaking](demos/03-circuit-breaking/README.md) | ServiceDefaults (outlier detection) | 📋 Scaffolded |
| 04 | [Chaos Engineering](demos/04-chaos/README.md) | Fault injection + FAILURE_RATE/DELAY_MS | 📋 Scaffolded |

---

## Helm Chart

See [`charts/control-network-traffic/README.md`](charts/control-network-traffic/README.md)
for full install/upgrade/uninstall instructions and configurable values.

---

## References

- [Consul: Canary Deployments with Service Splitters](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-splitters-canary-deployment)
- [Consul: Circuit Breaking with Envoy](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-mesh-circuit-breaking)
- [Consul: Chaos Engineering](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/introduction-chaos-engineering)
