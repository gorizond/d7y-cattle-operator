// Package machineconfig implements the CREATE-only machine-config webhook.
// It patches one spec field, userData by default, chosen per Cluster with the
// p2p.dragonfly.io/node-config-key annotation.
package machineconfig

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	wranglerwebhook "github.com/rancher/wrangler/v3/pkg/webhook"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

const (
	MachineConfigGroup   = "rke-machine-config.cattle.io"
	MachineConfigVersion = "v1"

	LabelEnabled               = "p2p-dragonfly-io"
	AnnotationSeed             = "p2p.dragonfly.io/seed"
	AnnotationSchedulerCluster = "p2p.dragonfly.io/scheduler-cluster-id"
	AnnotationCluster          = "p2p.dragonfly.io/cluster"
	AnnotationClusterName      = "p2p.dragonfly.io/cluster-name"
	AnnotationClusterNamespace = "p2p.dragonfly.io/cluster-namespace"
	// AnnotationNodeConfigKey selects a single spec field to patch on CREATE.
	AnnotationNodeConfigKey = "p2p.dragonfly.io/node-config-key"

	defaultNodeConfigKey = "userData"

	managerGRPCPort = 65003
)

var nodeConfigKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

// ClusterCache is the subset of Wrangler's provisioning Cluster cache used by
// the webhook.
type ClusterCache interface {
	Get(namespace, name string) (*provv1.Cluster, error)
	List(namespace string, selector labels.Selector) ([]*provv1.Cluster, error)
}

// SecretCache is the subset of Wrangler's Secret cache used by the webhook.
type SecretCache interface {
	Get(namespace, name string) (*corev1.Secret, error)
}

type eventRecorder interface {
	Create(*corev1.Event) (*corev1.Event, error)
}

// Handler mutates new machine configs. It never mutates updates.
type Handler struct {
	Clusters          ClusterCache
	Secrets           SecretCache
	Events            eventRecorder
	Namespace         string
	MTLSSecretName    string
	ManagerGRPCDomain string
	StaticURL         string
}

// NewRouter registers the group/version router. The handler checks operation and
// kind because Wrangler's router does not support wildcard resource matching.
func NewRouter(handler *Handler) *wranglerwebhook.Router {
	router := wranglerwebhook.NewRouter()
	router.Group(MachineConfigGroup).
		Version(MachineConfigVersion).
		Handle(handler)
	return router
}

// Admit implements wrangler webhook.Handler.
func (h *Handler) Admit(resp *wranglerwebhook.Response, req *wranglerwebhook.Request) error {
	resp.Allowed = true
	if req.Operation != admissionv1.Create {
		return nil
	}

	obj, err := req.DecodeObject()
	if err != nil {
		h.event(req, "DecodeFailed", err)
		return nil
	}
	config, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	if !strings.HasSuffix(config.GetKind(), "Config") {
		return nil
	}

	cluster, err := h.targetCluster(config)
	if err != nil {
		h.event(req, "ClusterLookupFailed", err)
		return nil
	}

	configApplicable := config.GetLabels()[LabelEnabled] == "true" &&
		config.GetAnnotations()[AnnotationSeed] == "true"
	clusterApplicable := cluster != nil &&
		cluster.Labels[LabelEnabled] == "true" &&
		cluster.Annotations[AnnotationSeed] == "true"
	if !configApplicable && !clusterApplicable {
		return nil
	}

	nodeConfigKey := defaultNodeConfigKey
	if cluster != nil {
		if key := strings.TrimSpace(cluster.Annotations[AnnotationNodeConfigKey]); key != "" {
			if !nodeConfigKeyPattern.MatchString(key) {
				h.event(req, "NodeConfigKeyInvalid", fmt.Errorf("cluster annotation %s=%q must match %s", AnnotationNodeConfigKey, key, nodeConfigKeyPattern))
				return nil
			}
			nodeConfigKey = key
		}
	}

	schedulerID := strings.TrimSpace(config.GetAnnotations()[AnnotationSchedulerCluster])
	if schedulerID == "" && cluster != nil {
		schedulerID = strings.TrimSpace(cluster.Annotations[AnnotationSchedulerCluster])
	}
	if schedulerID == "" {
		h.event(req, "SchedulerClusterIDMissing", fmt.Errorf("scheduler cluster ID annotation is empty"))
		return nil
	}

	if h.Secrets == nil {
		h.event(req, "MTLSSecretUnavailable", fmt.Errorf("secret cache is not configured"))
		return nil
	}
	secret, err := h.Secrets.Get(h.Namespace, h.MTLSSecretName)
	if err != nil {
		h.event(req, "MTLSSecretUnavailable", err)
		return nil
	}
	payload, err := bootstrap.Encode(bootstrap.Payload{
		ManagerGRPC:        fmt.Sprintf("https://%s:%d", h.ManagerGRPCDomain, managerGRPCPort),
		StaticURL:          h.StaticURL,
		SchedulerClusterID: schedulerID,
		CACert:             secret.Data["ca.crt"],
		ClientCert:         secret.Data[corev1.TLSCertKey],
		ClientKey:          secret.Data[corev1.TLSPrivateKeyKey],
	})
	if err != nil {
		h.event(req, "PayloadBuildFailed", err)
		return nil
	}

	nodeConfig, _, err := unstructured.NestedString(config.Object, "spec", nodeConfigKey)
	if err != nil {
		h.event(req, "NodeConfigMalformed", err)
		return nil
	}
	line := bootstrap.InjectionLine(h.StaticURL, payload)
	var patched string
	var changed bool
	if nodeConfigKey == defaultNodeConfigKey {
		patched, changed, err = bootstrap.InjectUserData(nodeConfig, line)
	} else {
		patched, changed, err = bootstrap.InjectScript(nodeConfig, line)
	}
	if err != nil {
		h.event(req, "NodeConfigPatchFailed", err)
		return nil
	}
	if !changed {
		return nil
	}
	if err := unstructured.SetNestedField(config.Object, patched, "spec", nodeConfigKey); err != nil {
		h.event(req, "NodeConfigPatchFailed", err)
		return nil
	}
	if err := resp.CreatePatch(req, config); err != nil {
		h.event(req, "PatchCreateFailed", err)
	}
	return nil
}

