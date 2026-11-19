// Package cluster reconciles Rancher provisioning Clusters into Fleet HelmOps.
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
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

const controllerName = "d7y-cattle-operator"

// eventStore is the subset of Wrangler's EventController used for Cluster events.
type eventStore interface {
	Create(*corev1.Event) (*corev1.Event, error)
}

// Options carries the operator configuration needed for reconciliation.
type Options struct {
	OperatorNamespace           string
	MTLSSecretName              string
	ManagerGRPCDomain           string
	ManagerGRPCPort             int
	ManagerAPIHost              string
	ManagerAPIURL               string
	ManagerAPICredentialsSecret string
	P2PDomain                   string
	ClusterIDFallback           string
	Resolver                    Resolver
	DragonflyChart              helmop.Chart
	ExtrasChart                 helmop.Chart
	MirrorChart                 helmop.Chart
	HarborChart                 helmop.Chart
	RegistryProxyChart          helmop.Chart
	AdmissionChart              helmop.Chart
	HarborIngressClass          string
	ProxyDomain                 string
	ProxyHarborURL              string
	ProxyHarborAdminSecret      string
	ProxyRegistries             []string
	ProxyBasePath               string
	ProxyTLSSecret              string
	ProxyHostAliases            []helmop.HostAlias
	AdmissionImagePullSecret    string
	AdmissionServiceAccount     string
	AdmissionNetworkOnly        bool
	ExtrasIngressController     string
	ExtrasIngressClassName      string
	ExtrasIngressGRPCEnabled    bool
	ExtrasIngressRESTEnabled    bool
	ExtrasIngressTLSSecret      string
	ExtrasTraefikTransport      bool
	VPAEnabled                  bool
}

// Controller reconciles provisioning.cattle.io/v1 Clusters.
type Controller struct {
	ctx     context.Context
	secrets apply.SecretStore
	events  eventStore
	helmops apply.Store
	bundles apply.Store
	opts    Options

	clusters clusterStore

	robotEnsurer      robotEnsurer
	managerMu         sync.Mutex
	managerHTTPClient *http.Client
	registrar         managerRegistrar
	allocator         *registry.Allocator
}

// Register wires the Cluster controller. Handlers must be registered before
// the factories are started.
func Register(ctx context.Context, clusters provctl.ClusterController, secrets corectl.SecretController, events corectl.EventController, helmops, bundles apply.Store, opts Options) {
	c := &Controller{
		ctx:          ctx,
		secrets:      secrets,
		events:       events,
		helmops:      helmops,
		bundles:      bundles,
		clusters:     clusters,
		robotEnsurer: harborRobotEnsurer{},
		opts:         opts,
	}
	clusters.OnChange(ctx, controllerName, c.reconcile)
}

