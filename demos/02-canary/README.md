# Demo 02: Canary Deployment with Service Splitters

A canary deployment gradually shifts traffic from v1 to v2, letting you
validate the new version on a small percentage of real traffic before full cut-over.

```
                                    ┌──────────────────────────────┐
                                    │  Consul ServiceSplitter       │
          ┌────────┐   ┌─────┐      │  backend:  90% → v1           │
 users ──▶│frontend│──▶│ api │─────▶│            10% → v2 (canary)  │
          └────────┘   └─────┘      └──────────────────────────────┘
```

---

## Prerequisites

- Baseline installed (see repo [README](../../README.md))
- Consul CLI available and port-forwarded to the Consul server

---

## Steps

### 1. Port-forward the Consul server (if not already running)

```bash
oc port-forward svc/consul-server 8500:8500 -n consul &
```

### 2. Deploy both backend versions (if not already done)

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backendV2.enabled=true

oc rollout status deployment/backend-v2 -n control-network-traffic
```

### 3. Apply the ServiceResolver

```bash
consul config write consul/config-entries/service-resolver.yaml
```

### 4. Remove the ServiceRouter (if active from a previous demo)

The ServiceRouter bypasses the ServiceSplitter. Remove it before starting the canary:

```bash
consul config delete -kind service-router -name backend
```

> If no router is active this command will error — that's fine, continue.

### 5. Port-forward to frontend and verify baseline (100% v1)

```bash
oc port-forward deployment/frontend 8080:8080 -n control-network-traffic &

for i in $(seq 1 10); do
  curl -s http://localhost:8080/ | jq -r '.api.backend.version'
done
# Expected: all "v1"
```

### 6. Start canary at 10%

Edit `consul/config-entries/service-splitter.yaml` — set the weights:

```hcl
Splits = [
  {
    Weight        = 90
    ServiceSubset = "v1"
  },
  {
    Weight        = 10
    ServiceSubset = "v2"
  }
]
```

Apply:

```bash
consul config write consul/config-entries/service-splitter.yaml
```

Verify:

```bash
for i in $(seq 1 20); do
  curl -s http://localhost:8080/ | jq -r '.api.backend.version'
done
# Expected: ~18 "v1", ~2 "v2" (10% canary)
```

### 7. Incrementally increase canary traffic

Repeat step 6 with increasing v2 weights:

| Stage | v1 weight | v2 weight |
|-------|-----------|-----------|
| Baseline | 100 | 0 |
| Canary start | 90 | 10 |
| Expand | 50 | 50 |
| Pre-promote | 10 | 90 |
| Full cut-over | 0 | 100 |

Each time, apply the splitter and run the 20-request verification loop.

### 8. Promote or rollback

**Promote to v2:**
```bash
# Set Weight = 100 for v2, remove v1 split, apply
consul config write consul/config-entries/service-splitter.yaml
```

**Rollback to v1:**
```bash
# Set Weight = 100 for v1, remove v2 split, apply
consul config write consul/config-entries/service-splitter.yaml
```

---

## References

- [HashiCorp Tutorial: Canary Deployments with Service Splitters](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-splitters-canary-deployment)
- `consul/config-entries/service-splitter.yaml`
- `consul/config-entries/service-resolver.yaml`

