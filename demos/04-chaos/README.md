# Demo 04: Chaos Engineering

> **Status:** Scaffolded – steps planned, ready for walkthrough.

Chaos engineering deliberately injects failures and latency to verify that the
system (and Consul service mesh) behaves gracefully under adverse conditions.

The `backend` service has built-in chaos toggles controlled by environment variables:

| Env Var | Default | Description |
|---------|---------|-------------|
| `FAILURE_RATE` | `0` | Probability (0.0–1.0) that any request returns HTTP 500 |
| `DELAY_MS` | `0` | Artificial response delay in milliseconds |

---

## Planned Scenarios

### Scenario A – Random failures (circuit breaker trigger)

Combine with Demo 03 to observe circuit breaking in action:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0.5
```

Expected behaviour: ~50% of requests fail; circuit breaker trips after threshold.

### Scenario B – Latency injection (timeout / retry testing)

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.delayMs=3000
```

Expected: responses take ≥3 seconds; observe timeout behaviour in api/frontend.

### Scenario C – Total backend failure

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=1.0
```

Expected: circuit breaker ejects all backend instances; frontend surfaces error.

### Scenario D – Pod kill

```bash
# Delete the backend pod; OpenShift restarts it automatically
oc delete pod -l app.kubernetes.io/name=backend
```

Expected: brief 503, then recovery once the new pod passes readiness probes.

---

## Recovery

Reset chaos toggles:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.failureRate=0 \
  --set backend.delayMs=0
```

---

## References

- [HashiCorp Tutorial: Consul and Chaos Engineering](https://developer.hashicorp.com/consul/tutorials/control-network-traffic/introduction-chaos-engineering)
- Backend service `FAILURE_RATE` and `DELAY_MS` env vars (see `services/backend/main.go`)