func (c *Controller) reconcile(key string, cluster *provv1.Cluster) (result *provv1.Cluster, retErr error) {
	if cluster == nil {
		return nil, nil
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

	hc := c.helmopCluster(cluster)
	c.warnUnsupportedChartVersion(cluster, hc.Chart.Version)

	var errs []error
	if err := c.applyHelmOp(cluster, helmop.KeyDB(hc)); err != nil {
		status.set(conditionKeyDBReady, false, "ApplyFailed", err.Error())
		errs = append(errs, err)
	} else {
		status.set(conditionKeyDBReady, true, "Ready", "KeyDB HelmOp applied")
	}

	if hc.HarborURL == "" {
		hc.HarborURL = c.opts.ProxyHarborURL
	}
	if hc.HarborURL == "" && hc.HarborManaged && roles.HarborDomain != "" {
		hc.HarborURL = "https://" + roles.HarborDomain
	}
	harborErr := c.ensureHarbor(cluster, hc, roles)
	setHarborCondition(status, roles, harborErr)
	if harborErr != nil {
		errs = append(errs, harborErr)
	}
	proxyErr := c.ensureRegistryProxy(cluster, hc, roles, harborErr)
	proxyReady := false
	proxyMessage := ""
	if proxyErr == nil && roles.Proxy {
		var stateErr error
		proxyReady, proxyMessage, stateErr = c.helmOpReady(cluster, helmop.RegistryProxyName(cluster.Name), "registry proxy")
		if stateErr != nil {
			proxyErr = stateErr
		}
	}
	setRegistryProxyCondition(status, roles, proxyErr, proxyReady, proxyMessage)
	if proxyErr != nil {
		errs = append(errs, proxyErr)
	}

	admissionErr := c.ensureAdmission(cluster, hc, roles)
	admissionReady := false
	admissionMessage := ""
	if admissionErr == nil && roles.Admission {
		var stateErr error
		admissionReady, admissionMessage, stateErr = c.helmOpReady(cluster, helmop.AdmissionName(cluster.Name), "admission webhook")
		if stateErr != nil {
			admissionErr = stateErr
		}
	}
	setAdmissionCondition(status, roles, admissionErr, admissionReady, admissionMessage)
	if admissionErr != nil {
		errs = append(errs, admissionErr)
	}

	mtlsReady := true
	if roles.Manager || roles.Seed {
		publicIP, err := c.ensureMTLSCertificate(cluster, roles)
		if err != nil {
			errs = append(errs, err)
			mtlsReady = false
		} else {
			// Refresh the HelmOp inputs from the updated Secret and use the
			// currently resolved public address for advertiseIP/dynconfig.
			hc = c.helmopCluster(cluster)
			hc.SchedulerPublicIP = publicIP
			hc.SchedulerHost = schedulerHost(c.opts.P2PDomain, c.clusterID(cluster))
		}
	}
	if roles.Manager || roles.Seed {
		if mtlsReady {
			if err := c.ensureMTLSSecret(cluster); err != nil {
				errs = append(errs, err)
				mtlsReady = false
			}
		}
	}

	if roles.Manager {
		if mtlsReady {
			if err := c.ensureManager(cluster, hc); err != nil {
				errs = append(errs, err)
			}
		}
	} else if err := c.deleteManager(cluster); err != nil {
		errs = append(errs, err)
	}

	seedReady := false
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
				seedReady = true
				controlErr, dataErr := c.ensureSeed(cluster, hc)
				setSeedCondition(status, conditionControlReady, controlErr)
				setSeedCondition(status, conditionDataReady, dataErr)
				for _, err := range []error{controlErr, dataErr} {
					if err != nil {
						errs = append(errs, err)
					}
				}
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

	if err := c.applyHelmOp(cluster, helmop.Extras(hc, helmop.ExtrasOptions{
		IngressEnabled:                 roles.Manager && mtlsReady,
		IngressController:              c.opts.ExtrasIngressController,
		IngressClassName:               c.opts.ExtrasIngressClassName,
		GRPCEnabled:                    c.opts.ExtrasIngressGRPCEnabled,
		RESTEnabled:                    c.opts.ExtrasIngressRESTEnabled,
		TLSSecret:                      c.opts.ExtrasIngressTLSSecret,
		TraefikServersTransportEnabled: c.opts.ExtrasTraefikTransport,
		VPAEnabled:                     c.opts.VPAEnabled,
		Manager:                        roles.Manager && mtlsReady,
		Seed:                           seedReady,
		SchedulerPublicEnabled:         seedReady,
		SchedulerPublicIP:              hc.SchedulerPublicIP,
		SchedulerDomain:                c.opts.P2PDomain,
		SchedulerClusterID:             c.clusterID(cluster),
		SchedulerService:               helmop.SchedulerServiceName(cluster.Name),
		SchedulerPort:                  helmop.SchedulerPort,
	})); err != nil {
		errs = append(errs, err)
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

func setHarborCondition(status *statusBuilder, roles Roles, err error) {
	switch {
	case !roles.Harbor:
		status.set(conditionHarborReady, false, "Disabled", "Harbor is not enabled")
		status.set(conditionHarborDomainMissing, false, "NotRequired", "Harbor is not enabled")
	case roles.HarborURL != "" && validateHarborURL(roles.HarborURL) != nil:
		status.set(conditionHarborReady, false, "HarborURLInvalid", "p2p.dragonfly.io/harbor-url is not an absolute https URL")
		status.set(conditionHarborDomainMissing, false, "NotRequired", "using an external Harbor")
	case roles.HarborURL != "":
		status.set(conditionHarborReady, true, "External", "using the configured external Harbor")
		status.set(conditionHarborDomainMissing, false, "NotRequired", "using an external Harbor")
	case validateHarborDomain(roles.HarborDomain) != nil:
		status.set(conditionHarborReady, false, "HarborDomainMissing", "p2p.dragonfly.io/harbor-domain is missing or invalid")
		status.set(conditionHarborDomainMissing, true, "Invalid", "p2p.dragonfly.io/harbor-domain is missing or invalid")
	case err != nil:
		status.set(conditionHarborReady, false, "ApplyFailed", err.Error())
		status.set(conditionHarborDomainMissing, false, "Valid", "Harbor domain is valid")
	default:
		status.set(conditionHarborReady, true, "Ready", "Harbor HelmOp applied")
		status.set(conditionHarborDomainMissing, false, "Valid", "Harbor domain is valid")
	}
}

func (c *Controller) helmOpReady(cluster *provv1.Cluster, name, component string) (bool, string, error) {
	obj := &unstructured.Unstructured{}
	err := c.helmops.Get(c.ctx, cluster.Namespace, name, obj, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, component + " HelmOp is not created yet", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("get %s HelmOp: %w", component, err)
	}
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false, component + " HelmOp Ready condition is missing", nil
	}
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] != "Ready" {
			continue
		}
		if condition["status"] == "True" {
			return true, "", nil
		}
		message, _ := condition["message"].(string)
		if message == "" {
			message = component + " HelmOp is not Ready"
		}
		return false, message, nil
	}
	return false, component + " HelmOp Ready condition is missing", nil
}

