package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
)

// Admission returns the opt-in Pod image rewriting HelmOp. It is independent
// from Harbor, the registry proxy, and the seed role: the only input is the
// operator-wide proxy domain (plus optional credential injection), and there
// is no dependsOn.
func Admission(c Cluster) *fleetv1alpha1.HelmOp {
	op := newHelmOp(AdmissionName(c.Name), c, c.AdmissionChart, "admission-webhook", admissionValues(c), nil)
	// The chart ships a cluster-scoped MutatingWebhookConfiguration, which
	// Fleet refuses inside a targetNamespace. Install without one and let
	// namespaced resources default to TargetNamespace.
	op.Spec.TargetNamespace = ""
	op.Spec.DefaultNamespace = TargetNamespace
	return op
}

// sharedAdmissionExcludedNamespacesTemplate renders the per-Cluster
// mutate-exclure annotation into a real YAML list.
//
// Fleet marshals helm.values to YAML, renders ${ } over the whole document and
// parses the result back into values. "$" is not a YAML indicator, so this
// string stays unquoted in the marshalled document and the rendered [...] is
// parsed as a list. Do not wrap the template in quotes and do not JSON-encode
// splitList: an absent annotation would render [""] instead of [].
//
// missingkey=error applies to field lookups; index on a missing map key
// returns nil, so the if guard is what turns an absent annotation into [].
// quote keeps names with special characters safe inside the flow sequence.
// The chart's static namespaceSelector.matchExpressions are untouched.
const sharedAdmissionExcludedNamespacesTemplate = `${ if index .ClusterAnnotations "p2p.dragonfly.io/mutate-exclure" }[${ range $i, $v := splitList "," (index .ClusterAnnotations "p2p.dragonfly.io/mutate-exclure") }${ if $i },${ end }${ trim $v | quote }${ end }]${ else }[]${ end }`

// SharedAdmission returns the shared admission-webhook HelmOp. It renders the
// per-Cluster mutate-exclure annotation through the Fleet values template above
// so the chart receives a real list; the per-cluster builder keeps the parsed
// list from the operator.
func SharedAdmission(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	c.AdmissionExcludeNamespaces = nil
	values := admissionValues(c)
	values["namespaceSelector"].(map[string]any)["excludedNamespaces"] = sharedAdmissionExcludedNamespacesTemplate
	op := newSharedHelmOp(SharedAdmissionName(), "", c, c.AdmissionChart, "admission-webhook",
		values, nil, AdmissionSelector())
	op.Spec.TargetNamespace = ""
	op.Spec.DefaultNamespace = TargetNamespace
	return op
}

func admissionValues(c Cluster) map[string]any {
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
	return values
}
