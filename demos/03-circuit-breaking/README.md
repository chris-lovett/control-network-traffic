# Demo 03: Circuit Breaking

> **Status:** Scaffolded – steps planned, ready for walkthrough.

Circuit breaking ejects unhealthy upstream instances from the load-balancing
pool, preventing cascading failures across the service mesh.

```
          ┌────────┐   ┌─────┐      ┌─────────────┐
 users ──▶│frontend│──▶│ api │─────▶│   backend   │
          └────────┘   └─────┘      │  (Envoy      │
                                    │   outlier    │
                                    │   detection) │
                                    └─────────────┘
```

Consul configures Envoy's **outlier detection** (passive health checking) via
`ServiceDefaults`. When backend returns too many 5xx errors, Envoy temporarily
ejects it from the pool.

---

## Prerequisites

- Baseline app deployed
- Consul service mesh with Envoy sidecar injection enabled

---

## Planned Steps

### 1. Apply ServiceDefaults with circuit breaker config

```bash
consul config write consul/config-entries/service-defaults-backend.yaml
```

This configures:
- Eject after 3 consecutive 5xx responses
- Eject for 30 seconds (base), doubling on repeat failures

### 2. Enable failure simulation on backend

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0.8
```

### 3. Watch the circuit breaker trip

```bash
for i in $(seq 1 20); do
  curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/
done
# Expected: mix of 200 and 500, then 503 as Envoy ejects the backend
```

### 4. Observe recovery

After ~30 seconds, Envoy will probe the backend again. With `failureRate=0.8`
it will likely trip again; set `failureRate=0` to let it recover fully.

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0
```

---

## References

- [HashiCorp Tutorial: Circuit Breaking with Consul + Envoy](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-mesh-circuit-breaking)
- `consul/config-entries/service-defaults-backend.yaml`
- `consul/config-entries/proxy-defaults.yaml`
