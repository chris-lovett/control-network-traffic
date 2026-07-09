# Demo 02: Canary Deployment with Service Splitters

> **Status:** Scaffolded – steps planned, ready for walkthrough.

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

- Blue/green demo completed (or baseline installed + Consul config entries applied)
- Both backend v1 and v2 deployments running

---

## Planned Steps

### 1. Deploy both backend versions (if not already done)

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backendV2.enabled=true
```

### 2. Apply ServiceResolver

```bash
consul config write consul/config-entries/service-resolver.yaml
```

### 3. Start canary at 10%

Edit `consul/config-entries/service-splitter.yaml`:

```yaml
Splits:
  - Weight: 90
    ServiceSubset: "v1"
  - Weight: 10
    ServiceSubset: "v2"
```

Apply:

```bash
consul config write consul/config-entries/service-splitter.yaml
```

### 4. Verify traffic split

```bash
for i in $(seq 1 20); do
  curl -s http://localhost:8080/ | jq -r '.api.backend.version'
done
# Expected: ~18 responses "v1", ~2 responses "v2"
```

### 5. Incrementally increase canary traffic

Repeat step 3–4 at 25%, 50%, 75%, then 100%.

### 6. Promote or rollback

- **Promote:** set Weight to 100% v2, remove v1.
- **Rollback:** set Weight back to 100% v1.

---

## References

- [HashiCorp Tutorial: Canary Deployments with Service Splitters](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-splitters-canary-deployment)
- `consul/config-entries/service-splitter.yaml`
- `consul/config-entries/service-resolver.yaml`
