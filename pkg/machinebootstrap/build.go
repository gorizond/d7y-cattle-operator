// Package machinebootstrap injects the p2p install line into the
// rke.cattle.io/bootstrap Secret consumed by Rancher's node-driver bootstrap.
//
// Injection is synchronous: a mutating admission webhook patches the bootstrap
// Secret before the API server persists it, so the node driver can never read a
// Secret that missed the p2p block. There is no watch-based mutation.
package machinebootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
	"github.com/gorizond/d7y-cattle-operator/pkg/pki"
)

const (
	bootstrapSecretType    = corev1.SecretType("rke.cattle.io/bootstrap")
	bootstrapValueKey      = "value"
	machineBootstrapSuffix = "-machine-bootstrap"

	labelEnabled    = "d7y-operator-cattle"
	annotationSeed  = "p2p.dragonfly.io/seed"
	capiClusterName = "cluster.x-k8s.io/cluster-name"
	rkeClusterName  = "rke.cattle.io/cluster-name"

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
	ClientVersion      string
	NydusVersion       string
	SnapshotterVersion string
}

// SecretCache reads Secrets; Wrangler's SecretCache satisfies it.
type SecretCache interface {
	Get(namespace, name string) (*corev1.Secret, error)
}

// MachineCache reads CAPI Machines; Wrangler's MachineCache satisfies it.
type MachineCache interface {
	Get(namespace, name string) (*capiv1beta2.Machine, error)
}

// ClusterCache reads provisioning Clusters; Wrangler's ClusterCache satisfies it.
type ClusterCache interface {
	Get(namespace, name string) (*provv1.Cluster, error)
}

// Builder builds the desired machine bootstrap Secret.
type Builder struct {
	mtls     SecretCache
	machines MachineCache
	clusters ClusterCache
	opts     Options
}

// NewBuilder wires the caches used by the admission webhook.
func NewBuilder(mtls SecretCache, machines MachineCache, clusters ClusterCache, opts Options) *Builder {
	return &Builder{mtls: mtls, machines: machines, clusters: clusters, opts: opts}
}

// machineNameFromSecret resolves the CAPI Machine name for a bootstrap Secret.
// The webhook rule matches the "-machine-bootstrap" suffix, but
// name.SafeConcatName truncates long generated names before the suffix; those
// Secrets point at the Machine through an RKEBootstrap ownerReference, as older
// operator versions already handled.
func machineNameFromSecret(secret *corev1.Secret) string {
	if secret == nil {
		return ""
	}
	if strings.HasSuffix(secret.Name, machineBootstrapSuffix) {
		return strings.TrimSuffix(secret.Name, machineBootstrapSuffix)
	}
	for _, ref := range secret.OwnerReferences {
		if ref.APIVersion == "rke.cattle.io/v1" && ref.Kind == "RKEBootstrap" {
			return ref.Name
		}
	}
	return ""
}

