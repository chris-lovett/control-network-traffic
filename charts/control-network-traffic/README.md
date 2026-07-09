# control-network-traffic Helm Chart

Deploys the **control-network-traffic** demo application into a single OpenShift project/namespace.

The application is a three-tier microservices stack:

```
[browser / curl] → frontend → api → backend
```

Each service is a lightweight Go binary that exposes:
- `GET /` – returns JSON with service name, version, and the upstream response chain.
- `GET /health` – liveness/readiness probe endpoint.

## Prerequisites

| Tool | Minimum version |
|------|----------------|
| OpenShift | 4.12+ |
| Helm | 3.10+ |
| Consul Enterprise | 1.16+ (with service mesh / connect inject enabled) |

## Quick Start

### 1. Log in and create a project

```bash
oc login <your-cluster>
oc new-project control-network-traffic
```

### 2. Install the baseline chart

```bash
helm install cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --create-namespace
```

### 3. Verify rollout

```bash
oc rollout status deployment/frontend
oc rollout status deployment/api
oc rollout status deployment/backend
```

### 4. Test the request chain

```bash
# Port-forward the frontend locally
oc port-forward svc/frontend 8080:8080 &

# Hit the root endpoint – should show full chain
curl -s http://localhost:8080/ | jq .
```

Expected output (condensed):

```json
{
  "service": "frontend",
  "version": "v1",
  "message": "Hello from frontend v1!",
  "api": {
    "service": "api",
    "version": "v1",
    "message": "Hello from api v1!",
    "backend": {
      "service": "backend",
      "version": "v1",
      "message": "Hello from backend v1!"
    }
  }
}
```

## Upgrade

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backend.appVersion=v2 \
  --set backend.image.tag=v2
```

## Uninstall

```bash
helm uninstall cnt --namespace control-network-traffic
```

## Key Values

| Key | Default | Description |
|-----|---------|-------------|
| `imagePullPolicy` | `IfNotPresent` | Image pull policy for all containers |
| `frontend.image.tag` | `v1` | Frontend image tag |
| `api.image.tag` | `v1` | API image tag |
| `backend.image.tag` | `v1` | Backend (v1) image tag |
| `backend.failureRate` | `0` | Failure rate (0.0–1.0) for chaos demos |
| `backend.delayMs` | `0` | Artificial delay (ms) for latency demos |
| `backendV2.enabled` | `false` | Deploy a second backend (v2) for blue/green/canary |
| `backendV2.image.tag` | `v2` | Backend v2 image tag |
| `route.enabled` | `false` | Create an OpenShift Route for the frontend |

See [`values.yaml`](values.yaml) for the full list with documentation.

## Blue/Green Demo Short-Cut

To deploy both backend versions simultaneously:

```bash
helm upgrade cnt ./charts/control-network-traffic \
  --namespace control-network-traffic \
  --reuse-values \
  --set backendV2.enabled=true
```

Then apply the Consul `ServiceResolver` and `ServiceRouter` from
`consul/config-entries/` to control which version receives traffic.
See [`demos/01-blue-green/README.md`](../../demos/01-blue-green/README.md).