func setRegistryProxyCondition(status *statusBuilder, roles Roles, err error, ready bool, message string) {
	if !roles.Proxy {
		status.set(conditionRegistryProxyReady, false, "Disabled", "registry proxy is not enabled")
		return
	}
	if err != nil {
		status.set(conditionRegistryProxyReady, false, "ApplyFailed", err.Error())
		return
	}
	if !ready {
		if message == "" {
			message = "registry proxy HelmOp is not Ready"
		}
		status.set(conditionRegistryProxyReady, false, "Progressing", message)
		return
	}
	status.set(conditionRegistryProxyReady, true, "Ready", "registry proxy HelmOp Ready")
}

func proxyPublicURL(domain, basePath string) string {
	if domain == "" {
		return ""
	}
	if basePath == "" {
		return "https://" + domain
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	return "https://" + domain + strings.TrimRight(basePath, "/")
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

// ensureMTLSSecret replicates the operator mTLS Secret into the Cluster
// namespace so Fleet can deliver it downstream.
func (c *Controller) ensureMTLSSecret(cluster *provv1.Cluster) error {
	mtls, err := c.secrets.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName, metav1.GetOptions{})
	if err != nil {
		message := fmt.Sprintf("Secret %s/%s: %v", c.opts.OperatorNamespace, c.opts.MTLSSecretName, err)
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", message)
		return fmt.Errorf("get mTLS secret: %w", err)
	}

	source := mtls.DeepCopy()
	if source.Labels == nil {
		source.Labels = map[string]string{}
	}
	for key, value := range apply.Labels(cluster.Namespace, cluster.Name) {
		source.Labels[key] = value
	}
	if len(mtls.Data["ca.crt"]) == 0 {
		err := fmt.Errorf("mTLS Secret %s/%s has no ca.crt", c.opts.OperatorNamespace, c.opts.MTLSSecretName)
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return err
	}
	if _, err := apply.ReplicateSecret(source, cluster.Namespace, c.secrets); err != nil {
		message := fmt.Sprintf("replicate mTLS Secret to %s/%s: %v", cluster.Namespace, source.Name, err)
		c.event(cluster, corev1.EventTypeWarning, "SecretReplicationFailed", message)
		return fmt.Errorf("replicate mTLS Secret to %s/%s: %w", cluster.Namespace, source.Name, err)
	}
	return nil
}

func (c *Controller) helmopCluster(cluster *provv1.Cluster) helmop.Cluster {
	roles := ResolveRoles(cluster)
	hc := helmop.Cluster{
		Name:                       cluster.Name,
		Namespace:                  cluster.Namespace,
		MTLSSecretName:             c.opts.MTLSSecretName,
		Chart:                      c.opts.DragonflyChart,
		ExtrasChart:                c.opts.ExtrasChart,
		MirrorChart:                c.opts.MirrorChart,
		HarborChart:                c.opts.HarborChart,
		RegistryProxyChart:         c.opts.RegistryProxyChart,
		AdmissionChart:             c.opts.AdmissionChart,
		HarborAdminSecret:          helmop.HarborAdminSecretName(cluster.Name),
		HarborSecretKey:            helmop.HarborSecretKeyName(cluster.Name),
		ProxyWebhookSecretName:     helmop.RegistryProxyWebhookSecretName(cluster.Name),
		ProxyRobotSecretName:       helmop.RegistryProxyRobotSecretName(cluster.Name),
		ProxyDomain:                c.opts.ProxyDomain,
		ProxyPublicURL:             proxyPublicURL(c.opts.ProxyDomain, c.opts.ProxyBasePath),
		ProxyBasePath:              c.opts.ProxyBasePath,
		ProxyTLSSecret:             c.opts.ProxyTLSSecret,
		ProxyHostAliases:           c.opts.ProxyHostAliases,
		ProxyRegistries:            c.opts.ProxyRegistries,
		AdmissionImagePullSecret:   c.opts.AdmissionImagePullSecret,
		AdmissionServiceAccount:    c.opts.AdmissionServiceAccount,
		AdmissionNetworkOnly:       c.opts.AdmissionNetworkOnly,
		AdmissionExcludeNamespaces: roles.AdmissionExclude,
		ManagerGRPCDomain:          c.opts.ManagerGRPCDomain,
		ManagerGRPCPort:            c.opts.ManagerGRPCPort,
		ManagerAPIHost:             c.opts.ManagerAPIHost,
		SchedulerClusterID:         schedulerClusterID(cluster),
		SchedulerReplicas:          helmop.ReplicasOrOne(AnnotationSchedulerReplicas, cluster.Annotations[AnnotationSchedulerReplicas]),
		SeedClientReplicas:         helmop.ReplicasOrOne(AnnotationSeedClientReplicas, cluster.Annotations[AnnotationSeedClientReplicas]),
		Monitoring:                 monitoringEnabled(cluster),
		P2PDataTLS:                 p2pDataTLSEnabled(cluster),
		Labels:                     apply.Labels(cluster.Namespace, cluster.Name),
	}
	hc.HarborDomain = roles.HarborDomain
	hc.HarborURL = roles.HarborURL
	hc.HarborManaged = roles.Harbor && roles.HarborURL == ""
	hc.HarborIngressClass = c.opts.HarborIngressClass
	hc.ProxyEnabled = roles.Proxy
	hc.AdmissionEnabled = roles.Admission
	// The certificate revision is derived from the live Secret so a rotation
	// changes pod template annotations exactly once. Missing ca.crt errors are
	// reported by ensureMTLSSecret.
	if c.secrets != nil {
		if mtls, err := c.secrets.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName, metav1.GetOptions{}); err == nil {
			hc.MTLSCertRevision = pki.CertRevision(mtls.Data[corev1.TLSCertKey])
		}
	}
	return hc
}

