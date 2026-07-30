# Demo 05: API Gateway — North-South Ingress

This demo replaces the OpenShift Route with a **Consul API Gateway**, giving
you L7 policy (TLS termination, per-route timeouts, header manipulation) at the
ingress point rather than relying solely on the OCP router.

```
                                 ┌─────────────────────────────────┐
                                 │   Consul API Gateway :8443       │
  external   ┌─────────────┐    │   GatewayClass: consul           │
  traffic ──▶│ OCP Route   │───▶│   Listener: HTTPS / TLS Terminate│
             └─────────────┘    │   HTTPRoute: api.demo.local → ───┼──▶ frontend :8080
                                 └─────────────────────────────────┘
                                                │
                              (mesh sidecar re-encrypts downstream)
                                                │
                                          ┌─────▼─────┐    ┌─────────┐
                                          │    api    │───▶│ backend │
                                          └───────────┘    └─────────┘
```

Traffic is controlled by a **GatewayClass + Gateway + HTTPRoute** (Kubernetes
Gateway API), managed by Consul Enterprise's API Gateway controller.

---

## What this demo covers

| Concept | Config resource |
|---------|----------------|
| Register Consul as gateway controller | `GatewayClass` |
| Declare shared listener with TLS termination | `Gateway` |
| Bind a service to an explicit hostname | `HTTPRoute` |
| Per-route timeout ladder | `HTTPRoute.spec.rules[].timeouts` |
| Per-hostname SNI certificate isolation | `Gateway.spec.listeners[].tls.certificateRefs` |
| mTLS re-encryption downstream via mesh | Consul connect-inject sidecar |

---

## Prerequisites

- Baseline demo app installed (see repo [README](../../README.md))
- Consul Enterprise 1.16+ with the API Gateway controller enabled
- Kubernetes Gateway API CRDs installed (Consul installs these automatically
  when the API Gateway component is enabled)
- `kubectl` or `oc` access to the cluster
- Two port-forwards open in dedicated terminals (see below)

> **Two port-forwards are required — open each in a dedicated terminal and
> keep them running for the duration.**
>
> Terminal A — Consul API (required for all `consul config` commands):
> ```bash
> oc port-forward svc/consul-server 8500:8500 -n consul
> ```
>
> Terminal B — API Gateway (required for all `curl` verification steps):
> ```bash
> oc port-forward svc/api-gateway 18443:8443 -n control-network-traffic
> ```

---

## Step 1 — Create TLS certificates for the gateway listener

The gateway terminates TLS at the listener. Each app hostname gets its own
certificate — independent rotation and revocation lifecycles even on a shared
gateway process.

For this demo, generate self-signed certs. In production, use Vault PKI or
cert-manager.

```bash
# Certificate for api.demo.local (frontend route)
openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout /tmp/frontend.key -out /tmp/frontend.crt \
  -subj "/CN=api.demo.local"

kubectl create secret tls frontend-tls-cert \
  --cert=/tmp/frontend.crt \
  --key=/tmp/frontend.key \
  -n control-network-traffic
```

Verify the Secret exists:

```bash
kubectl get secret frontend-tls-cert -n control-network-traffic
```

---

## Step 2 — Apply the API Gateway config

```bash
kubectl apply -f consul/config-entries/api-gateway.yaml
```

This creates the `GatewayClass`, the shared `Gateway`, and the `HTTPRoute`
binding `api.demo.local` to the `frontend` service.

Verify the Gateway is accepted and programmed:

```bash
kubectl get gateway api-gateway -n control-network-traffic
# Expected: PROGRAMMED = True, READY = True
```

Verify the HTTPRoute is accepted:

```bash
kubectl get httproute frontend-route -n control-network-traffic
# Expected: STATUS = Accepted
```

---

## Step 3 — Verify traffic flows through the gateway

```bash
curl -sk --resolve "api.demo.local:18443:127.0.0.1" \
  https://api.demo.local:18443/ | jq .
```

Expected response — the full request chain:

```json
{
  "service": "frontend", "version": "v1",
  "api": {
    "service": "api", "version": "v1",
    "backend": { "service": "backend", "version": "v1" }
  }
}
```

---

## Step 4 — Observe the timeout ladder

The `HTTPRoute` sets:
- `timeouts.request: 10s` — gateway drops the connection if frontend doesn't
  respond within 10s
- `timeouts.backendRequest: 8s` — gateway drops the upstream request to
  frontend after 8s

This is the **timeout ladder pattern**: gateway timeout (10s) > upstream
timeout (8s). The client (OCP Route or load balancer) should be set to ~12s
so the gateway always has a chance to return a clean error rather than the
client cutting the connection first.

Demonstrate the timeout firing by injecting a 15s delay via the chaos toggle:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.delayMs=15000

oc rollout status deployment/backend -n control-network-traffic
```

```bash
time curl -sk --resolve "api.demo.local:18443:127.0.0.1" \
  https://api.demo.local:18443/ | jq .
# Expected: gateway returns 504 after ~10s, not a client-side hang
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

## Step 5 — Verify TLS termination and downstream re-encryption

The gateway terminates TLS from the client. Consul's connect-inject sidecar
on the `frontend` pod then handles mTLS for the downstream hop into the mesh —
the gateway never sends unencrypted traffic into the cluster.

```bash
# Confirm the certificate presented by the gateway matches api.demo.local
openssl s_client -connect 127.0.0.1:18443 -servername api.demo.local \
  -showcerts </dev/null 2>/dev/null | openssl x509 -noout -subject
# Expected: subject=CN=api.demo.local
```

---

## Step 6 — Cleanup

```bash
kubectl delete -f consul/config-entries/api-gateway.yaml
kubectl delete secret frontend-tls-cert -n control-network-traffic
```

---

## Key Concepts

| Concept | Detail |
|---------|--------|
| Hostname-based routing | `api.demo.local` — not a wildcard. Each app gets an explicit hostname for clear ownership and auditability |
| Per-route timeout ladder | `request > backendRequest` — aligns gateway, upstream, and client timeouts to prevent ambiguous errors |
| Per-hostname SNI cert | Each app's cert lifecycle is independent even on a shared Gateway process |
| mTLS re-encryption | Gateway terminates TLS externally; Consul sidecar handles mTLS into the mesh |
| `allowedRoutes: Same` | Only routes in this namespace can bind to the gateway — prevents cross-namespace route injection |

---

## References

- [Consul API Gateway Docs](https://developer.hashicorp.com/consul/docs/api-gateway)
- [Kubernetes Gateway API Spec](https://gateway-api.sigs.k8s.io/)
- `consul/config-entries/api-gateway.yaml`
- Demo 06: [Multi-App Gateway](../06-multi-app-gateway/README.md) — extends this demo to N apps per namespace
