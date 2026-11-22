// Package cluster reconciles Rancher provisioning Clusters into Fleet HelmOps.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	capictl "github.com/rancher/rancher/pkg/generated/controllers/cluster.x-k8s.io/v1beta2"
	provctl "github.com/rancher/rancher/pkg/generated/controllers/provisioning.cattle.io/v1"
	"github.com/rancher/wrangler/v3/pkg/condition"
	corectl "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/gorizond/d7y-cattle-operator/pkg/apply"
	"github.com/gorizond/d7y-cattle-operator/pkg/helmop"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
	"github.com/gorizond/d7y-cattle-operator/pkg/registry"
)

const (
	controllerName = "d7y-cattle-operator"
	// clusterFinalizer holds a deleting Cluster until the operator has removed
	// the per-seed scheduler certificate HelmOp, its legacy raw Bundle, and the
	// scheduler Secret in the operator namespace. The Secret is in a different
	// namespace from the Cluster, so Kubernetes ownerRef garbage collection
	// cannot clean it up.
	clusterFinalizer = "p2p.dragonfly.io/finalizer"
)

// eventStore is the subset of Wrangler's EventController used for Cluster events.
type eventStore interface {
	Create(*corev1.Event) (*corev1.Event, error)
}

// Options carries the operator configuration needed for reconciliation.
type Options struct {
	OperatorNamespace            string
	MTLSSecretName               string
	ManagerGRPCDomain            string
	ManagerGRPCPort              int
	ContainerdMirrorPort         int
	ManagerAPIHost               string
	ManagerAPIURL                string
	ManagerAPICredentialsSecret  string
	P2PDomain                    string
	ClusterIDFallback            string
	DragonflyChart               helmop.Chart
	ExtrasChart                  helmop.Chart
	MirrorChart                  helmop.Chart
	HostSyncChart                helmop.Chart
	SchedulerCertChart           helmop.Chart
	ExtrasIngressController      string
	ExtrasIngressClassName       string
	ExtrasIngressGRPCEnabled     bool
	ExtrasIngressRESTEnabled     bool
	ExtrasIngressTLSSecret       string
	ExtrasTraefikTransport       bool
	VPAEnabled                   bool
	StaticChart                  helmop.Chart
	StaticReleaseName            string
	StaticClientRepo             string
	StaticNydusRepo              string
	StaticSnapshotterRepo        string
	ClientSHA256AMD64            string
	ClientSHA256ARM64            string
	NydusSHA256AMD64             string
	NydusSHA256ARM64             string
	SnapshotterSHA256AMD64       string
	SnapshotterSHA256ARM64       string
	SnapshotterBinarySHA256AMD64 string
	SnapshotterBinarySHA256ARM64 string
	StaticURL                    string
	ClientVersion                string
	NydusVersion                 string
	SnapshotterVersion           string
}

// Controller reconciles provisioning.cattle.io/v1 Clusters.
type Controller struct {
	ctx         context.Context
	secrets     apply.SecretStore
	secretCache secretCache
	configmaps  configMapStore
	events      eventStore
	helmops     apply.Store
	bundles     apply.Store
	opts        Options

	clusters     clusterStore
	clusterCache clusterCache
	machines     machineCache
	downstream   schedulerPodLister

	managerMu         sync.Mutex
	managerHTTPClient *http.Client
	registrar         managerRegistrar
	allocator         *registry.Allocator
}

// Register wires the Cluster controller. Handlers must be registered before
// the factories are started.
func Register(ctx context.Context, clusters provctl.ClusterController, machines capictl.MachineController, secrets corectl.SecretController, configmaps corectl.ConfigMapController, events corectl.EventController, helmops, bundles apply.Store, opts Options) {
	c := &Controller{
		ctx:          ctx,
		secrets:      secrets,
		secretCache:  secrets.Cache(),
		configmaps:   configmaps,
		events:       events,
		helmops:      helmops,
		bundles:      bundles,
		clusters:     clusters,
		clusterCache: clusters.Cache(),
		machines:     machines.Cache(),
		downstream:   KubeSchedulerPodLister{},
		opts:         opts,
	}
	clusters.OnChange(ctx, controllerName, c.reconcile)
	machines.OnChange(ctx, controllerName+":scheduler-ips", c.machineChanged)
	if watcher, ok := helmops.(helmOpWatcher); ok {
		go c.watchHelmOps(watcher)
	}
	// A scheduler certificate Bundle can be changed by Fleet; re-enqueue only
	// the owning Cluster so the HelmOp-ready gate and dependents converge.
	if watcher, ok := bundles.(helmOpWatcher); ok {
		go c.watchBundles(watcher)
	}
}

