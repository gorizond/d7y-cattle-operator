package cluster

import (
	"fmt"
	"log/slog"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/condition"
	"k8s.io/apimachinery/pkg/api/equality"
)

const (
	conditionRolesResolved      condition.Cond = "RolesResolved"
	conditionKeyDBReady         condition.Cond = "KeyDBReady"
	conditionControlReady       condition.Cond = "ControlReady"
	conditionDataReady          condition.Cond = "DataReady"
	conditionNydusReady         condition.Cond = "NydusReady"
	conditionRegistered         condition.Cond = "Registered"
	conditionRegistrationFailed condition.Cond = "RegistrationFailed"
	conditionRetrying           condition.Cond = "Retrying"
	conditionIDMismatch         condition.Cond = "IdMismatch"
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

func (s *statusBuilder) apply() bool {
	if equality.Semantic.DeepEqual(s.original.Status.Conditions, s.updated.Status.Conditions) {
		return false
	}
	if s.controller.clusters == nil {
		return true
	}
	if _, err := s.controller.clusters.UpdateStatus(s.updated); err != nil {
		slog.Warn("failed to update Cluster status",
			"cluster", s.original.Namespace+"/"+s.original.Name,
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
