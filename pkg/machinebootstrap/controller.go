// Package machinebootstrap injects the p2p install line into the
// rke.cattle.io/bootstrap Secret consumed by Rancher's node-driver bootstrap.
package machinebootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	provctl "github.com/rancher/rancher/pkg/generated/controllers/provisioning.cattle.io/v1"
	corectl "github.com/rancher/wrangler/v3/pkg/generated/controllers/core/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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

	// maxBootstrapSecretBytes is the Kubernetes object limit. The API stores
	// data as base64, so the raw value ceiling is not the right guard.
	maxBootstrapSecretBytes = 1 << 20
)

// Options carries the operator configuration used to build the p2p line.
type Options struct {
	OperatorNamespace  string
	MTLSSecretName     string
	ManagerGRPCDomain  string
	ManagerGRPCPort    int
	StaticURL          string
	SmartProxyURL      string
	NydusMirrorURL     string
	SchedulerDomain    string
	ClusterIDFallback  string
	ClientVersion      string
	NydusVersion       string
	SnapshotterVersion string
}

type secretUpdater interface {
	Update(*corev1.Secret) (*corev1.Secret, error)
	Enqueue(namespace, name string)
}

type SecretCache interface {
	Get(namespace, name string) (*corev1.Secret, error)
	List(namespace string, selector labels.Selector) ([]*corev1.Secret, error)
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

	clusterInputsMu sync.Mutex
	clusterInputs   map[string]string
}

// Register wires the machine-bootstrap Secret controller.
func Register(ctx context.Context, secrets corectl.SecretController, machines capictl.MachineController, clusters provctl.ClusterController, events corectl.EventController, opts Options) {
	c := &Controller{
		secrets:  secrets,
		mtls:     secrets.Cache(),
		machines: machines.Cache(),
		clusters: clusters.Cache(),
		events:   events,
		opts:     opts,
	}
	secrets.OnChange(ctx, controllerName, c.reconcile)
	secrets.OnChange(ctx, controllerName+":client-cert-revision", c.clientSecretChanged)
	clusters.OnChange(ctx, controllerName+":scheduler-inputs", c.clusterChanged)
}

// clusterChanged re-enqueues only the bootstrap Secrets that belong to the
// updated Cluster. Reconcile compares the full input fingerprint and is a
// no-op when scheduler IPs/ID and the client revision are unchanged.
func (c *Controller) clusterChanged(_ string, cluster *provv1.Cluster) (*provv1.Cluster, error) {
	if cluster == nil || c.secrets == nil || c.mtls == nil || c.machines == nil || c.clusters == nil {
		return cluster, nil
	}
	if cluster.Labels[labelEnabled] != "true" || cluster.Annotations[annotationSeed] != "true" {
		return cluster, nil
	}
	key := string(cluster.UID)
	if key == "" {
		key = cluster.Namespace + "/" + cluster.Name
	}
	signature := strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerIPsAnnotation]) + "\x00" +
		strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerClusterIDAnnotation]) + "\x00" +
		bootstrap.Distro(cluster) + "\x00" +
		strings.TrimSpace(cluster.Annotations[bootstrap.PreinstallAnnotation])
	c.clusterInputsMu.Lock()
	if c.clusterInputs == nil {
		c.clusterInputs = map[string]string{}
	}
	if c.clusterInputs[key] == signature {
		c.clusterInputsMu.Unlock()
		return cluster, nil
	}
	c.clusterInputs[key] = signature
	c.clusterInputsMu.Unlock()

	items, err := c.mtls.List(metav1.NamespaceAll, labels.Everything())
	if err != nil {
		c.clusterInputsMu.Lock()
		if c.clusterInputs[key] == signature {
			delete(c.clusterInputs, key)
		}
		c.clusterInputsMu.Unlock()
		slog.Warn("failed to list bootstrap secrets for cluster change", "cluster", cluster.Name, "error", err)
		return cluster, nil
	}
	for _, secret := range items {
		if secret == nil || secret.Type != bootstrapSecretType {
			continue
		}
		machineName := machineNameFromSecret(secret)
		if machineName == "" {
			continue
		}
		machine, err := c.machines.Get(secret.Namespace, machineName)
		if err != nil || machine.Namespace != cluster.Namespace {
			continue
		}
		clusterName := strings.TrimSpace(machine.Labels[capiClusterName])
		if clusterName == "" {
			clusterName = strings.TrimSpace(machine.Labels[rkeClusterName])
		}
		if clusterName != cluster.Name {
			continue
		}
		if _, err := c.clusters.Get(machine.Namespace, clusterName); err != nil {
			continue
		}
		c.secrets.Enqueue(secret.Namespace, secret.Name)
	}
	return cluster, nil
}

