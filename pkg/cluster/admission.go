package cluster

import (
	"errors"
	"fmt"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
)

// ensureAdmission reconciles the independent admission-webhook HelmOp. It has
// no cross-toggle dependency: the only required input is the operator-wide
// proxy.domain, and no Harbor/proxy/seed state is consulted.
func (c *Controller) ensureAdmission(cluster *provv1.Cluster, hc helmop.Cluster, roles Roles) error {
	if !roles.Admission {
		name := helmop.AdmissionName(cluster.Name)
		var errs []error
		// Delete the HelmOp first so Fleet cannot recreate the Bundle, then
		// remove the Bundle explicitly. Fleet's Bundle finalizer purges the
		// BundleDeployment and the agent uninstalls the Helm release.
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
		if err := c.deleteBundle(cluster, name); err != nil {
			errs = append(errs, err)
		}
		if err := errors.Join(errs...); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "AdmissionCleanupFailed", err.Error())
			return err
		}
		return nil
	}
	if strings.TrimSpace(hc.ProxyDomain) == "" {
		err := fmt.Errorf("admission webhook requires the proxy.domain operator value")
		c.event(cluster, corev1.EventTypeWarning, "AdmissionProxyDomainMissing", err.Error())
		return err
	}
	return c.applyHelmOp(cluster, helmop.Admission(hc))
}

func setAdmissionCondition(status *statusBuilder, roles Roles, err error, ready bool, message string) {
	if !roles.Admission {
		status.set(conditionAdmissionWebhookReady, false, "Disabled", "admission webhook is not enabled")
		return
	}
	if err != nil {
		status.set(conditionAdmissionWebhookReady, false, "ApplyFailed", err.Error())
		return
	}
	if !ready {
		if message == "" {
			message = "admission webhook HelmOp is not Ready"
		}
		status.set(conditionAdmissionWebhookReady, false, "Progressing", message)
		return
	}
	status.set(conditionAdmissionWebhookReady, true, "Ready", "admission webhook HelmOp Ready")
}
