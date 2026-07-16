# Demo 03: Circuit Breaking

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

> **Known limitation (Consul 2.0 + OVN-Kubernetes):** `UpstreamConfig.Defaults.PassiveHealthCheck`
> in `service-defaults` is not currently translated to Envoy outlier detection
> configuration in Consul 2.0 with consul-dataplane on OpenShift/OVN-K8s.
> The failure simulation (steps 2–3) works and demonstrates fault propagation,
> but automatic Envoy-level host ejection (503 responses) does not fire in this environment.

---

## Prerequisites

- Baseline app deployed (see repo [README](../../README.md))
- Consul CLI port-forwarded: `oc port-forward svc/consul-server 8500:8500 -n consul &`
- Frontend port-forwarded: `oc port-forward deployment/frontend 8080:8080 -n control-network-traffic &`

---

## Steps

### 1. Apply ServiceDefaults with circuit breaker config

```bash
consul config write consul/config-entries/service-defaults-backend.yaml
```

This configures:
- Eject after 3 consecutive 5xx responses (`MaxFailures: 3`)
- Eject for 30 seconds base (`BaseEjectionTime: 30s`), doubling on repeat failures
- Up to 100% of hosts can be ejected (`MaxEjectionPercent: 100`)

### 2. Enable failure simulation on backend v1

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=1.0

oc rollout status deployment/backend -n control-network-traffic
```

### 3. Observe failures propagating through the mesh

```bash
for i in $(seq 1 10); do
  curl -s http://localhost:8080/ | jq -r '.api.error // "ok: \(.api.backend.version)"'
done
```

Expected output — all requests surface backend failures through the chain:
```
backend call failed: backend returned status 500: ...
backend call failed: backend returned status 500: ...
```

### 4. Recover — disable failure simulation

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0

oc rollout status deployment/backend -n control-network-traffic
```

Verify recovery:

```bash
for i in $(seq 1 5); do
  curl -s http://localhost:8080/ | jq -r '.api.backend.version'
done
# Expected: v1 (all requests healthy again)
```

---

## What the circuit breaker config does (conceptually)

Even where Envoy ejection does not visually fire, the `service-defaults` entry is
read by Consul and pushed to Envoy via xDS. In environments where the full
transparent proxy pipeline is active, after `MaxFailures` consecutive 5xx responses
Envoy would:
1. Remove the failing instance from its load-balancing pool
2. Return `503 no healthy upstream` to the caller
3. Re-admit the instance after `BaseEjectionTime` (with exponential backoff)

---

## References

- [HashiCorp Tutorial: Circuit Breaking with Consul + Envoy](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/service-mesh-circuit-breaking)
- `consul/config-entries/service-defaults-backend.yaml`
- `consul/config-entries/proxy-defaults.yaml`

