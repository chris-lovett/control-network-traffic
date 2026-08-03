# Blue/Green Deployment Troubleshooting Guide

Operational reference for platform engineers running blue/green deployments
with Consul Enterprise on OpenShift. Covers the full deployment lifecycle —
traffic splitting, environment isolation, rollback, and pipeline automation —
and the failure modes most commonly encountered at each stage.

For runnable step-by-step examples see [Demo 01 — Blue/Green](../demos/01-blue-green/README.md).

---

## Contents

- [How Consul blue/green works on OpenShift](#how-consul-bluegreen-works-on-openshift)
- [Diagnostic commands — quick reference](#diagnostic-commands--quick-reference)
- [Issue index](#issue-index)
- [Traffic splitting issues](#traffic-splitting-issues)
  - [T1 — All traffic stays on blue after splitter write](#t1--all-traffic-stays-on-blue-after-splitter-write)
  - [T2 — Green subset receives no traffic despite weight > 0](#t2--green-subset-receives-no-traffic-despite-weight--0)
  - [T3 — Traffic split is uneven or non-deterministic](#t3--traffic-split-is-uneven-or-non-deterministic)
  - [T4 — Splitter write fails with validation error](#t4--splitter-write-fails-with-validation-error)
  - [T5 — Header-based routing to green does not work](#t5--header-based-routing-to-green-does-not-work)
- [Service registration issues](#service-registration-issues)
  - [R1 — Green pods are Running but not appearing in Consul catalog](#r1--green-pods-are-running-but-not-appearing-in-consul-catalog)
  - [R2 — Green subset is empty in the ServiceResolver](#r2--green-subset-is-empty-in-the-serviceresolver)
  - [R3 — Health checks passing in Kubernetes but failing in Consul](#r3--health-checks-passing-in-kubernetes-but-failing-in-consul)
- [Config entry issues](#config-entry-issues)
  - [C1 — ServiceResolver write is rejected](#c1--servicereresolver-write-is-rejected)
  - [C2 — ServiceSplitter write is rejected](#c2--servicesplitter-write-is-rejected)
  - [C3 — Config entry applied but Envoy has not picked up the change](#c3--config-entry-applied-but-envoy-has-not-picked-up-the-change)
  - [C4 — DefaultSubset is not honoured after splitter deletion](#c4--defaultsubset-is-not-honoured-after-splitter-deletion)
- [mTLS and intentions issues](#mtls-and-intentions-issues)
  - [M1 — Green pods return 503 immediately after deployment](#m1--green-pods-return-503-immediately-after-deployment)
  - [M2 — Intention exists but green still receives connection refused](#m2--intention-exists-but-green-still-receives-connection-refused)
  - [M3 — mTLS certificate errors in green pod logs](#m3--mtls-certificate-errors-in-green-pod-logs)
- [Admin partition and namespace issues](#admin-partition-and-namespace-issues)
  - [P1 — Green partition cannot reach shared services in blue partition](#p1--green-partition-cannot-reach-shared-services-in-blue-partition)
  - [P2 — ACL token used by CI/CD pipeline is denied in green partition](#p2--acl-token-used-by-cicd-pipeline-is-denied-in-green-partition)
  - [P3 — exported-services change is not visible in green partition](#p3--exported-services-change-is-not-visible-in-green-partition)
- [Rollback issues](#rollback-issues)
  - [B1 — Rollback splitter write succeeds but traffic does not return to blue](#b1--rollback-splitter-write-succeeds-but-traffic-does-not-return-to-blue)
  - [B2 — In-flight requests are dropped during rollback](#b2--in-flight-requests-are-dropped-during-rollback)
  - [B3 — Blue pods have been scaled down before soak period ends](#b3--blue-pods-have-been-scaled-down-before-soak-period-ends)
- [Pipeline and automation issues](#pipeline-and-automation-issues)
  - [A1 — Pipeline shifts weight before green pods are healthy](#a1--pipeline-shifts-weight-before-green-pods-are-healthy)
  - [A2 — consul config write races with Envoy xDS propagation](#a2--consul-config-write-races-with-envoy-xds-propagation)
  - [A3 — Automated rollback trigger fires on a transient spike](#a3--automated-rollback-trigger-fires-on-a-transient-spike)

---

## How Consul blue/green works on OpenShift

On OpenShift with Consul connect-inject, each pod carries an Envoy sidecar.
Traffic management is entirely data-plane — no changes to the application, no
DNS flips, no load balancer reconfiguration.

The three config entries that together form the blue/green primitive:

```
ServiceDefaults  →  declares protocol (must be http/http2/grpc for L7 splitting)
ServiceResolver  →  defines "blue" and "green" subsets by matching pod metadata
ServiceSplitter  →  assigns traffic weights across those subsets (must sum to 100)
```

Pod metadata is set via the `consul.hashicorp.com/service-meta-*` annotation on
the Deployment, which maps to `Service.Meta` in the Consul catalog:

```yaml
# Deployment annotation — green version
annotations:
  consul.hashicorp.com/service-meta-env: "green"
  consul.hashicorp.com/service-meta-version: "v2.3.0"
```

The ServiceResolver matches on that metadata:

```hcl
Kind = "service-resolver"
Name = "backend"

Subsets = {
  blue  = { Filter = "Service.Meta.env == blue"  }
  green = { Filter = "Service.Meta.env == green" }
}

DefaultSubset = "blue"
```

The ServiceSplitter controls the live weight distribution:

```hcl
Kind   = "service-splitter"
Name   = "backend"
Splits = [
  { Weight = 95, ServiceSubset = "blue"  },
  { Weight = 5,  ServiceSubset = "green" },
]
```

Envoy sidecars receive the updated routing config via xDS within milliseconds
of a config entry write. No pod restarts are required.

---

## Diagnostic commands — quick reference

```bash
# List all services registered in Consul for the namespace
consul catalog services -namespace <ns>

# Inspect a specific service's registered instances and their metadata
consul catalog nodes -service <service-name> -namespace <ns>

# Read a config entry
consul config read -kind service-resolver  -name <service-name>
consul config read -kind service-splitter  -name <service-name>
consul config read -kind service-defaults  -name <service-name>

# List all config entries of a kind
consul config list -kind service-splitter

# Check Envoy xDS state for a specific pod's sidecar
oc exec -n <ns> <pod-name> -c envoy -- \
  curl -s http://localhost:19000/config_dump | jq '.configs[] | select(.["@type"] | contains("RouteConfiguration"))'

# Check Envoy cluster membership (which endpoints are in each subset)
oc exec -n <ns> <pod-name> -c envoy -- \
  curl -s http://localhost:19000/clusters | grep <service-name>

# Check Envoy listeners
oc exec -n <ns> <pod-name> -c envoy -- \
  curl -s http://localhost:19000/listeners

# Tail connect-inject logs (registration events, health check updates)
oc logs -n consul -l component=connect-injector --tail=100 -f

# Tail a specific pod's Envoy sidecar logs
oc logs -n <ns> <pod-name> -c envoy --tail=100 -f

# Check Consul agent on a node for health check state
consul health service <service-name> -namespace <ns>

# Check intentions
consul intention list -namespace <ns>
consul intention check <source> <destination>
```

---

## Issue index

| # | Symptom | Section |
|---|---------|---------|
| T1 | All traffic stays on blue after splitter write | [T1](#t1--all-traffic-stays-on-blue-after-splitter-write) |
| T2 | Green subset receives no traffic despite weight > 0 | [T2](#t2--green-subset-receives-no-traffic-despite-weight--0) |
| T3 | Traffic split is uneven or non-deterministic | [T3](#t3--traffic-split-is-uneven-or-non-deterministic) |
| T4 | Splitter write fails with validation error | [T4](#t4--splitter-write-fails-with-validation-error) |
| T5 | Header-based routing to green does not work | [T5](#t5--header-based-routing-to-green-does-not-work) |
| R1 | Green pods are Running but not in Consul catalog | [R1](#r1--green-pods-are-running-but-not-appearing-in-consul-catalog) |
| R2 | Green subset is empty in the ServiceResolver | [R2](#r2--green-subset-is-empty-in-the-serviceresolver) |
| R3 | Health checks passing in Kubernetes but failing in Consul | [R3](#r3--health-checks-passing-in-kubernetes-but-failing-in-consul) |
| C1 | ServiceResolver write rejected | [C1](#c1--servicereresolver-write-is-rejected) |
| C2 | ServiceSplitter write rejected | [C2](#c2--servicesplitter-write-is-rejected) |
| C3 | Config entry applied but Envoy has not picked up the change | [C3](#c3--config-entry-applied-but-envoy-has-not-picked-up-the-change) |
| C4 | DefaultSubset not honoured after splitter deletion | [C4](#c4--defaultsubset-is-not-honoured-after-splitter-deletion) |
| M1 | Green pods return 503 immediately after deployment | [M1](#m1--green-pods-return-503-immediately-after-deployment) |
| M2 | Intention exists but green still receives connection refused | [M2](#m2--intention-exists-but-green-still-receives-connection-refused) |
| M3 | mTLS certificate errors in green pod logs | [M3](#m3--mtls-certificate-errors-in-green-pod-logs) |
| P1 | Green partition cannot reach shared services in blue partition | [P1](#p1--green-partition-cannot-reach-shared-services-in-blue-partition) |
| P2 | CI/CD ACL token denied in green partition | [P2](#p2--acl-token-used-by-cicd-pipeline-is-denied-in-green-partition) |
| P3 | exported-services change not visible in green partition | [P3](#p3--exported-services-change-is-not-visible-in-green-partition) |
| B1 | Rollback splitter write succeeds but traffic does not return to blue | [B1](#b1--rollback-splitter-write-succeeds-but-traffic-does-not-return-to-blue) |
| B2 | In-flight requests dropped during rollback | [B2](#b2--in-flight-requests-are-dropped-during-rollback) |
| B3 | Blue pods scaled down before soak period ends | [B3](#b3--blue-pods-have-been-scaled-down-before-soak-period-ends) |
| A1 | Pipeline shifts weight before green pods are healthy | [A1](#a1--pipeline-shifts-weight-before-green-pods-are-healthy) |
| A2 | consul config write races with Envoy xDS propagation | [A2](#a2--consul-config-write-races-with-envoy-xds-propagation) |
| A3 | Automated rollback fires on a transient spike | [A3](#a3--automated-rollback-trigger-fires-on-a-transient-spike) |

---

## Traffic splitting issues

### T1 — All traffic stays on blue after splitter write

**Symptom:** `consul config write` exits 0, but 100% of requests continue
hitting blue instances.

**Diagnosis:**

```bash
# Confirm the splitter is actually stored
consul config read -kind service-splitter -name <service-name>

# Check whether ServiceDefaults declares http protocol — required for L7 splitting
consul config read -kind service-defaults -name <service-name>

# Confirm Envoy on a calling pod has received the updated route config
oc exec -n <ns> <caller-pod> -c envoy -- \
  curl -s http://localhost:19000/config_dump | \
  jq '.configs[] | select(.["@type"] | contains("RouteConfiguration")) | .route_config.virtual_hosts[].routes'
```

**Cause A — Missing or wrong ServiceDefaults protocol.**
L7 traffic management (splitting, routing) requires the service protocol to be
declared as `http`, `http2`, or `grpc`. If `ServiceDefaults` is absent or set
to `tcp`, the splitter is stored but silently ignored; Envoy falls back to
L4 round-robin.

```hcl
# Fix: ensure ServiceDefaults declares the correct protocol
Kind     = "service-defaults"
Name     = "backend"
Protocol = "http"   # required for splitting to take effect
```

**Cause B — Envoy sidecar has not received the xDS update.**
Confirm the calling pod's Envoy has the new cluster weights (see diagnostic
above). If the config dump still shows the old weights, check Consul server
health and xDS stream connectivity (see [C3](#c3--config-entry-applied-but-envoy-has-not-picked-up-the-change)).

---

### T2 — Green subset receives no traffic despite weight > 0

**Symptom:** Splitter shows `Weight = 5` for green but all traffic goes to blue.
No errors in logs.

**Diagnosis:**

```bash
# Check whether green instances are passing health checks
consul health service <service-name> -namespace <ns> | \
  jq '.[] | select(.Service.Meta.env == "green") | {ID: .Service.ID, Status: .Checks[].Status}'

# Check cluster membership in Envoy — is the green cluster empty?
oc exec -n <ns> <caller-pod> -c envoy -- \
  curl -s http://localhost:19000/clusters | grep "green"
```

**Cause — Green subset has no healthy instances.**
Consul's default `OnlyPassing` filter excludes instances that are failing or
critical. If all green pods are failing their health checks, the green subset
has zero members and Envoy has no endpoints to send traffic to. Requests
intended for green are silently dropped or fall back, depending on
`PrioritizeByLocality` and failover config.

Fix: ensure green pods pass their Consul health checks before shifting any
weight. See [R3](#r3--health-checks-passing-in-kubernetes-but-failing-in-consul)
if checks are inconsistent between Kubernetes and Consul.

---

### T3 — Traffic split is uneven or non-deterministic

**Symptom:** At low request volumes, observed split ratios do not match
configured weights (e.g., 80/20 weight produces 60/40 observations).

**Cause — Statistical variance at low sample size.**
Consul's weighted splitting is probabilistic. At fewer than ~200 requests the
observed ratio will deviate from the configured weight. This is not a bug.

Fix: Use a higher request volume when verifying split ratios. For smoke testing
at low volumes, use header-based routing to green (see
[Demo 01 Step 4](../demos/01-blue-green/README.md)) instead of relying on
weight-based observation.

---

### T4 — Splitter write fails with validation error

**Symptom:** `consul config write` exits non-zero with a message such as
`"weights must sum to 100"` or `"unknown subset"`.

**Cause A — Weights do not sum to 100.**
All `Weight` values in the `Splits` array must sum to exactly 100. Fractional
weights are allowed (e.g., `99.5` + `0.5`), but the total must be 100.

```hcl
# Invalid — sums to 105
Splits = [
  { Weight = 100, ServiceSubset = "blue"  },
  { Weight = 5,   ServiceSubset = "green" },
]

# Valid
Splits = [
  { Weight = 95, ServiceSubset = "blue"  },
  { Weight = 5,  ServiceSubset = "green" },
]
```

**Cause B — Subset name in the splitter does not match the resolver.**
The `ServiceSubset` values in the splitter must exactly match the subset names
defined in `ServiceResolver.Subsets`. A resolver defining `blue` and `green`
will reject a splitter referencing `v1` and `v2`.

```bash
# Confirm subset names in the resolver
consul config read -kind service-resolver -name <service-name> | grep -A2 Subsets
```

---

### T5 — Header-based routing to green does not work

**Symptom:** `curl -H "X-Backend-Version: v2"` returns a v1 response.

**Diagnosis:**

```bash
# Confirm the ServiceRouter exists and has the correct header match
consul config read -kind service-router -name <service-name>

# Confirm Envoy on the calling pod has the route in its config dump
oc exec -n <ns> <caller-pod> -c envoy -- \
  curl -s http://localhost:19000/config_dump | \
  jq '.. | .routes? // empty | .[] | select(.match.headers != null)'
```

**Cause A — ServiceRouter applied before ServiceResolver.**
The router references subsets that must exist in the resolver. Apply the
resolver first, then the router.

**Cause B — Header match is case-sensitive.**
Envoy header matching is case-insensitive for HTTP/1.1 but the `consul config`
API validates the exact string. Verify the header name casing in the router
matches what the client is sending.

**Cause C — An intermediate proxy (OCP Route, ingress) is stripping the header.**
Test by sending the request directly to the frontend port-forward, bypassing
the OCP Route, to isolate whether the header survives to the sidecar.

---

## Service registration issues

### R1 — Green pods are Running but not appearing in Consul catalog

**Symptom:** `oc get pods` shows green pods as `Running`/`Ready`, but
`consul catalog services` does not list them.

**Diagnosis:**

```bash
# Check connect-injector logs for registration errors
oc logs -n consul -l component=connect-injector --tail=200 | grep -i "error\|failed\|green"

# Confirm the namespace is enabled for connect injection
oc get namespace <ns> -o jsonpath='{.metadata.labels}'
# Must include: consul.hashicorp.com/connect-inject=true

# Check pod annotation is present
oc get pod <green-pod> -n <ns> -o jsonpath='{.metadata.annotations}' | jq
```

**Cause A — Namespace not labelled for connect injection.**
The connect-injector webhook only processes pods in namespaces labelled
`consul.hashicorp.com/connect-inject=true`. If this label is missing, the
webhook is skipped and no Envoy sidecar or Consul registration is created.

```bash
oc label namespace <ns> consul.hashicorp.com/connect-inject=true
```

**Cause B — Pod is missing the `consul.hashicorp.com/connect-inject: "true"` annotation.**
The annotation must be present on the pod spec (not just the Deployment) for
the webhook to inject the sidecar. Add it to the Deployment template:

```yaml
spec:
  template:
    metadata:
      annotations:
        consul.hashicorp.com/connect-inject: "true"
```

**Cause C — connect-injector webhook is failing.**
Check the webhook configuration and injector pod health. A CrashLooping injector
will silently pass pods through without injection.

```bash
oc get mutatingwebhookconfiguration | grep consul
oc get pods -n consul -l component=connect-injector
```

---

### R2 — Green subset is empty in the ServiceResolver

**Symptom:** Green pods are registered in the Consul catalog but
`consul health service <name>` shows no instances matching the green filter.

**Diagnosis:**

```bash
# Inspect the raw service metadata on a green instance
consul catalog nodes -service <service-name> -namespace <ns> | \
  jq '.[] | {ID: .ServiceID, Meta: .ServiceMeta}'

# Compare against the filter expression in the resolver
consul config read -kind service-resolver -name <service-name> | jq '.Subsets'
```

**Cause — Pod annotation key or value does not match the resolver filter.**
The annotation `consul.hashicorp.com/service-meta-env: "green"` maps to
`Service.Meta.env == "green"` in the filter. A typo in either the annotation
key (`service-meta-ENV`) or value (`Green` vs `green`) causes the filter to
match zero instances.

The annotation key must be lowercase and must match the field name used in
the resolver filter exactly.

```yaml
# Correct annotation on the green Deployment
annotations:
  consul.hashicorp.com/service-meta-env: "green"      # lowercase key and value
  consul.hashicorp.com/service-meta-version: "v2.3.0"
```

```hcl
# Matching resolver filter
Subsets = {
  green = { Filter = "Service.Meta.env == green" }
}
```

---

### R3 — Health checks passing in Kubernetes but failing in Consul

**Symptom:** Kubernetes readiness probes are green, but `consul health service`
shows the green instances as `critical` or `warning`.

**Diagnosis:**

```bash
# Show full health check details for green instances
consul health service <service-name> -namespace <ns> | \
  jq '.[] | select(.Service.Meta.env == "green") | .Checks'

# Check the health check configuration on a pod
oc get pod <green-pod> -n <ns> -o jsonpath='{.metadata.annotations}' | \
  jq 'to_entries | map(select(.key | startswith("consul.hashicorp.com/service-check")))'
```

**Cause A — Consul check endpoint differs from Kubernetes probe path.**
The Consul HTTP check is configured separately from the Kubernetes readiness
probe. If the app has a different health endpoint (e.g., `/health/ready` for
Kubernetes vs `/healthz` for Consul), the Consul check may fail while the
probe passes.

```yaml
# Ensure the Consul check annotation uses the correct path for this service
annotations:
  consul.hashicorp.com/service-check-http: "/health"
  consul.hashicorp.com/service-check-interval: "10s"
```

**Cause B — Consul check is timing out before the app is fully initialised.**
Green pods starting a new version may have a longer cold-start time. The
Consul check interval may fire before the app is ready, causing a critical
state that is not cleared. Add an initial delay or lengthen the interval.

**Cause C — Sidecar proxy is not yet ready when the check fires.**
The Envoy sidecar must be ready before inbound traffic is accepted. The
connect-injector adds a `consul.hashicorp.com/connect-inject-status` annotation
when injection is complete. Verify this is `injected` before concluding the
check is wrong.

---

## Config entry issues

### C1 — ServiceResolver write is rejected

**Symptom:** `consul config write service-resolver.hcl` exits non-zero.

**Common rejection reasons:**

| Error message | Cause | Fix |
|---|---|---|
| `"service-router already exists"` | A ServiceRouter referencing this service exists and conflicts with the resolver write order | Delete the router first, write the resolver, then rewrite the router |
| `"invalid filter expression"` | Syntax error in a `Filter` field | Validate with `consul debug` or test the filter against `consul catalog nodes` |
| `"DefaultSubset not found in Subsets"` | `DefaultSubset` value does not match any key in `Subsets` | Ensure the value is an exact match (case-sensitive) |

```bash
# Delete a conflicting router
consul config delete -kind service-router -name <service-name>

# Test a filter expression against live catalog data
consul catalog nodes -service <service-name> -filter 'Service.Meta.env == "green"'
```

---

### C2 — ServiceSplitter write is rejected

See [T4](#t4--splitter-write-fails-with-validation-error) for the two most
common causes (weights not summing to 100, unknown subset name).

Additional rejection: a `ServiceSplitter` cannot be written if no
`ServiceResolver` exists for the same service name. The resolver must exist
first.

```bash
consul config read -kind service-resolver -name <service-name>
# If this returns "Unexpected response code: 404" — write the resolver first
```

---

### C3 — Config entry applied but Envoy has not picked up the change

**Symptom:** `consul config read` shows the new weights, but Envoy's
`/config_dump` still reflects the old routing config seconds or minutes later.

**Diagnosis:**

```bash
# Check how long ago the config entry was updated
consul config read -kind service-splitter -name <service-name> | jq '.ModifyIndex'

# Check xDS stream health on a Consul server
consul debug -duration 30s -output /tmp/consul-debug.tar.gz
# Examine xDS stream errors in the debug bundle

# Check Envoy's xDS stream status on the affected pod
oc exec -n <ns> <pod-name> -c envoy -- \
  curl -s http://localhost:19000/config_dump | \
  jq '.configs[] | select(.["@type"] | contains("BootstrapConfig")) | .bootstrap.dynamic_resources'
```

**Cause A — Consul server is under load or experiencing leader election.**
xDS updates are gated on Raft replication. During a leader election or high
Consul server CPU, update propagation can lag. Monitor Consul server metrics
and alert on `consul.raft.commitTime` p99 > 500ms.

**Cause B — Envoy sidecar has lost its xDS gRPC stream.**
The xDS stream from Envoy to the Consul agent (or control plane) may have
dropped. The sidecar will reconnect, but there is a backoff window during which
updates are not received. Restarting the sidecar container (not the app) forces
stream re-establishment:

```bash
oc exec -n <ns> <pod-name> -c envoy -- kill -HUP 1
# or restart just the envoy container
oc debug node/<node> -- chroot /host -- crictl restart <envoy-container-id>
```

---

### C4 — DefaultSubset is not honoured after splitter deletion

**Symptom:** After `consul config delete -kind service-splitter`, traffic does
not fall back to blue. Instead, requests are load-balanced across all
registered instances of both blue and green.

**Cause:** `DefaultSubset` in the `ServiceResolver` is only the fallback when
the splitter is active but misconfigured. When no splitter exists at all, Consul
routes to all healthy instances of the service regardless of subset. This is
by design.

Fix: Do not rely on splitter deletion as a rollback mechanism. Always maintain
an explicit splitter entry that sends 100% to blue:

```hcl
# Blue-100 splitter — keep this in version control as the rollback artifact
Kind   = "service-splitter"
Name   = "backend"
Splits = [
  { Weight = 100, ServiceSubset = "blue"  },
  { Weight = 0,   ServiceSubset = "green" },
]
```

Apply this file as the rollback action rather than deleting the splitter.

---

## mTLS and intentions issues

### M1 — Green pods return 503 immediately after deployment

**Symptom:** Traffic shifted to green returns HTTP 503. Error appears in Envoy
logs as `upstream connect error or disconnect/reset before headers`.

**Diagnosis:**

```bash
# Check intentions — does green have permission to receive traffic from its callers?
consul intention check <caller-service> <green-service>

# Check Envoy access logs on a green pod for the reset reason
oc logs -n <ns> <green-pod> -c envoy --tail=50 | grep "upstream_reset_before_response_started"

# Confirm the green service has a valid leaf certificate
oc exec -n <ns> <green-pod> -c envoy -- \
  curl -s http://localhost:19000/certs | jq '.certificates[].cert_chain[0].subject_alt_names'
```

**Cause A — No intention permits traffic to the green service.**
Consul's default-deny model blocks all traffic unless an intention explicitly
allows it. Because intentions are bound to the service name (not the subset
or pod IP), a single intention covering the service name applies to both blue
and green instances. If the intention was defined on the blue service name and
the green deployment registers under a different name, a new intention is needed.

```hcl
# Intention covers all subsets of the service — blue and green
Kind = "service-intentions"
Name = "backend"   # service name, not subset name
Sources = [
  { Name = "api", Action = "allow" }
]
```

**Cause B — Green pod's Envoy sidecar is not yet initialised.**
The sidecar startup sequence (certificate issuance, xDS stream establishment)
takes a few seconds. Requests hitting green pods before the sidecar is ready
will be rejected. Kubernetes readiness gates and `consul.hashicorp.com/connect-inject-status`
annotation should prevent traffic before the sidecar is ready, but a pipeline
that shifts weight immediately after `oc rollout status` may still be too fast.

---

### M2 — Intention exists but green still receives connection refused

**Symptom:** `consul intention check` returns `allow`, but green pods still
get connection refused or reset errors from callers.

**Diagnosis:**

```bash
# List all intentions and check for a conflicting deny
consul intention list -namespace <ns> | grep <service-name>

# Check the effective intention result including wildcards
consul intention match -destination <service-name>
```

**Cause — A lower-priority wildcard deny intention is overriding the allow.**
Intentions are evaluated in priority order. A wildcard `deny` (`Name = "*"`)
with a lower priority number than the specific `allow` will block traffic even
when the specific allow appears to match. Verify there is no namespace-wide or
service-wide wildcard deny in place.

```bash
consul intention list -namespace <ns> | jq '.[] | select(.DestinationName == "*" or .SourceName == "*")'
```

---

### M3 — mTLS certificate errors in green pod logs

**Symptom:** Envoy logs show `CERTIFICATE_VERIFY_FAILED` or
`peer certificate unknown` on green pods.

**Cause — Green pods were deployed into a namespace or partition not covered
by the Consul CA.**
Consul's connect CA issues leaf certificates per service identity, scoped to
the namespace and partition. If green pods are deployed into a new namespace
or partition that has not been registered with the CA, certificate issuance
fails and mTLS cannot be established.

```bash
# Confirm the CA is active in the namespace/partition
consul connect ca get-config

# Check connect-inject logs for certificate issuance errors
oc logs -n consul -l component=connect-injector | grep -i "cert\|ca\|tls"
```

Fix: ensure the green namespace/partition is correctly configured with Consul
before deploying workloads. If using Vault as the CA, confirm the Vault
PKI role permits issuance for the green namespace.

---

## Admin partition and namespace issues

### P1 — Green partition cannot reach shared services in blue partition

**Symptom:** Green services that depend on shared infrastructure (databases,
identity providers) receive connection errors after deployment into a separate
partition.

**Diagnosis:**

```bash
# Confirm the exported-services entry exists in the blue partition
consul config read -kind exported-services -name default -partition default

# Verify the green service can see the exported service in its catalog
consul catalog services -partition green
```

**Cause — exported-services config entry is missing or does not include the
target consumer partition.**
By default, services in one partition are invisible to other partitions. An
`exported-services` entry in the source (blue) partition must explicitly list
the green partition as a consumer.

```hcl
Kind      = "exported-services"
Name      = "default"
Partition = "default"   # the blue/source partition

Services = [
  {
    Name      = "postgres-proxy"
    Consumers = [{ Partition = "green" }]
  }
]
```

After applying this entry, the service will appear in `consul catalog services`
within the green partition. A corresponding intention must still permit the
green service to call it.

---

### P2 — ACL token used by CI/CD pipeline is denied in green partition

**Symptom:** `consul config write` or `consul catalog register` fails with
`Permission denied` when the pipeline runs against the green partition.

**Cause — The pipeline ACL token was created in the default (blue) partition
and is not valid in the green partition.**
Tokens in Consul Enterprise are partition-scoped. A token created in the
`default` partition cannot write config entries or register services in the
`green` partition.

Fix: create a dedicated token scoped to the green partition and use it
exclusively for green-partition pipeline operations.

```bash
# Create a policy scoped to the green partition
consul acl policy create \
  -name "green-deploy" \
  -partition "green" \
  -rules @green-deploy-policy.hcl

# Create the token
consul acl token create \
  -description "CI/CD green partition deploy token" \
  -partition "green" \
  -policy-name "green-deploy"
```

If using Vault's Consul secrets engine, create a separate role for the green
partition so that pipeline runs against green receive a partition-scoped token
automatically.

---

### P3 — exported-services change is not visible in green partition

**Symptom:** `exported-services` was updated in the blue partition but the
green partition catalog still does not show the newly exported service.

**Diagnosis:**

```bash
# Confirm the config entry write was replicated
consul config read -kind exported-services -name default -partition default

# Check Consul server logs for replication errors
oc logs -n consul -l component=server | grep -i "export\|replicate\|error"
```

**Cause — Replication lag or a stale read from a non-leader server.**
`exported-services` changes replicate via Raft. On a loaded cluster, a
follower server may serve a stale read for a brief window after the write.

Fix: add `-consistent` to reads during troubleshooting to force the read to
the leader:

```bash
consul catalog services -partition green -consistent
```

If the service still does not appear after 30+ seconds, check Consul server
Raft health (`consul operator raft list-peers`) and confirm the write was
committed to the leader.

---

## Rollback issues

### B1 — Rollback splitter write succeeds but traffic does not return to blue

**Symptom:** The rollback splitter (100% blue, 0% green) is applied
successfully but traffic does not shift back.

**Diagnosis:**

```bash
# Confirm the rollback splitter is actually stored (not a stale cached read)
consul config read -kind service-splitter -name <service-name> -consistent

# Check Envoy config dump on a caller pod — does it show the rollback weights?
oc exec -n <ns> <caller-pod> -c envoy -- \
  curl -s http://localhost:19000/config_dump | \
  jq '.. | .weighted_clusters? // empty'
```

**Cause A — Blue pods have been scaled to zero.**
If blue pods were stopped as part of a premature cleanup step, the blue subset
has no healthy members. The splitter correctly sends 100% to blue, but blue
has nothing to receive it. Immediately scale blue back up:

```bash
oc scale deployment/<blue-deployment> -n <ns> --replicas=<original-count>
```

**Cause B — xDS propagation lag.**
See [C3](#c3--config-entry-applied-but-envoy-has-not-picked-up-the-change).
The rollback write has been accepted by Consul but Envoy on the caller pods
has not yet received the update. Wait 10–15 seconds and recheck the config dump.

---

### B2 — In-flight requests are dropped during rollback

**Symptom:** A spike in 5xx errors or connection resets during the rollback
weight shift, even though both blue and green pods remain Running.

**Cause — Long-lived connections (HTTP keep-alive, gRPC streaming) are not
immediately redirected by a weight change.**
Envoy's weight update is atomic for new connections, but existing TCP/HTTP
keep-alive connections continue to route to their established upstream until
the connection is closed. A weight shift does not tear down in-flight connections.
For short-lived HTTP/1.1 requests this resolves within seconds. For gRPC streams
or long-polling connections, you may need to close the connection explicitly.

**Mitigation for gRPC:** configure a `max_connection_duration` on the upstream
service to bound the lifetime of long-lived connections, so they are
re-established to the correct subset after a weight change:

```hcl
Kind = "service-defaults"
Name = "backend"
Protocol = "grpc"

UpstreamConfig {
  Defaults {
    Limits {
      MaxConnectionDuration = "300s"   # force reconnection every 5 minutes
    }
  }
}
```

---

### B3 — Blue pods have been scaled down before soak period ends

**Symptom:** Rollback is required after green has been running for a while, but
blue has already been stopped. Rollback takes minutes instead of seconds.

**Cause — Blue was treated as disposable rather than as the standing rollback
target.**
The blue/green model's primary advantage is instant rollback because blue
remains allocated and warm. Scaling blue to zero before the soak period ends
eliminates that advantage entirely.

**Operational rule:** Do not stop or scale down blue until:
1. Green has been serving 100% of traffic for the full soak period.
2. All automated and manual validation gates have passed.
3. The on-call team has explicitly approved teardown.

Encode this as a mandatory pipeline stage with a manual approval gate:

```yaml
# Example GitHub Actions stage — requires explicit approval before blue teardown
- name: Teardown blue
  needs: [soak-period-complete]
  environment: production-blue-teardown   # environment with required reviewers
  run: |
    kubectl scale deployment/backend-blue --replicas=0 -n $NAMESPACE
```

---

## Pipeline and automation issues

### A1 — Pipeline shifts weight before green pods are healthy

**Symptom:** Errors spike immediately after the canary weight shift because
green pods are still starting up.

**Cause — Pipeline polls `oc rollout status` but does not wait for Consul
health checks to pass.**
`oc rollout status` reports ready when Kubernetes readiness probes pass. Consul
health checks are evaluated independently by the connect-injector and may take
additional seconds to transition to `passing`. Shifting weight while Consul
health checks are still `critical` sends traffic to a subset with no healthy
members.

**Fix:** Poll Consul health directly before shifting weight:

```bash
# Wait until all green instances are passing in Consul
until consul health service backend -namespace <ns> | \
  jq -e '[.[] | select(.Service.Meta.env == "green") | .Checks[].Status] | all(. == "passing")' \
  > /dev/null 2>&1; do
  echo "Waiting for green health checks..."
  sleep 5
done
echo "Green is healthy — shifting weight"
consul config write splitter-canary-5pct.hcl
```

---

### A2 — consul config write races with Envoy xDS propagation

**Symptom:** The pipeline writes the canary splitter and immediately runs a
validation check. The check samples traffic and sees 0% green because Envoy
has not received the update yet. The pipeline incorrectly concludes green is
unhealthy and triggers an automated rollback.

**Cause — Validation runs too soon after the config write.**
xDS propagation from Consul to all Envoy sidecars is typically sub-second under
normal conditions, but can take several seconds on a loaded cluster or during
control plane operations.

**Fix:** Add an explicit propagation wait between the config write and the
validation sample:

```bash
consul config write splitter-canary-5pct.hcl
echo "Waiting for xDS propagation..."
sleep 15   # conservative; tune down to 5s on a healthy, lightly loaded cluster
# Now sample traffic
```

For tighter control, poll the Envoy config dump on a representative caller pod
and wait until its route config reflects the new weights before sampling.

---

### A3 — Automated rollback trigger fires on a transient spike

**Symptom:** Green is healthy but a brief error spike (e.g., from a downstream
dependency) triggers the automated rollback before the error is resolved.

**Cause — Rollback threshold is evaluated on an instantaneous error rate rather
than a sustained window.**

**Fix:** Gate the rollback trigger on a sustained error rate over a rolling
window, not a point-in-time sample. A 60-second window at p99 is a reasonable
baseline; tune to your SLA.

```bash
# Example: only rollback if error rate exceeds 5% for 60 consecutive seconds
WINDOW=60
THRESHOLD=0.05
START=$(date +%s)

while true; do
  ERROR_RATE=$(query_error_rate_for_green)   # implement using your observability stack
  if (( $(echo "$ERROR_RATE < $THRESHOLD" | bc -l) )); then
    START=$(date +%s)   # reset window on recovery
  fi
  ELAPSED=$(( $(date +%s) - START ))
  if [[ $ELAPSED -ge $WINDOW ]]; then
    echo "Sustained error rate above threshold — rolling back"
    consul config write splitter-blue-100pct.hcl
    exit 1
  fi
  sleep 5
done
```

---

## Related resources

| Resource | Description |
|---|---|
| [Demo 01 — Blue/Green](../demos/01-blue-green/README.md) | Step-by-step walkthrough for this repo |
| [Demo 02 — Canary](../demos/02-canary/README.md) | Weighted traffic shifting with ServiceSplitter |
| [API Gateway Guidance](api-gateway-guidance.md) | North-south ingress, timeouts, rate limiting |
| [Architecture](architecture.md) | Service topology and Consul control plane overview |
| [Consul ServiceResolver docs](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-resolver) | Subset filter reference |
| [Consul ServiceSplitter docs](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-splitter) | Weight configuration reference |
| [Consul Intentions docs](https://developer.hashicorp.com/consul/docs/connect/config-entries/service-intentions) | mTLS access policy reference |
| [Consul Admin Partitions docs](https://developer.hashicorp.com/consul/docs/enterprise/admin-partitions) | Partition topology and exported-services |
