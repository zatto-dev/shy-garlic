# Shy Garlic Helm chart

Deploys [Shy Garlic](https://github.com/zatto-dev/shy-garlic), a self-hosted homelab
dashboard. The app keeps its state in SQLite and edits its own YAML config, so it
always runs as **one replica** with a persistent volume (there is no `replicaCount`).

## Install

The chart is published as an OCI artifact; chart version and app version match the
release tag.

```bash
helm install shy-garlic oci://ghcr.io/zatto-dev/charts/shy-garlic \
  --version <x.y.z> --namespace shy-garlic --create-namespace
```

The first visit to `/setup` creates the administrator account (or see
[Admin account from a Secret](#admin-account-from-a-secret)).

## Upgrade

```bash
helm upgrade shy-garlic oci://ghcr.io/zatto-dev/charts/shy-garlic \
  --version <x.y.z> --namespace shy-garlic --reuse-values
```

Upgrading never overwrites the dashboard config on the volume. `helm uninstall`
keeps the chart-created PersistentVolumeClaim, so data survives an accident.

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/zatto-dev/shy-garlic` | Image repository. |
| `image.tag` | `""` | Empty = the chart's `appVersion`. Pin a release, do not use `latest`. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Pull secrets. |
| `nameOverride` / `fullnameOverride` | `""` | Name overrides. |
| `persistence.existingClaim` | `""` | Use a claim you created instead of letting the chart create one. |
| `persistence.storageClass` | `""` | `""` = default StorageClass, `"-"` = no class (static PV). A cluster without a default StorageClass leaves the pod Pending. |
| `persistence.accessModes` | `[ReadWriteOnce]` | PVC access modes. |
| `persistence.size` | `1Gi` | PVC size. |
| `persistence.annotations` | `{}` | PVC annotations. |
| `config.initial` | `""` | Initial config, used only when the config file does not exist yet. Empty = the bundled example. The file on the volume is the source of truth afterwards. |
| `admin.existingSecret` | `""` | Existing Secret with the admin credentials; takes precedence over `username`/`password`. |
| `admin.existingSecretUserKey` | `SHYG_ADMIN_USER` | Key holding the user name. |
| `admin.existingSecretPasswordKey` | `SHYG_ADMIN_PASSWORD` | Key holding the password. |
| `admin.username` / `admin.password` | `""` | Let the chart create the Secret (both required). Prefer `existingSecret`: values end up in Helm release history. |
| `allowedHosts` | `[]` | Host names the panel answers to (`SHYG_ALLOWED_HOSTS`); other `Host` headers get `421` (DNS rebinding protection). `/healthz` is exempt for probes; `/metrics` is not, so a scraper must send an allowed `Host` or be added here. |
| `trustedProxy` | `""` | Comma-separated CIDRs of the ingress/proxy allowed to set `X-Forwarded-For`/`-Proto`. Set it when using `ingress` or `httpRoute`. |
| `extraEnv` | `[]` | Extra environment variables (`SHYG_WEBHOOK`, `SHYG_ICONS`, ...). No credentials here. |
| `extraEnvFrom` | `[]` | Extra `envFrom` sources, e.g. a Secret. |
| `service.type` | `ClusterIP` | Service type. |
| `service.port` | `80` | Service port. |
| `service.annotations` | `{}` | Service annotations. |
| `ingress.enabled` | `false` | Create an Ingress. |
| `ingress.className` | `""` | Ingress class. |
| `ingress.annotations` | `{}` | Ingress annotations. |
| `ingress.hosts` | `[{host: shyg.example.com, ...}]` | Hosts and paths. |
| `ingress.tls` | `[]` | TLS entries. |
| `httpRoute.enabled` | `false` | Create a Gateway API HTTPRoute (needs the CRDs and a Gateway). |
| `httpRoute.annotations` | `{}` | HTTPRoute annotations. |
| `httpRoute.parentRefs` | `[]` | Gateways to attach to. |
| `httpRoute.hostnames` | `[]` | Hostnames. |
| `podSecurityContext` | restricted profile, uid/gid/fsGroup `65532` | Pod security context. |
| `securityContext` | no privilege escalation, read-only root FS, drop `ALL` | Container security context. |
| `resources` | requests `20m`/`64Mi`, memory limit `256Mi` | Resources. |
| `probes.startup` | `failureThreshold: 30`, `periodSeconds: 2` | Startup probe (`GET /healthz`). |
| `probes.liveness` | `periodSeconds: 20`, `timeoutSeconds: 3` | Liveness probe. |
| `probes.readiness` | `periodSeconds: 10`, `timeoutSeconds: 3` | Readiness probe. |
| `podAnnotations` / `podLabels` | `{}` | Extra pod metadata. |
| `nodeSelector` / `tolerations` / `affinity` | `{}` / `[]` / `{}` | Scheduling. |

## Examples

### Your own initial config

Seed the dashboard from a local file. It is used only on first start, when the
config file does not exist on the volume yet; later edits happen in the panel
(Settings → File).

```bash
helm install shy-garlic oci://ghcr.io/zatto-dev/charts/shy-garlic \
  --version <x.y.z> --set-file config.initial=./shygarlic.yaml
```

The chart stores it in a ConfigMap (`<release>-shy-garlic-seed`) that is copied to
the volume when the config file is missing.

### Admin account from a Secret

```bash
kubectl create secret generic shyg-admin \
  --from-literal=SHYG_ADMIN_USER=admin \
  --from-literal=SHYG_ADMIN_PASSWORD='change-me'
```

```yaml
admin:
  existingSecret: shyg-admin
```

### Ingress

```yaml
trustedProxy: "10.244.0.0/16"   # your ingress controller / pod CIDR
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: shyg.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: shyg-tls
      hosts: [shyg.example.com]
```

### Gateway API (HTTPRoute)

```yaml
trustedProxy: "10.244.0.0/16"
httpRoute:
  enabled: true
  parentRefs:
    - name: my-gateway
      namespace: gateway-system
  hostnames: [shyg.example.com]
```

### Persistence

```yaml
persistence:
  storageClass: fast-ssd
  size: 2Gi
```

Or reuse an existing claim:

```yaml
persistence:
  existingClaim: shyg-data
```
