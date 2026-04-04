# Vault Integration

The operator chart supports **optional** HashiCorp Vault integration via the
Vault Agent Injector (sidecar) pattern. This is an opt-in feature; the default
deployment uses standard Kubernetes Secrets.

## Architecture

```
Pod
├── manager (rune-operator)          ← reads token from projected volume
└── vault-agent (sidecar, injected)  ← fetches secret from Vault, writes to shared tmpfs
```

The Vault Agent authenticates with Vault using Kubernetes Auth, fetches the
configured secret, and writes it to a shared `emptyDir` volume mounted by the
operator container.

## Prerequisites

1. [Vault Agent Injector](https://developer.hashicorp.com/vault/docs/platform/k8s/injector) installed in the cluster.
2. A Kubernetes Auth method configured in Vault:

```bash
vault auth enable kubernetes
vault write auth/kubernetes/config \
  kubernetes_host="https://$KUBERNETES_PORT_443_TCP_ADDR:443"
vault write auth/kubernetes/role/rune-operator \
  bound_service_account_names=rune-operator \
  bound_service_account_namespaces=<namespace> \
  policies=rune-operator \
  ttl=1h
```

3. A Vault policy that allows reading the secret:

```hcl
path "secret/data/rune/api-token" {
  capabilities = ["read"]
}
```

## Helm values

```yaml
vault:
  enabled: true
  address: "https://vault.example.com:8200"
  role: "rune-operator"
  secretPath: "secret/data/rune/api-token"
```

When `vault.enabled: true` the chart creates:

- A `VaultAuth` CRD resource (requires the Vault Secrets Operator or the
  Vault Agent Injector annotations to be wired up).
- Pod annotations that instruct the Vault Agent Injector to mount the secret.

## Keeping standard Secrets as default

`vault.enabled` defaults to `false`.  Standard Kubernetes Secrets (created via
`kubectl create secret` or SOPS-encrypted Helm values) remain the default and
recommended path for most deployments.

## Troubleshooting

```bash
# Check Vault Agent sidecar logs.
kubectl logs -n <namespace> deploy/rune-operator -c vault-agent

# Verify the projected secret volume is populated.
kubectl exec -n <namespace> deploy/rune-operator -c manager -- cat /vault/secrets/token
```