func (c *Controller) reconcile(key string, cluster *provv1.Cluster) (result *provv1.Cluster, retErr error) {
	if cluster == nil {
		return nil, nil
	}
	if cluster.DeletionTimestamp != nil {
		return c.reconcileDeletion(cluster)
	}

	status := c.newStatus(cluster)
	defer func() {
		_, err := status.apply()
		if err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()

	roles := ResolveRoles(cluster)
	if !roles.Enabled {
		if err := c.cleanupAll(cluster); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "CleanupFailed", err.Error())
			if hasManagedStatus(cluster) {
				status.managedDisabled("LabelMissing", "cluster does not have label "+LabelEnabled+"=true")
			}
			return cluster, err
		}
		if hasManagedStatus(cluster) {
			status.managedDisabled("LabelMissing", "cluster does not have label "+LabelEnabled+"=true")
		}
		return cluster, nil
	}
	status.rolesResolved(true, roles.Manager, roles.Seed)

	// Hold the Cluster until finalization can remove the operator-namespace
	// scheduler Secret. The finalizer is only added to clusters the operator
	// manages (the gate label is present above).
	if !hasClusterFinalizer(cluster) {
		updated := cluster.DeepCopy()
		updated.Finalizers = append(updated.Finalizers, clusterFinalizer)
		result, err := c.clusters.Update(updated)
		if err != nil {
			// A failed metadata write must not block the normal reconcile; the
			// next pass retries the idempotent finalizer add.
			slog.Warn("failed to add Cluster finalizer",
				"cluster", cluster.Namespace+"/"+cluster.Name,
				"error", err,
			)
		} else {
			cluster = result
			status.rebase(cluster)
		}
	}

	var errs []error

	// Seed scheduler node IPs drive both the certificate SANs and the seed's
	// scheduler-ips annotation.
	var seedIPs []string
	seedDiscoveryFailed := false
	if roles.Seed {
		var err error
		seedIPs, err = c.discoverSeedSchedulerIPs(cluster)
		if err != nil {
			seedDiscoveryFailed = true
			c.event(cluster, corev1.EventTypeWarning, "SchedulerIPsUnavailable", err.Error())
		}
	}
	mtlsReady := true
	if roles.Manager || roles.Seed {
		updated, ready, err := c.ensurePKI(cluster, roles, seedIPs, seedDiscoveryFailed)
		if updated != nil {
			cluster = updated
			status.rebase(updated)
		}
		mtlsReady = ready
		if err != nil {
			errs = append(errs, err)
		}
	}

	if !seedDiscoveryFailed {
		if updated, err := c.ensureSchedulerIPs(cluster, roles, seedIPs); err != nil {
			c.event(cluster, corev1.EventTypeWarning, "SchedulerIPsFailed", err.Error())
		} else if updated != nil {
			cluster = updated
			status.rebase(updated)
		}
	}

	if updated, err := c.ensureContainerdHosts(cluster); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "ContainerdHostsFailed", err.Error())
		status.set(conditionContainerdHostsReady, false, "ApplyFailed", err.Error())
	} else {
		status.set(conditionContainerdHostsReady, true, "Ready", "containerd registry mirror hosts are reconciled")
		if updated != nil {
			cluster = updated
			status.rebase(updated)
		}
	}

	hc := c.helmopCluster(cluster)
	c.warnUnsupportedChartVersion(cluster, hc.Chart.Version)

	// Fleet targets shared HelmOps by Cluster label and renders the scheduler
	// host from the cluster-id label, so the labels must be in place before the
	// objects are applied.
	if updated, err := c.ensureClusterMetadata(cluster); err != nil {
		errs = append(errs, err)
	} else if updated != nil {
		// Continue with the updated object so later writes carry the same
		// resourceVersion instead of fighting the metadata update.
		cluster = updated
		status.rebase(updated)
	}

	shared := c.ensureSharedHelmOps(cluster, hc, roles, mtlsReady)
	if shared.keydb != nil {
		status.set(conditionKeyDBReady, false, "ApplyFailed", shared.keydb.Error())
	} else {
		status.set(conditionKeyDBReady, true, "Ready", "KeyDB HelmOp applied")
	}
	if roles.Seed {
		if mtlsReady {
			id, mismatch, registrationCluster, err := c.ensureSeedRegistration(c.ctx, cluster)
			if err != nil {
				retrying := registrationFailure(err)
				if status.registrationFailure(retrying, err) {
					reason := "RegistrationFailed"
					if retrying {
						reason = "Retrying"
					}
					c.event(cluster, corev1.EventTypeWarning, reason, err.Error())
				}
				status.seedUnavailable(reasonFor(retrying), err.Error())
				errs = append(errs, err)
			} else if int64(int(id)) != id {
				err := fmt.Errorf("scheduler cluster ID %d does not fit in int", id)
				retrying := registrationFailure(err)
				status.registrationFailure(retrying, err)
				status.seedUnavailable(reasonFor(retrying), err.Error())
				errs = append(errs, err)
			} else {
				if registrationCluster == nil {
					registrationCluster = cluster
				}
				status.rebase(registrationCluster)
				hc.SchedulerClusterID = int(id)
				if changed := status.registrationSuccess(id, mismatch); changed {
					c.event(cluster, corev1.EventTypeWarning, "IdMismatch",
						fmt.Sprintf("annotation scheduler cluster ID %d differs from manager server ID %d; annotation preserved", hc.SchedulerClusterID, id))
				}
				if err := c.ensureSeed(cluster); err != nil {
					errs = append(errs, err)
				}
				setSeedCondition(status, conditionControlReady, shared.control)
				setSeedCondition(status, conditionDataReady, errors.Join(shared.data, shared.mirror))
			}
		} else {
			status.seedUnavailable("MTLSSecretUnavailable", "mTLS Secret is not ready")
		}
	} else {
		if err := c.deleteSeed(cluster); err != nil {
			errs = append(errs, err)
		}
		status.seedDisabled()
		status.seedUnavailable("SeedDisabled", "seed role is disabled; the scheduler cluster ID annotation is preserved")
	}
	for _, err := range []error{
		shared.keydb, shared.manager, shared.control, shared.data, shared.extras,
		shared.mirror, shared.hostSync, shared.static, shared.cleanup,
	} {
		if err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		err := errors.Join(errs...)
		c.event(cluster, corev1.EventTypeWarning, "ReconcileFailed", err.Error())
		return cluster, err
	}
	return cluster, nil
}

