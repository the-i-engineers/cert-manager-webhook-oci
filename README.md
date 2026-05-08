# cert-manager-webhook-oci

A [cert-manager](https://cert-manager.io) ACME DNS-01 webhook solver for [Oracle Cloud Infrastructure (OCI) DNS](https://www.oracle.com/cloud/networking/dns/).

This webhook enables cert-manager to solve [ACME DNS-01 challenges](https://letsencrypt.org/docs/challenge-types/#dns-01-challenge) by creating and deleting TXT records in OCI DNS zones.

> **Fork note**: This project is based on [thpham/cert-manager-webhook-oci](https://github.com/thpham/cert-manager-webhook-oci) with the following improvements:
> - Fixed: `compartmentOCID` is now correctly passed to all OCI DNS API calls (previously missing, causing 404 errors when using scoped IAM policies)
> - Updated: cert-manager v1.20.x, OCI Go SDK v65.90+, Go 1.22
> - Updated: Helm chart supports Pod Security Standards (restricted) via `securityContext` defaults
> - Added: `serviceAccountAnnotations` Helm value for IRSA / workload identity annotations

## Prerequisites

- cert-manager ≥ v1.10 installed in your cluster
- An OCI DNS zone managed in your tenancy
- IAM permissions for the webhook to create/delete DNS TXT records (see [Authentication](#authentication))

## Installation

### Helm

```bash
helm repo add cert-manager-webhook-oci https://the-i-engineers.github.io/cert-manager-webhook-oci
helm repo update
helm install cert-manager-webhook-oci cert-manager-webhook-oci/cert-manager-webhook-oci \
  --namespace cert-manager \
  --set groupName=acme.example.com \
  --set ociResourcePrincipal.region=<your-oci-region>
```

### Values

| Value | Default | Description |
|-------|---------|-------------|
| `groupName` | `acme.example.com` | Unique group name for this webhook — must match the `groupName` in your ClusterIssuer |
| `image.repository` | `ghcr.io/the-i-engineers/cert-manager-webhook-oci` | Docker image repository |
| `image.tag` | `latest` | Docker image tag |
| `ociProfileSecretNames` | `[]` | OCI credential secret names; empty = use OKE Workload Identity |
| `ociResourcePrincipal.version` | `"2.2"` | OCI resource principal version |
| `ociResourcePrincipal.region` | `""` | OCI region (required when using Workload Identity) |
| `serviceAccountAnnotations` | `{}` | Annotations for the webhook ServiceAccount |
| `certManager.namespace` | `cert-manager` | Namespace where cert-manager is installed |
| `certManager.serviceAccountName` | `cert-manager` | cert-manager ServiceAccount name |

## Authentication

### Option A: OKE Workload Identity (recommended for OKE clusters)

When running on Oracle Container Engine for Kubernetes (OKE), the webhook can use [Workload Identity](https://docs.oracle.com/en-us/iaas/Content/ContEng/Tasks/contenggrantingworkloadaccesstoresources.htm) to authenticate without managing static credentials.

1. **Create a Dynamic Group** matching the cert-manager-webhook-oci pod:

   ```
   Any {all {resource.type='workload', resource.namespace='cert-manager', resource.cluster_id='<your-cluster-ocid>'}}
   ```

2. **Create an IAM Policy** granting the dynamic group DNS record management in the compartment containing your DNS zone:

   ```
   Allow dynamic-group <your-dynamic-group-name> to manage dns in compartment <your-dns-compartment-name>
   ```

3. **Set the region** in Helm values:

   ```yaml
   ociProfileSecretNames: []
   ociResourcePrincipal:
     version: "2.2"
     region: <your-oci-region>  # e.g., eu-frankfurt-1
   ```

### Option B: API Key authentication

Create a Kubernetes Secret with OCI API key credentials:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: oci-profile
  namespace: cert-manager
type: Opaque
stringData:
  tenancy: "<tenancy-ocid>"
  user: "<user-ocid>"
  region: "<region>"
  fingerprint: "<key-fingerprint>"
  privateKey: |
    -----BEGIN RSA PRIVATE KEY-----
    ...
    -----END RSA PRIVATE KEY-----
  privateKeyPassphrase: ""
```

Then reference it in Helm values:

```yaml
ociProfileSecretNames:
  - oci-profile
```

## ClusterIssuer configuration

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-staging
spec:
  acme:
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-staging-key
    solvers:
      - dns01:
          webhook:
            groupName: acme.example.com  # must match Helm value
            solverName: oci
            config:
              compartmentOCID: "<compartment-ocid-containing-dns-zone>"
              # ociProfileSecretName: oci-profile  # only needed for Option B
```

### Important: `compartmentOCID`

The `compartmentOCID` in the ClusterIssuer config **must** be the OCID of the compartment that contains the OCI DNS zone. This is required for the webhook to correctly scope DNS API calls to the right compartment. Without it, OCI will attempt a tenancy-wide zone name lookup, which may fail with 404 if IAM policies are scoped to a compartment.

## Development

### Building locally

```bash
make build-local
```

### Running tests

Tests require `kubebuilder` test tools. See `Makefile` for details.

```bash
export TEST_ZONE_NAME=example.com.
export OCI_COMPARTMENT_OCID=<your-compartment-ocid>
make test
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
