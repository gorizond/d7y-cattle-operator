# admission-webhook

Opt-in Pod image rewriter for the Dragonfly smart registry proxy. The operator
installs this chart as `<cluster>-admission-webhook` only when the Cluster has
`p2p.dragonfly.io/admission: "true"`; it is never installed with Harbor, the
proxy, or the seed role.

## Rewrite

`MutatingAdmissionWebhook` on Pod `CREATE` rewrites the images of
`containers`, `initContainers`, and `ephemeralContainers`:

| Input | Output |
|---|---|
| any explicit registry (`registry.gitlab.com/...`, `quay.io/...`, `ghcr.io/...`) | `<proxy.domain>/<registry>/<repo>:<tag>` |
| unqualified Docker Hub (`nginx:1.27`, `library/nginx:1.27`) | `<proxy.domain>/docker.io/library/nginx:1.27` |
| proxy or Harbor ref | unchanged |

`@sha256` digests are preserved exactly, and a second admission of an
already-rewritten pod is a no-op. `kube-system`, `fleet-system`, `cattle-*`,
`kube-node-lease`, and `kube-public` are excluded via `namespaceSelector` and
the handler. `failurePolicy` defaults to `Fail`; the chart runs two replicas.

## Credentials (proxy pull path)

Two modes, selected by `networkOnly`:

1. **Network-only (default).** The proxy accepts anonymous pulls from the
   cluster network. No `imagePullSecrets`/`serviceAccountName` are injected.
   This is the recommended mode because it keeps nydus and containerd on the
   same credential-free path.
2. **Injected credentials.** With `networkOnly=false`, set
   `inject.imagePullSecret` and/or `inject.serviceAccountName`. The Secret must
   exist in **every** target namespace (the webhook never copies Secret
   values); the service account is set only from an empty/`default` value to
   avoid breaking user RBAC.

The nydus-snapshotter path receives the same credentials as containerd: both
resolve the pod's `imagePullSecrets`/service account before pulling from
`<proxy.domain>`. If you inject a Secret, create it (or a per-namespace copy
via your secret distribution) in every namespace the webhook covers; otherwise
nydusd falls back to anonymous pulls and fails on private proxy projects.

## TLS

With `tls.secretName` empty the chart generates a CA + serving certificate for
the Service and reuses the Secret across upgrades; `caBundle` is wired into the
`MutatingWebhookConfiguration`. With an external `tls.secretName`, set
`tls.caBundle` to the CA PEM.

## Values

| Key | Default | Purpose |
|---|---|---|
| `proxy.domain` | `""` | public proxy host from operator values; no internal host is hardcoded |
| `networkOnly` | `true` | disable credential injection |
| `inject.imagePullSecret` | `""` | Secret name injected into proxy-pulling pods |
| `inject.serviceAccountName` | `""` | serviceAccountName injected for proxy pulls |
| `failurePolicy` | `Fail` | webhook failure policy |
| `timeoutSeconds` | `5` | admission timeout |
| `namespaceSelector` | excludes system namespaces | extend with opt-in matchLabels/expressions |
| `tls.secretName` / `tls.caBundle` | `""` | external TLS Secret and CA |