func (h *Handler) targetCluster(config *unstructured.Unstructured) (*provv1.Cluster, error) {
	namespace := config.GetNamespace()
	annotations := config.GetAnnotations()

	if ref := strings.TrimSpace(annotations[AnnotationCluster]); ref != "" {
		clusterNamespace, clusterName := namespace, ref
		if parts := strings.SplitN(ref, "/", 2); len(parts) == 2 {
			clusterNamespace, clusterName = parts[0], parts[1]
		}
		return h.getCluster(clusterNamespace, clusterName)
	}
	if name := strings.TrimSpace(annotations[AnnotationClusterName]); name != "" {
		clusterNamespace := strings.TrimSpace(annotations[AnnotationClusterNamespace])
		if clusterNamespace == "" {
			clusterNamespace = namespace
		}
		return h.getCluster(clusterNamespace, name)
	}

	if h.Clusters == nil {
		return nil, fmt.Errorf("cluster cache is not configured")
	}
	clusters, err := h.Clusters.List(namespace, labels.Everything())
	if err != nil {
		return nil, err
	}
	configName := config.GetName()
	var best *provv1.Cluster
	for _, cluster := range clusters {
		if cluster.Namespace != namespace {
			continue
		}
		if configName != cluster.Name && !strings.HasPrefix(configName, cluster.Name+"-") {
			continue
		}
		if best == nil || len(cluster.Name) > len(best.Name) {
			best = cluster
		}
	}
	return best, nil
}

func (h *Handler) getCluster(namespace, name string) (*provv1.Cluster, error) {
	if h.Clusters == nil {
		return nil, fmt.Errorf("cluster cache is not configured")
	}
	cluster, err := h.Clusters.Get(namespace, name)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	return cluster, err
}

func (h *Handler) event(req *wranglerwebhook.Request, reason string, err error) {
	if h.Events == nil {
		return
	}
	now := metav1.Now()
	kind := req.Kind.Kind
	if kind == "" {
		kind = "Config"
	}
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s.%x", req.Name, time.Now().UnixNano()),
			Namespace: req.Namespace,
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: MachineConfigGroup + "/" + MachineConfigVersion,
			Kind:       kind,
			Namespace:  req.Namespace,
			Name:       req.Name,
			UID:        req.UID,
		},
		Reason:         reason,
		Message:        err.Error(),
		Type:           corev1.EventTypeWarning,
		Source:         corev1.EventSource{Component: "d7y-cattle-operator-webhook"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if _, createErr := h.Events.Create(event); createErr != nil {
		slog.Warn("failed to create webhook event", "reason", reason, "error", createErr)
	}
}
