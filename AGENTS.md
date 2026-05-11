# AGENTS.md

## Purpose and Scope

This repo is a [cert-manager](https://cert-manager.io) ACME DNS-01 webhook solver for [Oracle Cloud Infrastructure (OCI) DNS](https://www.oracle.com/cloud/networking/dns/). It implements the cert-manager `v1alpha1` solver interface so cert-manager can create and delete DNS TXT records in OCI DNS zones to satisfy ACME DNS-01 challenges.

The repo is **public** and self-contained. All documentation (README, this file) must remain generic and free of internal infrastructure references.

## Repository Shape

```
main.go                            # all solver logic (Present, CleanUp, Initialize, ociDNSClient)
main_test.go                       # integration tests using kubebuilder test env
Makefile                           # build, test, render-manifest targets
Dockerfile                         # multi-arch image build
go.mod / go.sum
pkg/                               # support packages (if any)
testdata/oci/                      # sample config files for tests (templated with envsubst)
deploy/
  cert-manager-webhook-oci/        # Helm chart
    Chart.yaml
    values.yaml
    templates/                     # deployment, service, rbac, clusterrole, issuer, cert
.github/
  workflows/
    ci.yml                         # build + vet on every PR
    lint-pr-title.yml              # Conventional Commits PR title check
    daily-tag-release.yml          # daily semver tag via PAT → fires release.yml
    release.yml                    # builds Docker image + Helm chart, publishes to GHCR + gh-pages
  release.yml                      # release notes category config for GitHub Releases
  dependabot.yml                   # Actions daily, Go weekly
```

## Key Code Paths

### Solver entry points (`main.go`)

- **`Present(ch)`** — called by cert-manager to add a DNS TXT record for a challenge. Calls `ociDNSClient()` then `PatchZoneRecords` with `RecordOperationOperationAdd`.
- **`CleanUp(ch)`** — removes the TXT record after the challenge is validated. Same flow with `RecordOperationOperationRemove`.
- **`Initialize(kubeClientConfig, stopCh)`** — sets up the in-cluster Kubernetes client (`c.client`) used to read OCI credential secrets.

### `patchRequest()` (`main.go`)

Builds a `dns.PatchZoneRecordsRequest`. Key points:
- Strips trailing `.` from `ch.ResolvedZone` and `ch.ResolvedFQDN` — OCI API expects names without trailing dots.
- Sets `CompartmentId` from `cfg.CompartmentOCID` **only if non-empty** — this scopes the zone lookup to the correct compartment. Without it, OCI performs a tenancy-wide lookup, which will fail if IAM policies are compartment-scoped.
- TTL is hardcoded to 60 seconds.

### `ociDNSClient()` — authentication (`main.go`)

Two-step auth:
1. **API key (OCI profile secret)**: if `ociProfileSecretName` is set in the ClusterIssuer config, looks up that Kubernetes Secret in the **pod's own namespace** (`POD_NAMESPACE` env var, injected via downwardAPI in `deployment.yaml`). Falls back to the challenge namespace (`ch.ResourceNamespace`) if `POD_NAMESPACE` is unset.
2. **OKE Workload Identity**: if the secret lookup fails, falls back to `auth.OkeWorkloadIdentityConfigurationProvider()`. If both fail, returns an error.

`POD_NAMESPACE` is injected via `fieldRef: metadata.namespace` in the Helm chart's deployment template — do not remove it.

## Authentication Summary

| Mode | When | Required k8s RBAC |
|------|------|-------------------|
| API key (secret) | `ociProfileSecretName` set in ClusterIssuer + secret present | Role in pod namespace (`ociProfileSecretNames` Helm value) |
| OKE Workload Identity | no secret configured, or secret not found | OCI Dynamic Group + IAM Policy only |

For API key mode: the Helm value `ociProfileSecretNames` (list) must include the secret name — this controls which secret names appear in the generated RBAC Role's `resourceNames`. If `ociProfileSecretNames` is empty (`[]`), no Role is created and the secret lookup will always fail silently, falling back to Workload Identity.

## Helm Chart (`deploy/cert-manager-webhook-oci/`)

Key values in `values.yaml`:

| Value | Purpose |
|-------|---------|
| `groupName` | Must match `groupName` in ClusterIssuer — identifies this solver |
| `ociProfileSecretNames` | Controls RBAC generation; empty = no Role, relies on Workload Identity |
| `ociResourcePrincipal.region` | Required for Workload Identity |
| `serviceAccountAnnotations` | For IRSA / workload identity SA annotations |

The Helm chart templates create: Deployment, Service, ServiceAccount, ClusterRole (for cert-manager ACME solver), Role/RoleBinding (for OCI credential secrets, conditional on `ociProfileSecretNames`), self-signed Issuer, and CA/serving Certificate.

## Developer Workflows

### Build and vet

```bash
go build ./...
go vet ./...
```

### Local Docker build (single arch)

```bash
make build-local
```

### Run tests

Tests use [kubebuilder test tools](https://book.kubebuilder.io/reference/envtest.html) (etcd + kube-apiserver binaries).

```bash
# Set required env vars
export TEST_ZONE_NAME=example.com.
export OCI_COMPARTMENT_OCID=<your-compartment-ocid>

make test          # downloads kubebuilder tools to _test/ on first run
```

The `make test` target calls `envsubst` on `testdata/oci/config.json.sample` and `testdata/oci/oci-profile.yaml.sample` to produce config fixtures. Clean up with `make clean`.

### Render Helm manifest

```bash
make rendered-manifest.yaml   # writes deploy/rendered-manifest.yaml
```

Useful for reviewing chart output before submitting Helm chart changes.

## CI / Release Pipeline

| Workflow | Trigger | What it does |
|----------|---------|-------------|
| `ci.yml` | PR to `main` | `go build ./...`, `go vet ./...` |
| `lint-pr-title.yml` | PR to `main` | Enforces Conventional Commits PR title |
| `daily-tag-release.yml` | Cron 03:30 UTC + `workflow_dispatch` | Creates semver tag via `IENGINEER_API_TOKEN` (PAT); PAT-pushed tag fires `release.yml` via `push.tags` |
| `release.yml` | `push: tags: v*` + `workflow_dispatch` | Builds multi-arch Docker image → pushes to GHCR; packages Helm chart → publishes to gh-pages |

**Bump rules** (Conventional Commits on commits since last tag):

| Pattern | Bump |
|---------|------|
| `type!:` or `BREAKING CHANGE:` in footer | major |
| `feat:` / `feat(scope):` | minor |
| `fix:`, `chore:`, `ci:`, `docs:`, others | patch |

## Agent Change Checklist

Before pushing any change:

- [ ] `go build ./...` — must succeed
- [ ] `go vet ./...` — must be clean
- [ ] If touching `main.go`: verify `ociDNSClient()` still handles both auth paths; check namespace logic around `POD_NAMESPACE`
- [ ] If touching the Helm chart: run `make rendered-manifest.yaml` and review the diff; ensure `ociProfileSecretNames` RBAC conditional logic is intact
- [ ] If adding/removing Helm values: update `values.yaml` defaults and the `README.md` values table
- [ ] PR title follows Conventional Commits (`type(scope): description`) — enforced by `lint-pr-title.yml`
- [ ] Never commit secrets, OCI credentials, or internal infrastructure names
