# d7y-letsencrypt

Minimal cert-manager resources for the Dragonfly manager edge: one
`ClusterIssuer` (ACME HTTP-01) and one `Certificate`.

The two values that normally change are:

- `email` — ACME account contact (default `i@negash.ru`)
- `server` — ACME directory (default Let's Encrypt production)

The chart stores no secrets. cert-manager creates the ACME account key in its
cluster resource namespace (`accountSecretName`), and the issued certificate
lands in `secretName` in the release namespace.

The solver is pinned to the Traefik `web` entrypoint via
`ingressTemplate.metadata.annotations` so HTTP-01 always uses port 80; this
avoids the Traefik v3.7 `websecure.asDefault` redirect. DNS-01 and wildcards are
not supported — this is an edge-only HTTP-01 chart. The internal manager mTLS
Secret (`dragonfly-p2p-mtls`) is separate and untouched.
