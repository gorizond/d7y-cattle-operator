// Package machineconfig implements the CREATE-only machine-config webhook.
// It patches one node-config field, userData by default, chosen per Cluster
// with the p2p.dragonfly.io/node-config-key annotation. RKE machine configs
// (for example CloudblastConfig) keep their fields at the top level; objects
// with a spec map keep the legacy spec path.
package machineconfig

import (
	"encoding/json"
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

	LabelEnabled               = "d7y-operator-cattle"
	AnnotationSeed             = "p2p.dragonfly.io/seed"
	AnnotationSchedulerCluster = "p2p.dragonfly.io/scheduler-cluster-id"
	AnnotationCluster          = "p2p.dragonfly.io/cluster"
	AnnotationClusterName      = "p2p.dragonfly.io/cluster-name"
	AnnotationClusterNamespace = "p2p.dragonfly.io/cluster-namespace"
	// AnnotationNodeConfigKey selects a single node-config field to patch on
	// CREATE. RKE machine configs keep it at the top level.
	AnnotationNodeConfigKey = "p2p.dragonfly.io/node-config-key"

	defaultNodeConfigKey = "userData"

	// managerGRPCPort is the public Traefik websecure port. The manager
	// backend still listens on 65003 behind the IngressRouteTCP.
	managerGRPCPort = 443
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
	SmartProxyURL     string
	NydusMirrorURL    string
	SchedulerDomain   string
	ClusterIDFallback string
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

	clusterID := strings.TrimSpace(h.ClusterIDFallback)
	if cluster != nil && strings.TrimSpace(cluster.Status.ClusterName) != "" {
		clusterID = strings.TrimSpace(cluster.Status.ClusterName)
	}
	schedulerHostName := schedulerHost(h.SchedulerDomain, clusterID)
	if schedulerHostName == "" {
		h.event(req, "SchedulerHostUnavailable", fmt.Errorf("scheduler host is empty: P2P_DOMAIN and cluster ID are required"))
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
		SmartProxyURL:      h.SmartProxyURL,
		NydusMirrorURL:     h.NydusMirrorURL,
		SchedulerHost:      schedulerHostName,
		SchedulerClusterID: schedulerID,
		CACert:             secret.Data["ca.crt"],
		ClientCert:         secret.Data[corev1.TLSCertKey],
		ClientKey:          secret.Data[corev1.TLSPrivateKeyKey],
	})
	if err != nil {
		h.event(req, "PayloadBuildFailed", err)
		return nil
	}

	nodeConfig, present, err := nodeConfigValue(config.Object, nodeConfigKey)
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
	specNested := false
	if _, ok := config.Object["spec"].(map[string]any); ok {
		specNested = true
	}
	path := "/" + nodeConfigKey
	if specNested {
		path = "/spec/" + nodeConfigKey
	}
	op := "replace"
	if !present {
		op = "add"
	}
	ops := []jsonPatchOperation{{
		Op:    op,
		Path:  path,
		Value: patched,
	}}
	// HetznerConfig is the only rke-machine-config kind that declares
	// userDataFromFile. Rancher merges the generated install script into a
	// temporary file and passes that path on --hetzner-user-data, so the driver
	// must read the path as a file instead of treating it as inline content.
	if nodeConfigKey == defaultNodeConfigKey {
		fromFile, found, err := nodeConfigBool(config.Object, specNested, "userDataFromFile")
		if err != nil {
			h.event(req, "NodeConfigMalformed", err)
			return nil
		}
		if found && !fromFile {
			prefix := ""
			if specNested {
				prefix = "/spec"
			}
			ops = append(ops, jsonPatchOperation{
				Op:    "replace",
				Path:  prefix + "/userDataFromFile",
				Value: true,
			})
		}
	}
	patch, err := json.Marshal(ops)
	if err != nil {
		h.event(req, "PatchCreateFailed", err)
		return nil
	}
	patchType := admissionv1.PatchTypeJSONPatch
	resp.Patch = patch
	resp.PatchType = &patchType
	return nil
}

type jsonPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// nodeConfigValue reads the configured node field. RKE machine configs
// (CloudblastConfig and the other rke-machine-config resources) have no spec
// map and store the field at the top level; objects that do have a spec map
// keep the legacy spec path.
func nodeConfigValue(object map[string]any, key string) (string, bool, error) {
	if _, ok := object["spec"].(map[string]any); ok {
		return unstructured.NestedString(object, "spec", key)
	}
	return unstructured.NestedString(object, key)
}

// nodeConfigBool reads a boolean node-config field. Only HetznerConfig declares
// userDataFromFile, so found is false for every other machine config kind.
func nodeConfigBool(object map[string]any, specNested bool, key string) (bool, bool, error) {
	if specNested {
		return unstructured.NestedBool(object, "spec", key)
	}
	return unstructured.NestedBool(object, key)
}

func schedulerHost(domain, clusterID string) string {
	domain = strings.TrimSpace(strings.Trim(domain, "."))
	clusterID = strings.TrimSpace(clusterID)
	if domain == "" || clusterID == "" {
		return ""
	}
	return fmt.Sprintf("d7y-scheduler.%s.%s", clusterID, domain)
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
