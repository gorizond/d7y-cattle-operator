package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Admission returns the opt-in Pod image rewriting HelmOp. It is independent
// from Harbor, the registry proxy, and the seed role: the only input is the
// operator-wide proxy domain (plus optional credential injection), and there
// is no dependsOn.
func Admission(c Cluster) *fleetv1alpha1.HelmOp {
	values := map[string]any{
		"proxy": map[string]any{"domain": c.ProxyDomain},
		"inject": map[string]any{
			"imagePullSecret":    c.AdmissionImagePullSecret,
			"serviceAccountName": c.AdmissionServiceAccount,
		},
		"networkOnly": c.AdmissionNetworkOnly,
	}
	return newHelmOp(AdmissionName(c.Name), c, c.AdmissionChart, "admission-webhook", values, nil)
}