func reasonFor(retrying bool) string {
	if retrying {
		return "Retrying"
	}
	return "RegistrationFailed"
}

func setSeedCondition(status *statusBuilder, cond condition.Cond, err error) {
	if err != nil {
		status.set(cond, false, "ApplyFailed", err.Error())
		return
	}
	status.set(cond, true, "Ready", "HelmOp applied")
}

func (c *Controller) warnUnsupportedChartVersion(cluster *provv1.Cluster, version string) {
	if version == helmop.VerifiedDragonflyChartVersion {
		return
	}
	c.event(cluster, corev1.EventTypeWarning, "UnsupportedChartVersion",
		fmt.Sprintf("Dragonfly chart version %s is not verified; expected %s", version, helmop.VerifiedDragonflyChartVersion))
}

func (c *Controller) helmopCluster(cluster *provv1.Cluster) helmop.Cluster {
	hc := helmop.Cluster{
		Name:                               cluster.Name,
		Namespace:                          cluster.Namespace,
		MTLSSecretName:                     c.opts.MTLSSecretName,
		Chart:                              c.opts.DragonflyChart,
		ExtrasChart:                        c.opts.ExtrasChart,
		MirrorChart:                        c.opts.MirrorChart,
		HostSyncChart:                      c.opts.HostSyncChart,
		SchedulerCertChart:                 c.opts.SchedulerCertChart,
		StaticChart:                        c.opts.StaticChart,
		StaticReleaseName:                  c.opts.StaticReleaseName,
		StaticClientVersion:                c.opts.ClientVersion,
		StaticNydusVersion:                 c.opts.NydusVersion,
		StaticSnapshotterVersion:           c.opts.SnapshotterVersion,
		StaticClientRepo:                   c.opts.StaticClientRepo,
		StaticNydusRepo:                    c.opts.StaticNydusRepo,
		StaticSnapshotterRepo:              c.opts.StaticSnapshotterRepo,
		StaticClientSHA256AMD64:            c.opts.ClientSHA256AMD64,
		StaticClientSHA256ARM64:            c.opts.ClientSHA256ARM64,
		StaticNydusSHA256AMD64:             c.opts.NydusSHA256AMD64,
		StaticNydusSHA256ARM64:             c.opts.NydusSHA256ARM64,
		StaticSnapshotterSHA256AMD64:       c.opts.SnapshotterSHA256AMD64,
		StaticSnapshotterSHA256ARM64:       c.opts.SnapshotterSHA256ARM64,
		StaticSnapshotterBinarySHA256AMD64: c.opts.SnapshotterBinarySHA256AMD64,
		StaticSnapshotterBinarySHA256ARM64: c.opts.SnapshotterBinarySHA256ARM64,
		StaticManagerHost:                  c.opts.ManagerAPIHost,
		StaticTLSSecret:                    c.opts.ExtrasIngressTLSSecret,
		StaticIngressClassName:             c.opts.ExtrasIngressClassName,
		StaticURL:                          c.opts.StaticURL,
		MirrorPort:                         c.opts.ContainerdMirrorPort,
		ManagerGRPCDomain:                  c.opts.ManagerGRPCDomain,
		P2PDomain:                          c.opts.P2PDomain,
		ManagerGRPCPort:                    c.opts.ManagerGRPCPort,
		ManagerAPIHost:                     c.opts.ManagerAPIHost,
		ManagerAPICredentialsSecret:        c.opts.ManagerAPICredentialsSecret,
		SchedulerClusterID:                 schedulerClusterID(cluster),
		SchedulerReplicas:                  helmop.ReplicasOrOne(AnnotationSchedulerReplicas, cluster.Annotations[AnnotationSchedulerReplicas]),
		SeedClientReplicas:                 helmop.ReplicasOrOne(AnnotationSeedClientReplicas, cluster.Annotations[AnnotationSeedClientReplicas]),
		SchedulerIPs:                       strings.TrimSpace(cluster.Annotations[AnnotationSchedulerIPs]),
		Monitoring:                         monitoringEnabled(cluster),
		P2PDataTLS:                         p2pDataTLSEnabled(cluster),
		Labels:                             apply.Labels(cluster.Namespace, cluster.Name),
	}
	if c.secrets != nil {
		if client, err := c.secrets.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName, metav1.GetOptions{}); err == nil {
			hc.MTLSCertRevision = pki.SecretRevision(client.Data)
		}
		if manager, err := c.secrets.Get(c.opts.OperatorNamespace, helmop.ManagerMTLSSecretName, metav1.GetOptions{}); err == nil {
			hc.ManagerCertRevision = pki.SecretRevision(manager.Data)
		}
		if scheduler, err := c.secrets.Get(c.opts.OperatorNamespace, helmop.SchedulerSourceSecretName(cluster.Name), metav1.GetOptions{}); err == nil {
			hc.SchedulerCertRevision = pki.SecretRevision(scheduler.Data)
		}
	}
	return hc
}

