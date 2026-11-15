package cluster

import (
	"context"
	"net"
)

// Resolver resolves the per-cluster scheduler host. The default implementation
// uses net.DefaultResolver; tests inject a fake.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// NetResolver is the production DNS resolver.
type NetResolver struct{}

// LookupHost resolves host with net.DefaultResolver.
func (NetResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}
