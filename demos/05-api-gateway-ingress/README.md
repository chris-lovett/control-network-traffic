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
- Vault with a PKI secrets engine enabled and a role that can issue certs for
  your gateway hostname
- Vault Secrets Operator deployed into the cluster (see
  [Vault Secrets Operator on OpenShift](https://developer.hashicorp.com/vault/docs/platform/k8s/vso/openshift))
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

## Step 1 — Provision the gateway TLS certificate from Vault

> **Important distinction:** Vault configured as the Consul service mesh CA
> (via `connect.ca_provider = "vault"`) issues **leaf certificates for sidecar
> mTLS** — that is separate from the **gateway listener TLS certificate**, which
> is a standard Kubernetes `tls` Secret that the gateway reads directly. This
> step provisions the listener cert; the mesh CA handles everything downstream.
>
> Reference: [Consul API Gateway configuration](https://developer.hashicorp.com/consul/docs/north-south/api-gateway)
> | [Vault Secrets Operator on OpenShift](https://developer.hashicorp.com/vault/docs/platform/k8s/vso/openshift)

The Vault Secrets Operator (VSO) is the recommended path on OpenShift. It
watches a `VaultPKISecret` resource and keeps the resulting Kubernetes `tls`
Secret current — including automatic rotation — without any manual `vault`
CLI steps or external tooling.

### 1a — Create a VaultPKISecret for the frontend hostname

```yaml
# frontend-vault-pki-secret.yaml
apiVersion: secrets.hashicorp.com/v1beta1
kind: VaultPKISecret
metadata:
  name: frontend-tls-cert
  namespace: control-network-traffic
spec:
  # Mount path of your Vault PKI secrets engine
  mount: pki
  # Vault role authorised to issue certs for your gateway hostname
  role: <your-pki-role>
  commonName: api.demo.local
  altNames:
    - api.demo.local
  # VSO writes the issued cert and key into this Secret.
  # The name must match certificateRefs in api-gateway.yaml.
  destination:
    name: frontend-tls-cert
    create: true
    type: kubernetes.io/tls
  # Renew when 2/3 of the TTL has elapsed
  expiryOffset: 30d
  ttl: 90d
```

```bash
kubectl apply -f frontend-vault-pki-secret.yaml

# VSO issues the cert and populates the Secret automatically.
# Wait for it to be ready:
kubectl wait vaultpkisecret/frontend-tls-cert \
  --for=condition=SecretSynced \
  --timeout=60s \
  -n control-network-traffic
```

### 1b — Verify the Secret was created

```bash
kubectl get secret frontend-tls-cert -n control-network-traffic
# Expected:
#   NAME                TYPE                DATA   AGE
#   frontend-tls-cert   kubernetes.io/tls   2      <age>
```

The gateway hot-reloads certificates when the Secret is updated — VSO
rotation events do not require a gateway restart or connection drain.

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

Confirm the gateway is presenting the Vault-issued certificate for the correct
hostname, and that the cert chain resolves to your Vault PKI CA:

```bash
# Confirm the subject CN matches the SNI hostname
openssl s_client -connect 127.0.0.1:18443 -servername api.demo.local \
  -showcerts </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer
# Expected:
#   subject=CN=api.demo.local
#   issuer=CN=<your Vault PKI CA name>
```

```bash
# Confirm the chain is trusted against your Vault CA cert
# Retrieve the CA cert from Vault if you don't have it locally:
vault read -field=certificate pki/cert/ca > /tmp/vault-ca.crt

openssl s_client -connect 127.0.0.1:18443 -servername api.demo.local \
  -CAfile /tmp/vault-ca.crt </dev/null 2>/dev/null | grep "Verify return code"
# Expected: Verify return code: 0 (ok)
```

```bash
# Confirm SNI isolation — the gateway must serve the reporting cert
# for reporting.demo.local, not the frontend cert
openssl s_client -connect 127.0.0.1:18443 -servername reporting.demo.local \
  -showcerts </dev/null 2>/dev/null | openssl x509 -noout -subject
# Expected: CN=reporting.demo.local  (NOT CN=api.demo.local)
```

---

## Step 6 — Cleanup

```bash
kubectl delete -f consul/config-entries/api-gateway.yaml
kubectl delete -f frontend-vault-pki-secret.yaml
# VSO will remove the managed Secret automatically when the VaultPKISecret is deleted
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
