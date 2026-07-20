# Demo 04: Chaos Engineering

Chaos engineering deliberately injects failures and latency to verify that the
system (and Consul service mesh) behaves gracefully under adverse conditions.

The `backend` service has built-in chaos toggles controlled by environment variables:

| Env Var | Default | Description |
|---------|---------|-------------|
| `FAILURE_RATE` | `0` | Probability (0.0–1.0) that any request returns HTTP 500 |
| `DELAY_MS` | `0` | Artificial response delay in milliseconds |

---

## Prerequisites

- Baseline app deployed (see repo [README](../../README.md))
- Frontend port-forwarded (dedicated terminal): `oc port-forward svc/frontend 18080:8080 -n control-network-traffic`

---

## Scenario A — Random failures

Inject a 50% failure rate and observe errors propagating through the mesh:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0.5

oc rollout status deployment/backend -n control-network-traffic
```

Verify:

```bash
for i in $(seq 1 10); do
  curl -s http://localhost:18080/ | jq -r 'if .api.backend then "ok: \(.api.backend.version)" else "FAIL" end'
done
# Expected: ~50% ok, ~50% FAIL
```

---

## Scenario B — Latency injection

Inject a 2 second artificial delay on every backend response:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0 \
  --set backend.delayMs=2000

oc rollout status deployment/backend -n control-network-traffic
```

Verify:

```bash
time curl -s http://localhost:18080/ > /dev/null
# Expected: real time > 2s
```

---

## Scenario C — Total backend failure

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=1.0 \
  --set backend.delayMs=0

oc rollout status deployment/backend -n control-network-traffic
```

Verify every request surfaces a backend error:

```bash
curl -s http://localhost:18080/ | jq '.api.error'
# Expected: "backend call failed: backend returned status 500: ..."
```

---

## Scenario D — Pod kill

Reset chaos toggles first, then delete the backend pod:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0 \
  --set backend.delayMs=0

oc rollout status deployment/backend -n control-network-traffic
```

Then in a separate terminal, poll while deleting the pod:

```bash
# Terminal 1 — poll continuously
watch -n1 'curl -s http://localhost:18080/ | jq -r "if .api.backend then \"ok: \\(.api.backend.version)\" else \"FAIL\" end"'

# Terminal 2 — kill the pod
oc delete pod -l app.kubernetes.io/name=backend,version=v1 -n control-network-traffic
```

Expected: brief FAIL responses while the pod restarts, then recovery once the new
pod passes readiness probes (~10–20 seconds).

---

## Recovery — reset all chaos toggles

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0 \
  --set backend.delayMs=0

oc rollout status deployment/backend -n control-network-traffic
```

---

## References

- [HashiCorp Tutorial: Consul and Chaos Engineering](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/introduction-chaos-engineering)
- Backend service `FAILURE_RATE` and `DELAY_MS` env vars (see `services/backend/main.go`)

