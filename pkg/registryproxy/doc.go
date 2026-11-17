// Package registryproxy implements the smart OCI registry proxy that fronts
// Harbor for Dragonfly/nydus target clusters.
//
// The v1 server is read-only and exposes the OCI distribution endpoints, the
// Harbor webhook conversion endpoint, health/readiness, and metrics. Dynamic
// Harbor project/webhook creation, source copy, and nydus conversion are
// implemented in later tasks; this package currently provides only the
// skeleton and shared package boundary.
package registryproxy