// resolver returns the configured DNS resolver or the production default.
func (c *Controller) resolver() Resolver {
	if c.opts.Resolver != nil {
		return c.opts.Resolver
	}
	return NetResolver{}
}

// clusterID returns the Rancher cluster ID used in the scheduler DNS alias.
// Provisioning Cluster status.clusterName is authoritative; the explicit
// operator fallback is used only when status is not populated yet.
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
	if domain == "" || clusterID == "" {
		return ""
	}
	return fmt.Sprintf("d7y-scheduler.%s.%s", clusterID, domain)
}

// ensureMTLSCertificate creates or updates the operator P2P Secret from the
// scheduler host resolved at reconcile time. Seed clusters own the full SAN set:
// resolved public IPs + manager DNS. A DNS failure leaves
// the existing certificate untouched and returns an error so the reconcile is
// retried. Manager-only clusters only bootstrap a missing Secret and never
// rewrite an existing one (a seed reconcile owns the public IP set).
func (c *Controller) ensureMTLSCertificate(cluster *provv1.Cluster, roles Roles) (string, error) {
	if !roles.Manager && !roles.Seed {
		return "", nil
	}
	clusterID := c.clusterID(cluster)
	if clusterID == "" {
		err := fmt.Errorf("cluster %s/%s has empty status.clusterName and no --p2p-cluster-id fallback", cluster.Namespace, cluster.Name)
		c.event(cluster, corev1.EventTypeWarning, "SchedulerHostUnavailable", err.Error())
		return "", err
	}
	host := schedulerHost(c.opts.P2PDomain, clusterID)
	if host == "" {
		err := fmt.Errorf("scheduler host is empty: --p2p-domain is required")
		c.event(cluster, corev1.EventTypeWarning, "SchedulerHostUnavailable", err.Error())
		return "", err
	}

	existing, getErr := c.secrets.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName, metav1.GetOptions{})
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		err := fmt.Errorf("get mTLS Secret %s/%s: %w", c.opts.OperatorNamespace, c.opts.MTLSSecretName, getErr)
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return "", err
	}

	if !roles.Seed {
		if getErr == nil {
			return "", nil
		}
		return "", c.bootstrapMTLSSecret(cluster)
	}

	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	resolved, err := c.resolver().LookupHost(ctx, host)
	if err != nil {
		msg := fmt.Sprintf("resolve scheduler host %s: %v", host, err)
		c.event(cluster, corev1.EventTypeWarning, "SchedulerDNSResolutionFailed", msg)
		return "", errors.New(msg)
	}
	resolvedIPs := validPublicIPs(resolved)
	if len(resolvedIPs) == 0 {
		msg := fmt.Sprintf("scheduler host %s resolved to no IP addresses", host)
		c.event(cluster, corev1.EventTypeWarning, "SchedulerDNSResolutionFailed", msg)
		return "", errors.New(msg)
	}
	publicIPs := resolvedIPs
	if getErr == nil {
		publicIPs = append(publicIPs, existingPublicIPs(existing)...)
	}
	publicIPs = dedupeStrings(publicIPs)

	bundle, err := pki.Generate(pki.Options{
		ManagerGRPCDomain:  c.opts.ManagerGRPCDomain,
		ManagerAPIURL:      c.opts.ManagerAPIURL,
		SchedulerPublicIPs: publicIPs,
	})
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return "", err
	}
	result, err := pki.EnsureSecret(c.secrets, c.opts.OperatorNamespace, c.opts.MTLSSecretName, bundle)
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return "", err
	}
	if result.Created || result.Updated {
		slog.Info("P2P certificate ensured",
			"cluster", cluster.Namespace+"/"+cluster.Name,
			"host", host,
			"created", result.Created,
			"updated", result.Updated,
			"revision", result.Revision,
		)
	}
	return primaryPublicIP(resolvedIPs), nil
}