// Build returns the Secret with the current p2p block and annotations. The
// second result is false when the Secret is not applicable or already carries
// the current block; callers must then leave the Secret unchanged.
func (b *Builder) Build(secret *corev1.Secret) (*corev1.Secret, bool, error) {
	if secret == nil || secret.Type != bootstrapSecretType {
		return secret, false, nil
	}
	if b == nil || b.mtls == nil || b.machines == nil || b.clusters == nil {
		return secret, false, fmt.Errorf("machinebootstrap: caches are not configured")
	}
	machineName := machineNameFromSecret(secret)
	if machineName == "" {
		return secret, false, nil
	}
	machine, err := b.machines.Get(secret.Namespace, machineName)
	if err != nil {
		return secret, false, fmt.Errorf("get CAPI Machine %s/%s: %w", secret.Namespace, machineName, err)
	}

	clusterName := strings.TrimSpace(machine.Labels[capiClusterName])
	if clusterName == "" {
		clusterName = strings.TrimSpace(machine.Labels[rkeClusterName])
	}
	if clusterName == "" {
		// Not a Rancher-provisioned machine; nothing to inject.
		return secret, false, nil
	}
	cluster, err := b.clusters.Get(machine.Namespace, clusterName)
	if err != nil {
		return secret, false, fmt.Errorf("get provisioning Cluster %s/%s: %w", machine.Namespace, clusterName, err)
	}
	if cluster.Labels[labelEnabled] != "true" || cluster.Annotations[annotationSeed] != "true" {
		return secret, false, nil
	}

	value := string(secret.Data[bootstrapValueKey])
	base, err := removePreinstallBlock(value)
	if err != nil {
		return secret, false, fmt.Errorf("remove pre-install block: %w", err)
	}
	schedulerID := ""
	if cluster != nil {
		schedulerID = strings.TrimSpace(cluster.Annotations[bootstrap.SchedulerClusterIDAnnotation])
	}
	placeholderSchedulerID := schedulerID == ""

	preinstall, preinstallReason := effectivePreinstall(cluster, base)

	mtls, err := b.mtls.Get(b.opts.OperatorNamespace, b.opts.MTLSSecretName)
	if err != nil {
		return secret, false, fmt.Errorf("get mTLS Secret %s/%s: %w", b.opts.OperatorNamespace, b.opts.MTLSSecretName, err)
	}
	clientRevision := pki.SecretRevision(mtls.Data)
	inputsRevision := bootstrapInputsFingerprint(cluster, clientRevision,
		b.opts.ClientVersion, b.opts.NydusVersion, b.opts.SnapshotterVersion, preinstall)
	if strings.Contains(base, bootstrap.Marker) &&
		strings.Contains(base, shortInstallSignature) &&
		secret.Annotations[bootstrap.BootstrapInputsAnnotation] == inputsRevision &&
		strings.Contains(value, bootstrap.PreinstallMarker) == (preinstall != "") {
		return secret, false, nil
	}
	if preinstallReason != "" {
		slog.Warn(preinstallSkipMessage(preinstallReason),
			"secret", secret.Namespace+"/"+secret.Name, "cluster", clusterName, "reason", preinstallReason)
	}
	env, err := bootstrap.BuildInstallEnv(cluster, mtls, "", bootstrap.InstallConfig{
		ManagerGRPCDomain:  b.opts.ManagerGRPCDomain,
		ManagerGRPCPort:    b.opts.ManagerGRPCPort,
		StaticURL:          b.opts.StaticURL,
		SmartProxyURL:      b.opts.SmartProxyURL,
		NydusMirrorURL:     b.opts.NydusMirrorURL,
		ClientVersion:      b.opts.ClientVersion,
		NydusVersion:       b.opts.NydusVersion,
		SnapshotterVersion: b.opts.SnapshotterVersion,
	})
	if err != nil {
		return secret, false, fmt.Errorf("build install env: %w", err)
	}
	if placeholderSchedulerID {
		slog.Warn("injecting p2p bootstrap with scheduler cluster ID placeholder",
			"cluster", clusterName, "machine", machineName, "schedulerClusterID", "0")
	}

	// The old pre-install block was removed before the p2p step so its body
	// lines cannot be mistaken for the p2p marker; now replace the p2p block
	// and insert the new pre-install block immediately before it.
	patched, err := insertInlineBlock(base, shortInstallBlock(b.opts.StaticURL, env))
	if err != nil {
		return secret, false, fmt.Errorf("patch p2p block: %w", err)
	}
	preinstallIncluded := false
	if preinstall != "" {
		patched, err = insertPreinstallBlock(patched, inlinePreinstallBlock(preinstall))
		if err != nil {
			return secret, false, fmt.Errorf("patch pre-install block: %w", err)
		}
		preinstallIncluded = true
	}

	// The Kubernetes Secret limit applies to the serialized object, where
	// data is base64-encoded; the raw value length alone is not a safe guard.
	if preinstallIncluded && bootstrapSecretTooLarge(secret, patched, clientRevision, inputsRevision) {
		preinstallIncluded = false
		slog.Warn("pre-install block exceeds the serialized Secret limit; pre-install block skipped",
			"secret", secret.Namespace+"/"+secret.Name, "cluster", clusterName)
		patched, err = insertInlineBlock(base, shortInstallBlock(b.opts.StaticURL, env))
		if err != nil {
			return secret, false, fmt.Errorf("patch p2p block: %w", err)
		}
		inputsRevision = bootstrapInputsFingerprint(cluster, clientRevision,
			b.opts.ClientVersion, b.opts.NydusVersion, b.opts.SnapshotterVersion, "")
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

	// An oversized annotation must not cause repeated updates: when the
	// candidate value and annotations already match, the Secret converges.
	if string(secret.Data[bootstrapValueKey]) == patched &&
		secret.Annotations[bootstrap.ClientCertRevisionAnnotation] == clientRevision &&
		secret.Annotations[bootstrap.BootstrapInputsAnnotation] == inputsRevision {
		return secret, false, nil
	}
	slog.Info("injected machine bootstrap p2p block",
		"secret", secret.Namespace+"/"+secret.Name, "machine", machineName, "cluster", clusterName,
		"preinstall", preinstallIncluded)
	return updated, true, nil
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

// bootstrapSecretTooLarge reports whether adding the candidate value and
// annotations would exceed the Kubernetes object limit.
func bootstrapSecretTooLarge(secret *corev1.Secret, value, clientRevision, inputsRevision string) bool {
	candidate := secret.DeepCopy()
	if candidate.Data == nil {
		candidate.Data = map[string][]byte{}
	}
	candidate.Data[bootstrapValueKey] = []byte(value)
	if candidate.Annotations == nil {
		candidate.Annotations = map[string]string{}
	}
	candidate.Annotations[bootstrap.ClientCertRevisionAnnotation] = clientRevision
	candidate.Annotations[bootstrap.BootstrapInputsAnnotation] = inputsRevision
	raw, err := json.Marshal(candidate)
	if err != nil {
		return true
	}
	return len(raw) > maxBootstrapSecretBytes
}
