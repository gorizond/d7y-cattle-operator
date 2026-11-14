package cluster

import (
	"fmt"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/condition"
	"k8s.io/apimachinery/pkg/api/equality"
)

const (
	conditionRolesResolved      condition.Cond = "d7y.io/RolesResolved"
	conditionKeyDBReady         condition.Cond = "d7y.io/KeyDBReady"
	conditionControlReady       condition.Cond = "d7y.io/ControlReady"
	conditionDataReady          condition.Cond = "d7y.io/DataReady"
	conditionNydusReady         condition.Cond = "d7y.io/NydusReady"
	conditionRegistered         condition.Cond = "d7y.io/Registered"
	conditionRegistrationFailed condition.Cond = "d7y.io/RegistrationFailed"
	conditionRetrying           condition.Cond = "d7y.io/Retrying"
	conditionIDMismatch         condition.Cond = "d7y.io/IdMismatch"
)

// statusBuilder accumulates all condition changes for one reconcile pass and
// writes them with a single UpdateStatus call. Multiple status updates with the
// same stale resourceVersion would otherwise conflict with each other.
type statusBuilder struct {
	controller *Controller
	original   *provv1.Cluster
	updated    *provv1.Cluster
}

func (c *Controller) newStatus(cluster *provv1.Cluster) *statusBuilder {
	return &statusBuilder{controller: c, original: cluster, updated: cluster.DeepCopy()}
}

func (s *statusBuilder) set(cond condition.Cond, value bool, reason, message string) {
	setCondition(cond, s.updated, value, reason, message)
}

func (s *statusBuilder) rolesResolved(enabled bool, manager, seed bool) {
	if enabled {
		s.set(conditionRolesResolved, true, "RolesResolved",
			fmt.Sprintf("roles resolved: manager=%t seed=%t", manager, seed))
		return
	}
	s.set(conditionRolesResolved, false, "LabelMissing",
		"cluster does not have label "+LabelEnabled+"=true")
}

func (s *statusBuilder) registrationSuccess(id int64, mismatch bool) bool {
	previousMismatch := conditionIDMismatch.GetStatus(s.updated) == "True"
	message := fmt.Sprintf("scheduler cluster ID %d registered", id)
	s.set(conditionRegistered, true, "Registered", message)
	s.set(conditionRegistrationFailed, false, "Registered", message)
	s.set(conditionRetrying, false, "Registered", message)
	if mismatch {
		s.set(conditionIDMismatch, true, "IdMismatch",
			fmt.Sprintf("annotation ID differs from manager server ID %d; annotation preserved", id))
	} else {
		s.set(conditionIDMismatch, false, "IDsMatch", "annotation and manager server IDs match")
	}
	return mismatch && !previousMismatch
}

func (s *statusBuilder) registrationFailure(retrying bool, err error) bool {
	previous := conditionRegistered.GetStatus(s.updated) + conditionRegistrationFailed.GetStatus(s.updated) + conditionRetrying.GetStatus(s.updated)
	reason := "RegistrationFailed"
	if retrying {
		reason = "Retrying"
	}
	message := err.Error()
	s.set(conditionRegistered, false, reason, message)
	s.set(conditionRegistrationFailed, !retrying, reason, message)
	s.set(conditionRetrying, retrying, reason, message)
	s.set(conditionIDMismatch, false, reason, "")
	return previous != conditionRegistered.GetStatus(s.updated)+conditionRegistrationFailed.GetStatus(s.updated)+conditionRetrying.GetStatus(s.updated)
}

func (s *statusBuilder) managedDisabled(reason, message string) {
	s.rolesResolved(false, false, false)
	s.set(conditionKeyDBReady, false, reason, message)
	s.seedUnavailable(reason, message)
	s.seedDisabled()
}

func (s *statusBuilder) nydusDeferred() {
	s.set(conditionNydusReady, false, "NydusDeferred",
		"nydus-snapshotter is deferred: chart 0.0.10 targets containerd 1.x and is incompatible with RKE2 containerd 2.2")
}

func (s *statusBuilder) seedUnavailable(reason, message string) {
	s.set(conditionControlReady, false, reason, message)
	s.set(conditionDataReady, false, reason, message)
	s.set(conditionNydusReady, false, reason, message)
}

func hasManagedStatus(cluster *provv1.Cluster) bool {
	for _, cond := range cluster.Status.Conditions {
		switch condition.Cond(cond.Type) {
		case conditionRolesResolved, conditionKeyDBReady, conditionControlReady,
			conditionDataReady, conditionNydusReady, conditionRegistered,
			conditionRegistrationFailed, conditionRetrying, conditionIDMismatch:
			return true
		}
	}
	return false
}

func (s *statusBuilder) seedDisabled() {
	if conditionRegistered.GetStatus(s.updated) == "" &&
		conditionRegistrationFailed.GetStatus(s.updated) == "" &&
		conditionRetrying.GetStatus(s.updated) == "" &&
		conditionIDMismatch.GetStatus(s.updated) == "" {
		return
	}
	message := "seed role is disabled; the scheduler cluster ID annotation is preserved"
	s.set(conditionRegistered, false, "SeedDisabled", message)
	s.set(conditionRegistrationFailed, false, "SeedDisabled", message)
	s.set(conditionRetrying, false, "SeedDisabled", message)
	s.set(conditionIDMismatch, false, "SeedDisabled", message)
}

// rebase carries the resource version from a registration annotation update so
// the final status write does not conflict with that update.
func (s *statusBuilder) rebase(cluster *provv1.Cluster) {
	if cluster == nil {
		return
	}
	s.updated.ResourceVersion = cluster.ResourceVersion
}

func (s *statusBuilder) apply() (bool, error) {
	if equality.Semantic.DeepEqual(s.original.Status.Conditions, s.updated.Status.Conditions) {
		return false, nil
	}
	if s.controller.clusters == nil {
		return false, nil
	}
	if _, err := s.controller.clusters.UpdateStatus(s.updated); err != nil {
		return false, fmt.Errorf("update Cluster status: %w", err)
	}
	return true, nil
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
