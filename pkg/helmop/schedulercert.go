package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// SchedulerCertName returns the per-seed scheduler certificate HelmOp name.
// The legacy raw Bundle already owns <cluster>-p2p-scheduler, and Fleet names a
// HelmOp-generated Bundle after the HelmOp. A suffix keeps the migration free
// of a name collision while the old Bundle is removed after the new release is
// ready.
func SchedulerCertName(cluster string) string {
	return objectName(cluster, "p2p-scheduler-cert")
}

// SchedulerCert returns the per-seed HelmOp that installs the scheduler TLS
// Secret. The Secret name and keys are fixed so the control release keeps
// mounting the same paths.
func SchedulerCert(c Cluster, crt, key, ca string) *fleetv1alpha1.HelmOp {
	op := newHelmOp(SchedulerCertName(c.Name), c, c.SchedulerCertChart, "scheduler-cert",
		map[string]any{
			"tls": map[string]any{
				"crt": crt,
				"key": key,
				"ca":  ca,
			},
		}, nil)
	// Fleet copies spec.labels to the generated Bundle. The shared control
	// depends on SchedulerCertLabel, so it waits for this HelmOp's Bundle
	// rather than the legacy raw Bundle.
	op.Spec.Labels = map[string]string{
		"p2p-dragonfly-io":                   "p2p-mtls",
		SchedulerCertLabel:                   "true",
		"p2p.dragonfly.io/cluster-name":      c.Name,
		"p2p.dragonfly.io/cluster-namespace": c.Namespace,
		"app.kubernetes.io/managed-by":       "d7y-cattle-operator",
	}
	return op
}
