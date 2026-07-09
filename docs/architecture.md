# Architecture

## Overview

The `control-network-traffic` demo application is a three-tier microservices
stack that demonstrates Consul Enterprise traffic control patterns on OpenShift.

## Service Topology

```
                              ┌──────────────────────────────────────┐
                              │            OpenShift Project          │
                              │                                        │
  external   ┌───────────┐   │  ┌─────────┐       ┌─────────────┐  │
  traffic ──▶│ OCP Route │──▶│  │frontend │──────▶│    api      │  │
             └───────────┘   │  │ :8080   │       │   :8080     │  │
                              │  └─────────┘       └──────┬──────┘  │
                              │                           │          │
                              │                    ┌──────▼──────┐  │
                              │                    │  backend v1  │  │
                              │                    │   :8080     │  │
                              │                    └─────────────┘  │
                              │                                        │
                              │                    ┌─────────────┐   │
                              │                    │  backend v2  │  │
                              │                    │  (optional) │  │
                              │                    └─────────────┘  │
                              └──────────────────────────────────────┘
```

Each pod has an **Envoy sidecar** injected by Consul's connect-inject webhook.
All service-to-service traffic flows through these sidecars, enabling:

- mTLS by default
- L7 traffic management (routing, splitting, circuit breaking)
- Observability (metrics, tracing)

## Services

| Service | Port | Responsibility |
|---------|------|----------------|
| `frontend` | 8080 | Serves the entry point; calls `api` and returns full chain |
| `api` | 8080 | Middle tier; calls `backend` and aggregates the response |
| `backend` | 8080 | Leaf service; returns version info + supports chaos toggles |

## Request Flow

```
User/curl → frontend(/) → api(/) → backend(/) → backend response
                              ↑          ↑
                         logged here  logged here
```

Each hop adds its own service/version info to the JSON response, making it easy
to see which version of each service handled the request.

## Consul Traffic Control Plane

```
┌─────────────────────────────────────────────────────┐
│                  Consul Config Entries               │
│                                                      │
│  ServiceResolver  → defines v1/v2 subsets           │
│  ServiceRouter    → routes by header or path         │
│  ServiceSplitter  → splits traffic by weight %       │
│  ServiceDefaults  → per-service protocol + circuit   │
│                     breaker (outlier detection)      │
│  ProxyDefaults    → global Envoy proxy settings      │
└─────────────────────────────────────────────────────┘
```

## Key Env Vars

| Var | Services | Description |
|-----|----------|-------------|
| `APP_VERSION` | all | Version string surfaced in responses |
| `PORT` | all | HTTP listen port (default `8080`) |
| `API_URL` | frontend | URL of the API service |
| `BACKEND_URL` | api | URL of the backend service |
| `FAILURE_RATE` | backend | 0.0–1.0 rate of simulated 500 errors |
| `DELAY_MS` | backend | Artificial delay per request (ms) |
