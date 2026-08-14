# dufflebag Helm chart

This chart installs one dufflebag replica with PostgreSQL, ceph-aio object
storage, and a persistent file-backed Vault:

```sh
helm install dufflebag deploy/helm/dufflebag --create-namespace --namespace dufflebag
```

The chart uses one non-superuser database-owner role. The server applies
migrations at startup without a migration init container.

The instance remains NotReady until its one-shot `POST /sys/init` call succeeds.
Use the in-cluster `dufflebag` Service by default, enable `ingress.enabled` on
plain Kubernetes, or enable `route.enabled` for an edge-terminated OpenShift
Route.

## Vault trust model

This is a lab-grade trust model. The chart initializes Vault with one unseal
share and stores both the unseal key and root token in the namespace Secret
`dufflebag-vault-bootstrap`. Anyone who can read that Secret controls Vault.
The unsealer sidecar deliberately reads it so a restart does not lose access to
the KEK. Production deployments should bring their own independently operated
Vault; this chart is not a production Vault topology.

Deleting the namespace deletes the escrowed credentials. Reusing a retained
Vault PVC without the matching Secret leaves that Vault permanently sealed.

## Values

The values file exposes only settings consumed by the templates: image
repository/tag pairs, per-component resources, persistence sizes, the internal
PostgreSQL and S3 credentials, the S3 bucket, and Route/Ingress toggles and
hosts. The default credentials are suitable only for the chart's isolated lab
namespace and should be overridden anywhere less disposable.