// clientSecretChanged re-enqueues every machine-bootstrap Secret when the
// shared client leaf changes, so existing bootstrap blocks pick up the new CA
// bundle/revision without waiting for an unrelated informer event.
func (c *Controller) clientSecretChanged(_ string, secret *corev1.Secret) (*corev1.Secret, error) {
	if secret == nil || c.mtls == nil || c.secrets == nil {
		return secret, nil
	}
	if secret.Namespace != c.opts.OperatorNamespace || secret.Name != c.opts.MTLSSecretName {
		return secret, nil
	}
	items, err := c.mtls.List(metav1.NamespaceAll, labels.Everything())
	if err != nil {
		slog.Warn("failed to list bootstrap secrets for client revision", "error", err)
		return secret, nil
	}
	for _, item := range items {
		if item != nil && item.Type == bootstrapSecretType {
			c.secrets.Enqueue(item.Namespace, item.Name)
		}
	}
	return secret, nil
}

// machineNameFromSecret resolves the CAPI Machine name for a bootstrap Secret.
// RKEBootstrap-created Secrets use a truncated generated name and point at the
// Machine through an ownerReference; older Secrets use the
// "<machine>-machine-bootstrap" name.
func machineNameFromSecret(secret *corev1.Secret) string {
	if secret == nil {
		return ""
	}
	for _, ref := range secret.OwnerReferences {
		if ref.APIVersion == "rke.cattle.io/v1" && ref.Kind == "RKEBootstrap" {
			return ref.Name
		}
	}
	if strings.HasSuffix(secret.Name, machineBootstrapSuffix) {
		return strings.TrimSuffix(secret.Name, machineBootstrapSuffix)
	}
	return ""
}

func (c *Controller) reconcile(key string, secret *corev1.Secret) (*corev1.Secret, error) {
	if secret == nil || secret.Type != bootstrapSecretType {
		return secret, nil
	}
	if c.secrets == nil || c.mtls == nil || c.machines == nil || c.clusters == nil {
		return secret, nil
	}
	machineName := machineNameFromSecret(secret)
	if machineName == "" {
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
	base, err := removePreinstallBlock(value)
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
		return secret, err
	}
	schedulerID := ""
	if cluster != nil {
		schedulerID = strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerClusterIDAnnotation])
	}
	placeholderSchedulerID := schedulerID == ""

	preinstall, preinstallReason := effectivePreinstall(cluster, base)

	mtls, err := c.mtls.Get(c.opts.OperatorNamespace, c.opts.MTLSSecretName)
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "MTLSSecretUnavailable", err.Error())
		return secret, err
	}
	clientRevision := pki.SecretRevision(mtls.Data)
	inputsRevision := bootstrapInputsFingerprint(cluster, clientRevision,
		c.opts.ClientVersion, c.opts.NydusVersion, c.opts.SnapshotterVersion, preinstall)
	if strings.Contains(base, bootstrap.Marker) &&
		strings.Contains(base, shortInstallQuery) &&
		secret.Annotations[bootstrap.BootstrapInputsAnnotation] == inputsRevision &&
		strings.Contains(value, bootstrap.PreinstallMarker) == (preinstall != "") {
		return secret, nil
	}
	if preinstallReason != "" {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, preinstallReason, preinstallSkipMessage(preinstallReason))
	}
	env, err := bootstrap.BuildInstallEnv(cluster, mtls, "", bootstrap.InstallConfig{
		ManagerGRPCDomain:  c.opts.ManagerGRPCDomain,
		ManagerGRPCPort:    c.opts.ManagerGRPCPort,
		StaticURL:          c.opts.StaticURL,
		SmartProxyURL:      c.opts.SmartProxyURL,
		NydusMirrorURL:     c.opts.NydusMirrorURL,
		SchedulerDomain:    c.opts.SchedulerDomain,
		ClusterIDFallback:  c.opts.ClusterIDFallback,
		ClientVersion:      c.opts.ClientVersion,
		NydusVersion:       c.opts.NydusVersion,
		SnapshotterVersion: c.opts.SnapshotterVersion,
	})
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "PayloadBuildFailed", err.Error())
		return secret, err
	}
	payload, err := bootstrap.Encode(env)
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

	// The old pre-install block was removed before the p2p step so its body
	// lines cannot be mistaken for the p2p marker; now replace the p2p block
	// and insert the new pre-install block immediately before it.
	patched, err := insertInlineBlock(base, shortInstallBlock(c.opts.StaticURL, payload))
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
		return secret, err
	}
	preinstallIncluded := false
	if preinstall != "" {
		patched, err = insertPreinstallBlock(patched, inlinePreinstallBlock(preinstall))
		if err != nil {
			c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
			return secret, err
		}
		preinstallIncluded = true
	}
	updated := secret.DeepCopy()
	if updated.Data == nil {
		updated.Data = map[string][]byte{}
	}
	updated.Data[bootstrapValueKey] = []byte(patched)
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[bootstrap.ClientCertRevisionAnnotation] = clientRevision
	updated.Annotations[bootstrap.BootstrapInputsAnnotation] = inputsRevision

	// The Kubernetes Secret limit applies to the serialized object, where
	// data is base64-encoded; the raw value length alone is not a safe guard.
	if preinstallIncluded && bootstrapSecretTooLarge(updated) {
		preinstallIncluded = false
		p2pOnly, err := insertInlineBlock(base, shortInstallBlock(c.opts.StaticURL, payload))
		if err != nil {
			c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
			return secret, err
		}
		patched = p2pOnly
		inputsRevision = bootstrapInputsFingerprint(cluster, clientRevision,
			c.opts.ClientVersion, c.opts.NydusVersion, c.opts.SnapshotterVersion, "")
		updated.Data[bootstrapValueKey] = []byte(patched)
		updated.Annotations[bootstrap.BootstrapInputsAnnotation] = inputsRevision
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "PreinstallTooLarge",
			"pre-install block exceeds the serialized Secret limit; pre-install block skipped")
	}

	// An oversized annotation must not cause repeated Updates: when the
	// candidate value and annotations already match, the Secret converges.
	if string(secret.Data[bootstrapValueKey]) == patched &&
		secret.Annotations[bootstrap.ClientCertRevisionAnnotation] == clientRevision &&
		secret.Annotations[bootstrap.BootstrapInputsAnnotation] == inputsRevision {
		return secret, nil
	}
	result, err := c.secrets.Update(updated)
	if err != nil {
		c.event(machine, cluster, secret, corev1.EventTypeWarning, "BootstrapPatchFailed", err.Error())
		return secret, err
	}
	message := "short p2p block added to the machine bootstrap secret (p2p-only; d7y-operator-preinstall not included)"
	if preinstallIncluded {
		message = "short p2p and pre-install blocks added to the machine bootstrap secret (d7y-operator-preinstall included)"
	}
	c.event(machine, cluster, secret, corev1.EventTypeNormal, "BootstrapInjected", message)
	return result, nil
}

