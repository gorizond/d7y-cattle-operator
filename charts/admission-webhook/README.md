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
| any explicit registry (`registry.example.test/...`, `quay.example.test/...`, `ghcr.example.test/...`) | `<proxy.domain>/<registry>/<repo>:<tag>` |
| unqualified Docker Hub (`nginx:1.27`, `library/nginx:1.27`) | `<proxy.domain>/docker.io/library/nginx:1.27` |
| proxy or Harbor ref | unchanged |

`@sha256` digests are preserved exactly, and a second admission of an
already-rewritten pod is a no-op. `kube-system`, `fleet-system`, `cattle-*`,
`kube-node-lease`, and `kube-public` are excluded via `namespaceSelector` and
the handler. The Helm release namespace is always appended to the selector from
`.Release.Namespace`, so the webhook cannot block its own Deployment. Extra
namespaces come from the Cluster annotation
`p2p.dragonfly.io/mutate-exclure` (comma-separated); the operator resolves it
into `namespaceSelector.excludedNamespaces` and no custom expression is
rendered when the list is empty. `failurePolicy` defaults to `Fail`; the chart
runs two replicas.

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

With `tls.secretName` empty the chart generates a CA + serving certificate and
reuses the Secret across upgrades; `caBundle` is wired into the
`MutatingWebhookConfiguration`. The certificate SANs cover the Service
(`.Release.Name`, `.Release.Name.<namespace>`, and the `.svc` /
`.svc.cluster.local` forms) plus the legacy TLS Secret name, because the
webhook `clientConfig.service.name` is the Service. The SAN layout is marked in
the Secret, so an upgrade from chart 0.1.0 reissues the certificate once. No
alias Service is created. With an external `tls.secretName`, set
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
| `namespaceSelector.excludedNamespaces` | `[]` | extra namespaces from `p2p.dragonfly.io/mutate-exclure`; empty renders no custom `NotIn` |
| `namespaceSelector` | excludes system namespaces | extend with opt-in matchLabels/expressions; the release namespace is always excluded |
| `tls.secretName` / `tls.caBundle` | `""` | external TLS Secret and CA |
