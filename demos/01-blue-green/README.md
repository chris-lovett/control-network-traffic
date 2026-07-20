# Demo 01: Blue/Green Deployment

Blue/green deployments keep two identical production environments ("blue" = current,
"green" = new version) and switch traffic all at once after validation.

```
          ┌──────────┐       ┌─────┐      ┌─────────────┐
 users ──▶│ frontend │──────▶│ api │──────▶ backend (v1) │  ← blue (active)
          └──────────┘       └─────┘      └─────────────┘

                                           ┌─────────────┐
                                           │ backend (v2) │  ← green (standby)
                                           └─────────────┘
```

Traffic is controlled by a **Consul ServiceResolver + ServiceRouter**.

---

## Prerequisites

- OpenShift project with Consul service mesh (connect inject) enabled
- Helm 3.10+
- `consul` CLI or `kubectl` for applying config entries
- Baseline demo app installed (see repo [README](../../README.md))

> **Two port-forwards are required for this demo — open each in a dedicated terminal
> and keep them running for the duration.**
>
> Terminal A — Consul API (required for all `consul config` commands):
> ```bash
> oc port-forward svc/consul-server 8500:8500 -n consul
> ```
>
> Terminal B — Frontend traffic (required for all `curl` verification steps):
> ```bash
> oc port-forward svc/frontend 18080:8080 -n control-network-traffic
> ```

---

## Step 1 – Deploy the baseline (blue only)

```bash
helm upgrade --install cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --create-namespace
```

Verify:

```bash
oc rollout status deployment/backend
curl -s http://localhost:18080/ | jq '.api.backend.version'
# Expected: "v1"
```

---

## Step 2 – Deploy the green version alongside blue

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backendV2.enabled=true \
  --set backendV2.image.tag=v2 \
  --set backendV2.appVersion=v2
```

Verify both deployments are running:

```bash
oc get pods -l app.kubernetes.io/name=backend
# You should see pods with version=v1 and version=v2
```

---

## Step 3 – Apply Consul config entries

> If you have attempted this step before, a stale `service-router` entry may
> exist and will block writing the resolver. Delete it first:
>
> ```bash
> consul config delete -kind service-router -name backend
> ```

Apply the ServiceResolver **first** so Consul knows about v1/v2 subsets before
the router references them:

```bash
consul config write consul/config-entries/service-resolver.yaml
```

Then apply the ServiceRouter (all traffic to v1 by default):

```bash
consul config write consul/config-entries/service-router.yaml
```

Verify traffic still goes to v1:

```bash
for i in $(seq 1 5); do
  curl -s http://localhost:18080/ | jq -r '.api.backend.version'
done
# Expected: v1 (all requests)
```

---

## Step 4 – Smoke-test green using a header

The ServiceRouter sends requests with `X-Backend-Version: v2` to v2.
Use this to validate the new version without affecting regular users:

```bash
curl -s -H "X-Backend-Version: v2" http://localhost:18080/ | jq '.api.backend.version'
# Expected: "v2"

# Regular traffic still hits v1:
curl -s http://localhost:18080/ | jq '.api.backend.version'
# Expected: "v1"
```

---

## Step 5 – Cut over to green (v2)

Edit `consul/config-entries/service-router.yaml` and change the default route
`ServiceSubset` from `"v1"` to `"v2"`, then reapply:

```bash
consul config write consul/config-entries/service-router.yaml
```

Verify:

```bash
for i in $(seq 1 5); do
  curl -s http://localhost:18080/ | jq -r '.api.backend.version'
done
# Expected: v2 (all requests)
```

---

## Step 6 – Rollback (if needed)

Change the default route back to `"v1"` and reapply:

```bash
consul config write consul/config-entries/service-router.yaml
```

Or update the `DefaultSubset` in `service-resolver.yaml` and reapply.

---

## Step 7 – Cleanup

```bash
# Remove config entries
consul config delete -kind service-router -name backend
consul config delete -kind service-resolver -name backend

# Remove v2 deployment
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backendV2.enabled=false

# Full teardown
helm uninstall cnt --namespace control-network-traffic
```

---

## Key Concepts

| Concept | Consul Resource |
|---------|----------------|
| Define v1/v2 subsets | `ServiceResolver` |
| Route by header / path | `ServiceRouter` |
| Atomic traffic switch | Update `ServiceRouter` default route |
| Rollback | Revert `ServiceRouter` to previous version |
