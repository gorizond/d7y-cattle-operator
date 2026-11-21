// Package machinebootstrap injects the p2p install line into the
// rke.cattle.io/bootstrap Secret consumed by Rancher's node-driver bootstrap.
package machinebootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corectl "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	capictl "github.com/rancher/rancher/pkg/generated/controllers/cluster.x-k8s.io/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const (
	controllerName         = "d7y-machine-bootstrap"
	bootstrapSecretType    = corev1.SecretType("rke.cattle.io/bootstrap")
	bootstrapValueKey      = "value"
	machineBootstrapSuffix = "-machine-bootstrap"

	labelEnabled       = "d7y-operator-cattle"
	annotationSeed     = "p2p.dragonfly.io/seed"
	capiClusterName    = "cluster.x-k8s.io/cluster-name"
	rkeClusterName     = "rke.cattle.io/cluster-name"
	eventComponentName = "d7y-cattle-operator-machine-bootstrap"
)

// Options carries the operator configuration used to build the p2p line.
type Options struct {
	OperatorNamespace string
	MTLSSecretName    string
	ManagerGRPCDomain string
	ManagerGRPCPort   int
	StaticURL         string
	SmartProxyURL     string
	NydusMirrorURL    string
	SchedulerDomain   string
	ClusterIDFallback string
}

type secretUpdater interface {
	Update(*corev1.Secret) (*corev1.Secret, error)
}

type SecretCache interface {
	Get(namespace, name string) (*corev1.Secret, error)
}

type MachineCache interface {
	Get(namespace, name string) (*capiv1beta2.Machine, error)
}

type ClusterCache interface {
	Get(namespace, name string) (*provv1.Cluster, error)
}

type eventStore interface {
	Create(*corev1.Event) (*corev1.Event, error)
}

// Controller watches machine bootstrap Secrets and adds the p2p install line.
type Controller struct {
	secrets  secretUpdater
	mtls     SecretCache
	machines MachineCache
	clusters ClusterCache
	events   eventStore
	opts     Options
}

// Register wires the machine-bootstrap Secret controller.
func Register(ctx context.Context, secrets corectl.SecretController, machines capictl.MachineController, clusters ClusterCache, events corectl.EventController, opts Options) {
	c := &Controller{
		secrets:  secrets,
		mtls:     secrets.Cache(),
		machines: machines.Cache(),
		clusters: clusters,
		events:   events,
		opts:     opts,
	}
	secrets.OnChange(ctx, controllerName, c.reconcile)
}