// bootstrapMTLSSecret creates a manager-identity-only Secret when no seed
// cluster has created the full certificate yet. It never updates an existing
// Secret, so it cannot strip public IP SANs.
func (c *Controller) bootstrapMTLSSecret(cluster *provv1.Cluster) error {
	bundle, err := pki.Generate(pki.Options{
		ManagerGRPCDomain: c.opts.ManagerGRPCDomain,
		ManagerAPIURL:     c.opts.ManagerAPIURL,
	})
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return err
	}
	result, err := pki.EnsureSecret(c.secrets, c.opts.OperatorNamespace, c.opts.MTLSSecretName, bundle)
	if err != nil {
		c.event(cluster, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return err
	}
	if result.Created {
		slog.Info("bootstrap P2P certificate ensured",
			"cluster", cluster.Namespace+"/"+cluster.Name,
			"revision", result.Revision,
		)
	}
	return nil
}

func validPublicIPs(resolved []string) []string {
	ips := make([]string, 0, len(resolved))
	for _, raw := range resolved {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if !pki.IsPublicIP(ip) {
			continue
		}
		ips = append(ips, ip.String())
	}
	return dedupeStrings(ips)
}

func existingPublicIPs(secret *corev1.Secret) []string {
	if secret == nil {
		return nil
	}
	certs, err := pki.ParseCertificates(secret.Data[corev1.TLSCertKey])
	if err != nil || len(certs) == 0 {
		return nil
	}
	ips := make([]string, 0, len(certs[0].IPAddresses))
	for _, ip := range certs[0].IPAddresses {
		// Keep previously resolved public addresses through DNS/IP rotation,
		// but never retain loopback, link-local, private, multicast, or
		// internal ClusterIP SANs.
		if !pki.IsPublicIP(ip) {
			continue
		}
		ips = append(ips, ip.String())
	}
	return ips
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// primaryPublicIP prefers an IPv4 address for scheduler advertiseIP and the
// external-dns target; the certificate still contains every resolved IP.
func primaryPublicIP(ips []string) string {
	for _, raw := range ips {
		if ip := net.ParseIP(raw); ip != nil && ip.To4() != nil {
			return ip.String()
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
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

func (c *Controller) applyHelmOp(cluster *provv1.Cluster, op *fleetv1alpha1.HelmOp) error {
	if _, err := apply.Ensure(c.ctx, c.helmops, op); err != nil {
		return fmt.Errorf("apply HelmOp %s/%s: %w", cluster.Namespace, op.Name, err)
	}
	return nil
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

func (c *Controller) cleanupAll(cluster *provv1.Cluster) error {
	var errs []error
	for _, name := range []string{
		helmop.KeyDBName(cluster.Name),
		helmop.ManagerName(cluster.Name),
		helmop.ExtrasName(cluster.Name),
		helmop.HarborName(cluster.Name),
		helmop.RegistryProxyName(cluster.Name),
		helmop.AdmissionName(cluster.Name),
	} {
		if err := c.deleteHelmOp(cluster, name); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.deleteBundle(cluster, helmop.AdmissionName(cluster.Name)); err != nil {
		errs = append(errs, err)
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
