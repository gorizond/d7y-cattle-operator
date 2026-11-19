package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Admission returns the opt-in Pod image rewriting HelmOp. It is independent
// from Harbor, the registry proxy, and the seed role: the only input is the
// operator-wide proxy domain (plus optional credential injection), and there
// is no dependsOn.
func Admission(c Cluster) *fleetv1alpha1.HelmOp {
	excludedNamespaces := c.AdmissionExcludeNamespaces
	if excludedNamespaces == nil {
		excludedNamespaces = []string{}
	}
	values := map[string]any{
		"proxy": map[string]any{"domain": c.ProxyDomain},
		"inject": map[string]any{
			"imagePullSecret":    c.AdmissionImagePullSecret,
			"serviceAccountName": c.AdmissionServiceAccount,
		},
		"networkOnly": c.AdmissionNetworkOnly,
		"namespaceSelector": map[string]any{
			"excludedNamespaces": excludedNamespaces,
		},
	}
	if c.OperatorImageRepository != "" && c.OperatorImageTag != "" {
		values["image"] = map[string]any{
			"repository": c.OperatorImageRepository,
			"tag":        c.OperatorImageTag,
		}
	}
	op := newHelmOp(AdmissionName(c.Name), c, c.AdmissionChart, "admission-webhook", values, nil)
	// The chart ships a cluster-scoped MutatingWebhookConfiguration, which
	// Fleet refuses inside a targetNamespace. Install without one and let
	// namespaced resources default to TargetNamespace.
	op.Spec.TargetNamespace = ""
	op.Spec.DefaultNamespace = TargetNamespace
	return op
}