func (c *Controller) reconcile(key string, secret *corev1.Secret) (*corev1.Secret, error) {
	if secret == nil || secret.Type != bootstrapSecretType {
		return secret, nil
	}
	if c.secrets == nil || c.mtls == nil || c.machines == nil || c.clusters == nil {
		return secret, nil
	}
	machineName := strings.TrimSuffix(secret.Name, machineBootstrapSuffix)
	if machineName == "" || machineName == secret.Name {
		return secret, nil
	}
	machine, err := c.machines.Get(secret.Namespace, machineName)
	if apierrors.IsNotFound(err) {
		c.event(nil, nil, secret, corev1.EventTypeWarning, "MachineNotFound", fmt.Sprintf("CAPI Machine %s/%s was not found", secret.Namespace, machineName))
		return secret, nil
	}
	if err != nil {
		c.event(nil, nil, secret, corev1.EventTypeWarning, "MachineLookupFailed", err.Error())
		return secret, err
	}

	clusterName := strings.TrimSpace(machine.Labels[capiClusterName])
	if clusterName == "" {
		clusterName = strings.TrimSpace(machine.Labels[rkeClusterName])
	}
	if clusterName == "" {
		c.event(machine, nil, secret, corev1.EventTypeWarning, "ClusterNameMissing", "CAPI Machine has no cluster name label")
		return secret, nil
	}
	cluster, err := c.clusters.Get(machine.Namespace, clusterName)
	if apierrors.IsNotFound(err) {
		c.event(machine, nil, secret, corev1.EventTypeWarning, "ClusterNotFound", fmt.Sprintf("provisioning Cluster %s/%s was not found", machine.Namespace, clusterName))
		return secret, nil
	}
	if err != nil {
		c.event(machine, nil, secret, corev1.EventTypeWarning, "ClusterLookupFailed", err.Error())
		return secret, err
	}
	if cluster.Labels[labelEnabled] != "true" || cluster.Annotations[annotationSeed] != "true" {
		return secret, nil
	}

	value := string(secret.Data[bootstrapValueKey])
	schedulerID := ""
	if cluster != nil {
		schedulerID = strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerClusterIDAnnotation])
	}
	placeholderSchedulerID := schedulerID == ""

	mtls, err := c.mtls.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName)
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return secret, err
	}
	clientRevision := pki.SecretRevision(mtls.Data)
	if strings.Contains(value, bootstrap.Marker) && secret.Annotations[bootstrap.ClientCertRevisionAnnotation] == clientRevision {
		return secret, nil
	}
	env, err := bootstrap.BuildInstallEnv(cluster, mtls, "", bootstrap.InstallConfig{
		ManagerGRPCDomain: c.opts.ManagerGRPCDomain,
		ManagerGRPCPort:   c.opts.ManagerGRPCPort,
		StaticURL:         c.opts.StaticURL,
		SmartProxyURL:     c.opts.SmartProxyURL,
		NydusMirrorURL:    c.opts.NydusMirrorURL,
		SchedulerDomain:   c.opts.SchedulerDomain,
		ClusterIDFallback: c.opts.ClusterIDFallback,
	})
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "PayloadBuildFailed", err.Error())
		return secret, err
	}
	if placeholderSchedulerID {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "SchedulerClusterIDPlaceholder",
			"scheduler cluster ID annotation is empty; injecting schedulerClusterID 0 placeholder")
		slog.Warn("injecting p2p bootstrap with scheduler cluster ID placeholder",
			"cluster", clusterName, "machine", machineName, "schedulerClusterID", "0")
	}

	updated := secret.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	updated.Data[bootstrapValueKey] = []byte(insertInlineBlock(value, inlineInstallBlock(env)))
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[bootstrap.ClientCertRevisionAnnotation] = clientRevision
	result, err := c.secrets.Update(updated)
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
		return secret, err
	}
	c.event(machine, cluster, secret, corev1.EventTypeNormal, "BootstrapInjected", "inline p2p block added to the machine bootstrap secret")
	return result, nil
}

func (c *Controller) event(machine *capiv1beta2.Machine, cluster *provv1.Cluster, secret *corev1.Secret, eventType, reason, message string) {
	if c.events == nil {
		return
	}
	var ref corev1.ObjectReference
	switch {
	case machine != nil:
		ref = corev1.ObjectReference{
			APIVersion: "cluster.x-k8s.io/v1beta2",
			Kind:       "Machine",
			Namespace:  machine.Namespace,
			Name:       machine.Name,
			UID:        machine.UID,
		}
	case cluster != nil:
		ref = corev1.ObjectReference{
			APIVersion: "provisioning.cattle.io/v1",
			Kind:       "Cluster",
			Namespace:  cluster.Namespace,
			Name:       cluster.Name,
			UID:        cluster.UID,
		}
	default:
		ref = corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  secret.Namespace,
			Name:       secret.Name,
			UID:        secret.UID,
		}
	}
	now := metav1.Now()
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%x", ref.Name, time.Now().UnixNano()),
			Namespace: ref.Namespace,
		},
		InvolvedObject: ref,
		Reason:         reason,
		Message:        message,
		Type:           eventType,
		Source:         corev1.EventSource{Component: eventComponentName},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if _, err := c.events.Create(event); err != nil {
		slog.Warn("failed to create machine bootstrap event", "reason", reason, "error", err)
	}
}