func bootstrapInputsFingerprint(cluster *provv1.Cluster, clientRevision, clientVersion, nydusVersion, snapshotterVersion, preinstall string) string {
	ips := ""
	schedulerID := ""
	if cluster != nil {
		ips = strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerIPsAnnotation])
		schedulerID = strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerClusterIDAnnotation])
	}
	input := clientRevision + "\x00" + ips + "\x00" + schedulerID + "\x00" + bootstrap.Distro(cluster) +
		"\x00" + clientVersion + "\x00" + nydusVersion + "\x00" + snapshotterVersion
	// Keep the exact v0.3.21 hash when no pre-install body is configured, so
	// upgrading the operator does not re-inject p2p blocks into existing nodes.
	if preinstall = strings.TrimSpace(preinstall); preinstall != "" {
		input += "\x00" + preinstall
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// effectivePreinstall returns the body to render and a skip reason when the
// annotation cannot be injected. Invalid or oversized bodies never reach the
// Secret; the p2p block is still rendered.
func effectivePreinstall(cluster *provv1.Cluster, value string) (string, string) {
	if cluster == nil {
		return "", ""
	}
	annotation := cluster.Annotations[bootstrap.PreinstallAnnotation]
	body := strings.TrimSpace(annotation)
	if body == "" {
		return "", ""
	}
	switch {
	case len(annotation) > bootstrap.MaxPreinstallAnnotationBytes:
		return "", "PreinstallTooLarge"
	case invalidPreinstall(body):
		return "", "PreinstallInvalid"
	case len(value)+len(body)+1024 > bootstrap.MaxBootstrapValueBytes:
		return "", "PreinstallTooLarge"
	}
	return body, ""
}

func invalidPreinstall(body string) bool {
	if strings.IndexByte(body, 0) >= 0 {
		return true
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == bootstrap.PreinstallMarker || trimmed == bootstrap.PreinstallEndMarker {
			return true
		}
	}
	return false
}

func preinstallSkipMessage(reason string) string {
	if reason == "PreinstallInvalid" {
		return "d7y-operator-preinstall annotation contains NUL or a reserved marker line; pre-install block skipped"
	}
	return "d7y-operator-preinstall annotation exceeds a size limit; pre-install block skipped"
}

func bootstrapSecretTooLarge(secret *corev1.Secret) bool {
	raw, err := json.Marshal(secret)
	if err != nil {
		return true
	}
	return len(raw) > maxBootstrapSecretBytes
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