func (c *Controller) clusterID(cluster *provv1.Cluster) string {
	if cluster != nil {
		if id := strings.TrimSpace(cluster.Status.ClusterName); id != "" {
			return id
		}
	}
	return strings.TrimSpace(c.opts.ClusterIDFallback)
}

func schedulerHost(domain, clusterID string) string {
	domain = strings.TrimSpace(strings.Trim(domain, "."))
	clusterID = strings.TrimSpace(clusterID)
	if domain == "" || clusterID == "" {
		return ""
	}
	return fmt.Sprintf("d7y-scheduler.%s.%s", clusterID, domain)
}

// schedulerClusterID returns the annotation value as the integer the Dragonfly
// chart expects. Missing, non-numeric, and non-positive values are reported as 0
// so ensureSeed emits SchedulerClusterIDMissing instead of deploying with a
// default ID.
func schedulerClusterID(cluster *provv1.Cluster) int {
	raw := cluster.Annotations[AnnotationSchedulerClusterID]
	id, err := strconv.Atoi(raw)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

// sharedHelmOps carries the per-component shared apply results so conditions
// and legacy cleanup only act on components whose shared HelmOp was applied.
type sharedHelmOps struct {
	keydb, manager, control, data, extras, mirror, hostSync, static error
	managerApplied                                                  bool
	cleanup                                                         error
}

// ensureClusterMetadata stamps the cluster-id label used by shared Fleet
// templates. Role labels are user-managed and are never modified here.
func (c *Controller) ensureClusterMetadata(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	labels := map[string]string{}
	if id := strings.TrimSpace(cluster.Status.ClusterName); id != "" {
		labels[helmop.LabelClusterID] = id
	}
	if len(labels) == 0 {
		return nil, nil
	}
	changed := false
	for key, value := range labels {
		if cluster.Labels[key] != value {
			changed = true
		}
	}
	if !changed || c.clusters == nil {
		return nil, nil
	}
	updated := cluster.DeepCopy()
	if updated.Labels == nil {
		updated.Labels = map[string]string{}
	}
	for key, value := range labels {
		updated.Labels[key] = value
	}
	result, err := c.clusters.Update(updated)
	if err != nil {
		return nil, fmt.Errorf("update Cluster metadata: %w", err)
	}
	// The caller continues with this object; the informer-cache copy passed to
	// the handler is never mutated.
	return result, nil
}

// ensureSharedHelmOps applies the fixed set of shared HelmOps once. The objects
// are named after the component, so repeated reconciles from every Cluster are
// idempotent. Per-cluster HelmOps are cleaned up separately.
func (c *Controller) ensureSharedHelmOps(cluster *provv1.Cluster, hc helmop.Cluster, roles Roles, mtlsReady bool) sharedHelmOps {
	var result sharedHelmOps
	result.keydb = c.applyHelmOp(cluster, helmop.SharedKeyDB(hc))
	// The manager needs the operator-global credentials Secret and the mTLS
	// Secret; skip it until the certificate path is ready so the legacy release
	// keeps running.
	if roles.Manager && mtlsReady {
		result.manager = c.ensureSharedManager(cluster, hc, roles)
		result.managerApplied = true
	}
	result.control = c.applyHelmOp(cluster, helmop.SharedControl(hc))
	result.data = c.applyHelmOp(cluster, helmop.SharedData(hc))
	result.extras = c.applyHelmOp(cluster, helmop.SharedExtras(hc, helmop.ExtrasOptions{
		IngressController:              c.opts.ExtrasIngressController,
		IngressClassName:               c.opts.ExtrasIngressClassName,
		GRPCEnabled:                    c.opts.ExtrasIngressGRPCEnabled,
		RESTEnabled:                    c.opts.ExtrasIngressRESTEnabled,
		TLSSecret:                      c.opts.ExtrasIngressTLSSecret,
		TraefikServersTransportEnabled: c.opts.ExtrasTraefikTransport,
		VPAEnabled:                     c.opts.VPAEnabled,
	}))
	result.mirror = c.applyHelmOp(cluster, helmop.SharedMirror(hc))
	if roles.Manager && mtlsReady {
		result.static = c.applyHelmOp(cluster, helmop.SharedStatic(hc))
	}
	if roles.Seed && strings.Trim(hc.SchedulerIPs, " ,") != "" {
		result.hostSync = c.applyHelmOp(cluster, helmop.HostSync(hc))
	} else {
		result.hostSync = c.deleteHostSync(cluster)
	}
	result.cleanup = c.cleanupLegacyHelmOps(cluster, result, roles)
	return result
}

// cleanupLegacyHelmOps removes the per-cluster HelmOps (and their Bundles) that
// the shared objects replaced. It only runs for components whose shared HelmOp
// was applied, so a failed apply never drops the working release.
func (c *Controller) cleanupLegacyHelmOps(cluster *provv1.Cluster, shared sharedHelmOps, roles Roles) error {
	legacy := []struct {
		name string
		err  error
	}{
		{helmop.KeyDBName(cluster.Name), shared.keydb},
		{helmop.ControlName(cluster.Name), shared.control},
		{helmop.DataName(cluster.Name), shared.data},
		{helmop.ExtrasName(cluster.Name), shared.extras},
		{helmop.MirrorName(cluster.Name), shared.mirror},
	}
	var errs []error
	for _, item := range legacy {
		if item.err != nil {
			continue
		}
		if err := c.deleteHelmOp(cluster, item.name); err != nil {
			errs = append(errs, err)
		}
		if err := c.deleteBundle(cluster, item.name); err != nil {
			errs = append(errs, err)
		}
	}
	// Manager: remove the legacy release when the role is gone, or once the
	// shared release was applied. Keep it when the shared apply was skipped or
	// failed.
	if !roles.Manager || (shared.managerApplied && shared.manager == nil) {
		name := helmop.ManagerName(cluster.Name)
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
		if err := c.deleteBundle(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Controller) applyHelmOp(cluster *provv1.Cluster, op *fleetv1alpha1.HelmOp) error {
	if _, err := apply.Ensure(c.ctx, c.helmops, op); err != nil {
		return fmt.Errorf("apply HelmOp %s/%s: %w", cluster.Namespace, op.Name, err)
	}
	return nil
}

func (c *Controller) deleteHostSync(cluster *provv1.Cluster) error {
	name := helmop.HostSyncName(cluster.Name)
	return errors.Join(c.deleteHelmOp(cluster, name), c.deleteBundle(cluster, name))
}

func (c *Controller) deleteHelmOp(cluster *provv1.Cluster, name string) error {
	if _, err := apply.Delete(c.ctx, c.helmops, cluster.Namespace, name); err != nil {
		return fmt.Errorf("delete HelmOp %s/%s: %w", cluster.Namespace, name, err)
	}
	return nil
}

// deleteBundle removes the Fleet Bundle generated from a HelmOp. The HelmOp
// controller normally purges it from its finalizer, but that does not run when
// the HelmOp was never reconciled or Fleet is temporarily unavailable. The
// Bundle controller then purges BundleDeployments and the downstream agent
// uninstalls the release. This is deliberately name-scoped: there is no broad
// Fleet GC and no downstream-cluster client.
func (c *Controller) deleteBundle(cluster *provv1.Cluster, name string) error {
	if c.bundles == nil {
		return nil
	}
	current := &unstructured.Unstructured{}
	err := c.bundles.Get(c.ctx, cluster.Namespace, name, current, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Bundle %s/%s: %w", cluster.Namespace, name, err)
	}
	// Only delete a Bundle that Fleet generated from this HelmOp. A same-named
	// Bundle without the HelmOp label may belong to a user or another source.
	if current.GetLabels()[fleetv1alpha1.HelmOpLabel] != name {
		return nil
	}
	if _, err := apply.Delete(c.ctx, c.bundles, cluster.Namespace, name); err != nil {
		return fmt.Errorf("delete Bundle %s/%s: %w", cluster.Namespace, name, err)
	}
	return nil
}

// reconcileDeletion runs the full cleanup while the Cluster has a
// DeletionTimestamp, then removes the finalizer. A cleanup error keeps the
// finalizer and returns to the caller, so Wrangler requeues with backoff; the
// next pass retries the idempotent deletes instead of blocking in-process.
func (c *Controller) reconcileDeletion(cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if !hasClusterFinalizer(cluster) {
		return cluster, nil
	}
	if err := c.cleanupAll(cluster); err != nil {
		c.event(cluster, corev1.EventTypeWarning, "FinalizerCleanupFailed", err.Error())
		return cluster, err
	}
	updated := cluster.DeepCopy()
	updated.Finalizers = removeClusterFinalizer(updated.Finalizers)
	result, err := c.clusters.Update(updated)
	if err != nil {
		return cluster, fmt.Errorf("remove Cluster finalizer: %w", err)
	}
	return result, nil
}

func hasClusterFinalizer(cluster *provv1.Cluster) bool {
	for _, finalizer := range cluster.Finalizers {
		if finalizer == clusterFinalizer {
			return true
		}
	}
	return false
}

func removeClusterFinalizer(finalizers []string) []string {
	result := make([]string, 0, len(finalizers))
	for _, finalizer := range finalizers {
		if finalizer != clusterFinalizer {
			result = append(result, finalizer)
		}
	}
	return result
}

func (c *Controller) cleanupAll(cluster *provv1.Cluster) error {
	var errs []error
	for _, name := range []string{
		helmop.KeyDBName(cluster.Name),
		helmop.ManagerName(cluster.Name),
		helmop.ExtrasName(cluster.Name),
	} {
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.deleteSeed(cluster); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *Controller) event(cluster *provv1.Cluster, eventType, reason, message string) {
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%x", cluster.Name, now.UnixNano()),
			Namespace: cluster.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "provisioning.cattle.io/v1",
			Kind:       "Cluster",
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			UID:        cluster.UID,
		},
		Reason:         reason,
		Message:        message,
		Type:           eventType,
		Source:         corev1.EventSource{Component: controllerName},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if _, err := c.events.Create(event); err != nil {
		slog.Warn("failed to create Cluster event",
			"cluster", cluster.Namespace+"/"+cluster.Name,
			"reason", reason,
			"error", err,
		)
	}
}
