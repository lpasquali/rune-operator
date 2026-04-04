# Rootless / Read-Only Deployment

This guide explains how to run the `rune-operator` in a hardened pod that:

- runs as a **non-root** user (UID 65534, `nobody`)
- mounts the root filesystem as **read-only**
- drops **all** Linux capabilities
- prevents privilege escalation

These settings satisfy the Kubernetes **Restricted** Pod Security Standard (PSS) and are required for UAT environments that enforce rootless deployments.

## Helm values

Add (or override) the following in your `values.yaml` or via `--set`:

```yaml
securityContext:
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  runAsNonRoot: true
  runAsUser: 65534
  capabilities:
    drop:
      - ALL

podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65534
  seccompProfile:
    type: RuntimeDefault
```

The chart ships these as the **default values**, so no extra configuration is needed for standard deployments.

## Verifying the deployment

```bash
# Confirm the container is running as UID 65534.
kubectl exec -n <namespace> deploy/rune-operator -- id

# Confirm the root FS is read-only.
kubectl exec -n <namespace> deploy/rune-operator -- touch /test-write 2>&1
# Expected: touch: /test-write: Read-only file system
```

## Temporary directories

The operator does not write to the filesystem at runtime. If a future dependency
requires a writable path add an `emptyDir` volume:

```yaml
volumes:
  - name: tmp
    emptyDir: {}

volumeMounts:
  - name: tmp
    mountPath: /tmp
```

## Pod Security Standards

Apply the **Restricted** policy to the operator namespace:

```bash
kubectl label namespace rune-operator \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest
```
