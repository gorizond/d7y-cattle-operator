package cluster

import (
	"fmt"
	"log/slog"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/condition"
	"k8s.io/apimachinery/pkg/api/equality"
)

const (
	conditionRegistered         condition.Cond = "Registered"
	conditionRegistrationFailed condition.Cond = "RegistrationFailed"
	conditionRetrying           condition.Cond = "Retrying"
	conditionIDMismatch         condition.Cond = "IdMismatch"
)

func (c *Controller) markRegistered(cluster *provv1.Cluster, id int64, mismatch bool) bool {
	updated := cluster.DeepCopy()
	message := fmt.Sprintf("scheduler cluster ID %d registered", id)
	setCondition(conditionRegistered, updated, true, "Registered", message)
	setCondition(conditionRegistrationFailed, updated, false, "Registered", message)
	setCondition(conditionRetrying, updated, false, "Registered", message)
	if mismatch {
		setCondition(conditionIDMismatch, updated, true, "IdMismatch",
			fmt.Sprintf("annotation ID differs from manager server ID %d; annotation preserved", id))
	} else {
		setCondition(conditionIDMismatch, updated, false, "IDsMatch", "annotation and manager server IDs match")
	}
	return c.updateRegistrationStatus(cluster, updated)
}

func (c *Controller) markRegistrationFailure(cluster *provv1.Cluster, retrying bool, err error) bool {
	updated := cluster.DeepCopy()
	reason := "RegistrationFailed"
	if retrying {
		reason = "Retrying"
	}
	message := err.Error()
	setCondition(conditionRegistered, updated, false, reason, message)
	setCondition(conditionRegistrationFailed, updated, !retrying, reason, message)
	setCondition(conditionRetrying, updated, retrying, reason, message)
	setCondition(conditionIDMismatch, updated, false, reason, "")
	return c.updateRegistrationStatus(cluster, updated)
}

func (c *Controller) markSeedDisabled(cluster *provv1.Cluster) bool {
	if conditionRegistered.GetStatus(cluster) == "" {
		return false
	}
	updated := cluster.DeepCopy()
	message := "seed role is disabled; the scheduler cluster ID annotation is preserved"
	setCondition(conditionRegistered, updated, false, "SeedDisabled", message)
	setCondition(conditionRegistrationFailed, updated, false, "SeedDisabled", message)
	setCondition(conditionRetrying, updated, false, "SeedDisabled", message)
	setCondition(conditionIDMismatch, updated, false, "SeedDisabled", message)
	return c.updateRegistrationStatus(cluster, updated)
}

func (c *Controller) updateRegistrationStatus(original, updated *provv1.Cluster) bool {
	if equality.Semantic.DeepEqual(original.Status.Conditions, updated.Status.Conditions) {
		return false
	}
	if c.clusters == nil {
		return true
	}
	if _, err := c.clusters.UpdateStatus(updated); err != nil {
		slog.Warn("failed to update Cluster registration status",
			"cluster", original.Namespace+"/"+original.Name,
			"error", err,
		)
	}
	return true
}

func setCondition(cond condition.Cond, cluster *provv1.Cluster, value bool, reason, message string) {
	if value {
		cond.True(cluster)
	} else {
		cond.False(cluster)
	}
	cond.Reason(cluster, reason)
	cond.Message(cluster, message)
}
