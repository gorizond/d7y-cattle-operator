package machinebootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

type fakeSecrets struct {
	secret    *corev1.Secret
	mtls      *corev1.Secret
	updates   int
	updateErr error
	enqueued  []string
	others    []*corev1.Secret
	listCalls int
}

func (f *fakeSecrets) Enqueue(namespace, name string) {
	f.enqueued = append(f.enqueued, namespace+"/"+name)
}

func (f *fakeSecrets) List(namespace string, selector labels.Selector) ([]*corev1.Secret, error) {
	f.listCalls++
	var result []*corev1.Secret
	for _, secret := range append([]*corev1.Secret{f.secret, f.mtls}, f.others...) {
		if secret == nil || (namespace != metav1.NamespaceAll && secret.Namespace != namespace) {
			continue
		}
		if selector != nil && !selector.Matches(labels.Set(secret.Labels)) {
			continue
		}
		result = append(result, secret.DeepCopy())
	}
	return result, nil
}

func (f *fakeSecrets) Update(secret *corev1.Secret) (*corev1.Secret, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.updates++
	f.secret = secret.DeepCopy()
	return secret.DeepCopy(), nil
}

func (f *fakeSecrets) Get(namespace, name string) (*corev1.Secret, error) {
	if f.mtls != nil && f.mtls.Namespace == namespace && f.mtls.Name == name {
		return f.mtls.DeepCopy(), nil
	}
	if f.secret == nil || f.secret.Namespace != namespace || f.secret.Name != name {
		return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
	}
	return f.secret.DeepCopy(), nil
}

type fakeMachines struct {
	machines map[string]*capiv1beta2.Machine
}

func (f *fakeMachines) Get(namespace, name string) (*capiv1beta2.Machine, error) {
	machine, ok := f.machines[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "cluster.x-k8s.io", Resource: "machines"}, name)
	}
	return machine, nil
}

type fakeClusters struct {
	clusters map[string]*provv1.Cluster
}

func (f *fakeClusters) Get(namespace, name string) (*provv1.Cluster, error) {
	cluster, ok := f.clusters[namespace+"/"+name]
	if !ok {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "provisioning.cattle.io", Resource: "clusters"}, name)
	}
	return cluster, nil
}

type fakeEvents struct {
	events []*corev1.Event
}

func (f *fakeEvents) Create(event *corev1.Event) (*corev1.Event, error) {
	f.events = append(f.events, event)
	return event, nil
}

func testController() (*Controller, *fakeSecrets, *fakeMachines, *fakeClusters, *fakeEvents, bootstrap.InstallConfig) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-1-machine-bootstrap", Namespace: "fleet-default"},
		Type:       bootstrapSecretType,
		Data:       map[string][]byte{bootstrapValueKey: []byte("#!/bin/bash\necho start\ndo_install \"$@\"\nexit 0\n")},
	}
	mtls := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "dragonfly-p2p-mtls", Namespace: "dragonfly-system"},
		Data: map[string][]byte{
			"ca.crt":                []byte("ca"),
			corev1.TLSCertKey:       []byte("cert"),
			corev1.TLSPrivateKeyKey: []byte("key"),
		},
	}
	machine := &capiv1beta2.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: "machine-1", Namespace: "fleet-default",
		Labels: map[string]string{capiClusterName: "c1"},
	}}
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "c1", Namespace: "fleet-default",
			Labels:      map[string]string{labelEnabled: "true"},
			Annotations: map[string]string{annotationSeed: "true", "p2p.dragonfly.io/scheduler-cluster-id": "2"},
		},
		Status: provv1.ClusterStatus{ClusterName: "c-m-test"},
	}
	secrets := &fakeSecrets{secret: secret, mtls: mtls}
	events := &fakeEvents{}
	cfg := bootstrap.InstallConfig{
		ManagerGRPCDomain: "grpc.example.com",
		ManagerGRPCPort:   443,
		StaticURL:         "http://static.example.com",
		SchedulerDomain:   "example.com",
		ClusterIDFallback: "c-m-fallback",
	}
	c := &Controller{
		secrets:  secrets,
		mtls:     secrets,
		machines: &fakeMachines{machines: map[string]*capiv1beta2.Machine{"fleet-default/machine-1": machine}},
		clusters: &fakeClusters{clusters: map[string]*provv1.Cluster{"fleet-default/c1": cluster}},
		events:   events,
		opts: Options{
			OperatorNamespace: "dragonfly-system",
			MTLSSecretName:    "dragonfly-p2p-mtls",
			ManagerGRPCDomain: cfg.ManagerGRPCDomain,
			ManagerGRPCPort:   cfg.ManagerGRPCPort,
			StaticURL:         cfg.StaticURL,
			SchedulerDomain:   cfg.SchedulerDomain,
			ClusterIDFallback: cfg.ClusterIDFallback,
		},
	}
	return c, secrets, c.machines.(*fakeMachines), c.clusters.(*fakeClusters), events, cfg
}

func TestReconcileInsertsBeforeDoInstall(t *testing.T) {
	controller, secrets, _, _, events, _ := testController()
	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	blockAt := strings.Index(value, bootstrap.Marker)
	doInstallAt := strings.Index(value, `do_install "$@"`)
	if blockAt < 0 || doInstallAt < 0 || blockAt > doInstallAt {
		t.Fatalf("bootstrap block is not before do_install: %q", value)
	}
	if !strings.Contains(value[blockAt:doInstallAt], "bash /tmp/d7y-p2p-install.sh || true") {
		t.Fatalf("bootstrap block is not best-effort under bash: %q", value)
	}
	if strings.Contains(value, "p2p-install.sh?cfg=") {
		t.Fatalf("bootstrap block still downloads p2p-install.sh: %q", value)
	}
	if !strings.Contains(value, "MANAGER_GRPC='https://grpc.example.com:443'") {
		t.Fatalf("bootstrap block is missing the inline manager env: %q", value)
	}
	if !strings.Contains(value, "P2P_DISTRO='k3s'") || !strings.Contains(value, "SCHEDULER_IPS=''") {
		t.Fatalf("bootstrap block is missing the empty scheduler IP env: %q", value)
	}
	if len(events.events) != 1 || events.events[0].Reason != "BootstrapInjected" {
		t.Errorf("events = %+v, want BootstrapInjected", events.events)
	}

	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	if secrets.updates != 1 {
		t.Errorf("Secret updates = %d, want 1 (idempotent)", secrets.updates)
	}
}

func TestReconcileFallsBackToLastExitZero(t *testing.T) {
	controller, secrets, _, _, _, _ := testController()
	secrets.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\necho start\nexit 0\necho after\nexit 0\n")
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	lastExit := strings.LastIndex(value, "exit 0")
	blockAt := strings.Index(value, bootstrap.Marker)
	if blockAt < 0 || lastExit < 0 || blockAt > lastExit {
		t.Fatalf("bootstrap block is not before the last exit 0: %q", value)
	}
}

func TestInlineBlockIsSelfContainedAndBestEffort(t *testing.T) {
	block := inlineInstallBlock("STATIC_URL=http://static.example.com\n")
	if !strings.HasPrefix(block, bootstrap.Marker+"\nif command -v bash >/dev/null 2>&1; then\n") {
		t.Fatalf("block start = %q", block)
	}
	for _, want := range []string{
		"cat > /tmp/d7y-p2p-install.sh <<'D7Y_P2P_EOF'",
		"\nD7Y_P2P_EOF\nbash /tmp/d7y-p2p-install.sh || true\nfi\n",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "\n(\n") || strings.Contains(block, ") || true") {
		t.Fatalf("block still uses the old subshell wrapper: %q", block)
	}
	if strings.HasPrefix(p2pInstallBody, "#!") {
		t.Fatal("embedded script body still starts with a shebang")
	}
	if strings.Contains(block, "p2p-install.sh?cfg=") {
		t.Fatal("embedded block still downloads p2p-install.sh")
	}
}

func TestInlineBlockParsesUnderPOSIXShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}
	block := inlineInstallBlockFor("echo p2p-ok\n", "STATIC_URL=http://static.example.com\n")
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(block)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n rejected the block: %v\n%s", err, out)
	}
}

func TestInlineBlockDelimiterCollision(t *testing.T) {
	block := inlineInstallBlockFor("echo D7Y_P2P_EOF\n", "")
	if !strings.Contains(block, "cat > /tmp/d7y-p2p-install.sh <<'D7Y_P2P_EOF_1'") {
		t.Fatalf("block did not choose a suffixed delimiter: %q", block)
	}
	if !strings.Contains(block, "\nD7Y_P2P_EOF_1\nbash /tmp/d7y-p2p-install.sh || true") {
		t.Fatalf("block did not close with the suffixed delimiter: %q", block)
	}
}

func TestReconcileCarriesSchedulerIPs(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	clusters.clusters["fleet-default/c1"].Annotations["p2p.dragonfly.io/scheduler-ips"] = "203.0.113.10,203.0.113.11"
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	if !strings.Contains(value, "SCHEDULER_IPS='203.0.113.10,203.0.113.11'") {
		t.Fatalf("bootstrap block is missing scheduler IPs: %q", value)
	}
}

func TestReconcileNonGatedClusterUntouched(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	clusters.clusters["fleet-default/c1"].Annotations[annotationSeed] = "false"
	original := string(secrets.secret.Data[bootstrapValueKey])

	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if got := string(secrets.secret.Data[bootstrapValueKey]); got != original {
		t.Errorf("bootstrap value changed for a non-gated cluster: %q", got)
	}
	if secrets.updates != 0 {
		t.Errorf("Secret updates = %d, want 0", secrets.updates)
	}
	if len(events.events) != 0 {
		t.Errorf("events = %+v, want none", events.events)
	}
}

func TestReconcileAppendsWithoutExitZero(t *testing.T) {
	controller, secrets, _, _, _, _ := testController()
	secrets.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\necho start\n")
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	if !strings.HasSuffix(value, "fi\n"+bootstrap.EndMarker+"\n") {
		t.Errorf("bootstrap value = %q, want the block appended with an end marker", value)
	}
}

func TestReconcilePatchFailureEmitsEvent(t *testing.T) {
	controller, secrets, _, _, events, _ := testController()
	secrets.updateErr = errors.New("secret update denied")
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err == nil {
		t.Fatal("reconcile() error = nil, want update error")
	}
	if len(events.events) == 0 || events.events[len(events.events)-1].Reason != "BootstrapPatchFailed" {
		t.Errorf("events = %+v, want BootstrapPatchFailed", events.events)
	}
}

func TestReconcileMissingSchedulerIDInjectsPlaceholder(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	delete(clusters.clusters["fleet-default/c1"].Annotations, "p2p.dragonfly.io/scheduler-cluster-id")

	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	if !strings.Contains(value, "SCHEDULER_CLUSTER_ID='0'\n") {
		t.Fatalf("bootstrap block is missing the scheduler ID placeholder: %q", value)
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("bootstrap marker is missing: %q", value)
	}
	if secrets.updates != 1 {
		t.Errorf("Secret updates = %d, want 1", secrets.updates)
	}
	var placeholder, injected bool
	for _, event := range events.events {
		switch event.Reason {
		case "SchedulerClusterIDPlaceholder":
			placeholder = event.Type == corev1.EventTypeWarning && strings.Contains(event.Message, "placeholder")
		case "BootstrapInjected":
			injected = true
		}
	}
	if !placeholder || !injected {
		t.Errorf("events = %+v, want placeholder warning and BootstrapInjected", events.events)
	}

	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	if secrets.updates != 1 {
		t.Errorf("Secret updates = %d, want 1 after idempotent re-sync", secrets.updates)
	}
}

func TestReconcileRebuildsBlockOnClientRevisionChange(t *testing.T) {
	controller, secrets, _, _, _, _ := testController()
	secret := secrets.secret.DeepCopy()
	if _, err := controller.reconcile("", secret); err != nil {
		t.Fatalf("first reconcile() error = %v", err)
	}
	first := string(secrets.secret.Data[bootstrapValueKey])
	if secrets.updates != 1 {
		t.Fatalf("updates = %d, want 1", secrets.updates)
	}
	previousRevision := secrets.secret.Annotations[bootstrap.ClientCertRevisionAnnotation]
	if previousRevision == "" {
		t.Fatal("client revision annotation is empty")
	}

	secrets.mtls.Data[corev1.TLSCertKey] = []byte("rotated-cert")
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("second reconcile() error = %v", err)
	}
	second := string(secrets.secret.Data[bootstrapValueKey])
	if secrets.updates != 2 {
		t.Errorf("updates = %d, want 2 after client rotation", secrets.updates)
	}
	if strings.Count(second, bootstrap.Marker+"\n") != 1 || strings.Count(second, bootstrap.EndMarker+"\n") != 1 {
		t.Errorf("bootstrap block markers were not replaced exactly once: %q", second)
	}
	if second == first {
		t.Error("bootstrap block did not change after client revision rotation")
	}
	if got := secrets.secret.Annotations[bootstrap.ClientCertRevisionAnnotation]; got == previousRevision {
		t.Error("client revision annotation did not change")
	}
}

func TestInsertInlineBlockReplacesLegacyBlockWithoutEndMarker(t *testing.T) {
	legacy := strings.TrimSuffix(inlineInstallBlockFor("echo legacy\n", "SCHEDULER_IPS=''\n"), bootstrap.EndMarker+"\n")
	raw := "#!/bin/bash\n" + legacy + `do_install "$@"` + "\nexit 0\n"
	next := inlineInstallBlockFor("echo new\n", "SCHEDULER_IPS='203.0.113.10'\n")
	got, err := insertInlineBlock(raw, next)
	if err != nil {
		t.Fatalf("insertInlineBlock() error = %v", err)
	}
	if strings.Contains(got, "echo legacy") {
		t.Errorf("legacy block was not removed: %q", got)
	}
	if !strings.Contains(got, "echo new") {
		t.Errorf("new block was not inserted: %q", got)
	}
	if strings.Count(got, bootstrap.Marker+"\n") != 1 || strings.Count(got, bootstrap.EndMarker+"\n") != 1 {
		t.Errorf("block markers = %q", got)
	}
	if !strings.Contains(got, "do_install \"$@\"\n") {
		t.Errorf("do_install line was lost: %q", got)
	}
}

func TestClientSecretChangeEnqueuesBootstrapSecrets(t *testing.T) {
	controller, secrets, _, _, _, _ := testController()
	if _, err := controller.clientSecretChanged("", secrets.mtls.DeepCopy()); err != nil {
		t.Fatalf("clientSecretChanged() error = %v", err)
	}
	if len(secrets.enqueued) != 1 || secrets.enqueued[0] != "fleet-default/machine-1-machine-bootstrap" {
		t.Errorf("enqueued = %v, want the bootstrap Secret", secrets.enqueued)
	}
	if _, err := controller.clientSecretChanged("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("clientSecretChanged(bootstrap) error = %v", err)
	}
	if len(secrets.enqueued) != 1 {
		t.Errorf("enqueued = %v, want no change for a non-client Secret", secrets.enqueued)
	}
}

func TestInsertInlineBlockRejectsUnknownLegacyShape(t *testing.T) {
	raw := "#!/bin/bash\n" + bootstrap.Marker + "\ndo_install \"$@\"\n"
	if _, err := insertInlineBlock(raw, inlineInstallBlockFor("echo new\n", "")); err == nil {
		t.Fatal("insertInlineBlock() error = nil, want unrecognized legacy shape error")
	}
}

func TestSchedulerInputsChangeReinjectsBlock(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	cluster := clusters.clusters["fleet-default/c1"]
	cluster.Annotations["p2p.dragonfly.io/scheduler-ips"] = "203.0.113.10"
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	if !strings.Contains(value, "SCHEDULER_IPS='203.0.113.10'") {
		t.Fatalf("initial block missing scheduler IP: %q", value)
	}
	if secrets.updates != 1 {
		t.Fatalf("updates = %d, want 1", secrets.updates)
	}
	firstFingerprint := secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation]
	if firstFingerprint == "" {
		t.Fatal("bootstrap-inputs fingerprint is empty")
	}

	cluster.Annotations["p2p.dragonfly.io/scheduler-ips"] = "203.0.113.11"
	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("IP-change reconcile() error = %v", err)
	}
	value = string(secrets.secret.Data[bootstrapValueKey])
	if !strings.Contains(value, "SCHEDULER_IPS='203.0.113.11'") || strings.Contains(value, "SCHEDULER_IPS='203.0.113.10'") {
		t.Fatalf("block did not replace scheduler IP: %q", value)
	}
	if secrets.updates != 2 {
		t.Errorf("updates = %d, want 2", secrets.updates)
	}
	if secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation] == firstFingerprint {
		t.Error("bootstrap-inputs fingerprint did not change with scheduler IPs")
	}

	if _, err := controller.reconcile("", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("equal reconcile() error = %v", err)
	}
	if secrets.updates != 2 {
		t.Errorf("updates = %d, want 2 for equal inputs", secrets.updates)
	}
}

func TestClusterChangedEnqueuesOnlyMatchingBootstrapSecrets(t *testing.T) {
	controller, secrets, machines, clusters, _, _ := testController()
	otherMachine := &capiv1beta2.Machine{ObjectMeta: metav1.ObjectMeta{
		Name: "machine-2", Namespace: "fleet-default",
		Labels: map[string]string{capiClusterName: "c2"},
	}}
	machines.machines["fleet-default/machine-2"] = otherMachine
	clusters.clusters["fleet-default/c2"] = &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c2", Namespace: "fleet-default"}}
	otherSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "machine-2-machine-bootstrap", Namespace: "fleet-default"},
		Type:       bootstrapSecretType,
	}
	secrets.others = append(secrets.others, otherSecret)

	if _, err := controller.clusterChanged("", clusters.clusters["fleet-default/c1"]); err != nil {
		t.Fatalf("clusterChanged() error = %v", err)
	}
	if len(secrets.enqueued) != 1 || secrets.enqueued[0] != "fleet-default/machine-1-machine-bootstrap" {
		t.Errorf("enqueued = %v, want only c1 bootstrap", secrets.enqueued)
	}
	listCalls := secrets.listCalls

	// Same scheduler inputs: no second list/enqueue.
	if _, err := controller.clusterChanged("", clusters.clusters["fleet-default/c1"]); err != nil {
		t.Fatalf("clusterChanged(unchanged) error = %v", err)
	}
	if len(secrets.enqueued) != 1 || secrets.listCalls != listCalls {
		t.Errorf("unchanged inputs enqueued %v or listed %d times", secrets.enqueued, secrets.listCalls)
	}

	// Changed scheduler IPs: one new enqueue for c1.
	c1 := clusters.clusters["fleet-default/c1"]
	c1.Annotations["p2p.dragonfly.io/scheduler-ips"] = "203.0.113.10"
	if _, err := controller.clusterChanged("", c1); err != nil {
		t.Fatalf("clusterChanged(changed) error = %v", err)
	}
	if len(secrets.enqueued) != 2 || secrets.enqueued[1] != "fleet-default/machine-1-machine-bootstrap" {
		t.Errorf("changed inputs enqueued %v", secrets.enqueued)
	}

	// Non-seed and non-enabled clusters are ignored.
	nonSeed := c1.DeepCopy()
	nonSeed.Annotations["p2p.dragonfly.io/seed"] = "false"
	if _, err := controller.clusterChanged("", nonSeed); err != nil {
		t.Fatalf("clusterChanged(non-seed) error = %v", err)
	}
	nonEnabled := c1.DeepCopy()
	nonEnabled.Labels[labelEnabled] = "false"
	if _, err := controller.clusterChanged("", nonEnabled); err != nil {
		t.Fatalf("clusterChanged(non-enabled) error = %v", err)
	}
	if len(secrets.enqueued) != 2 {
		t.Errorf("non-seed/non-enabled cluster enqueued %v", secrets.enqueued)
	}
}

func setPreinstall(clusters *fakeClusters, body string) {
	clusters.clusters["fleet-default/c1"].Annotations[bootstrap.PreinstallAnnotation] = body
}

func eventByReason(events *fakeEvents, reason string) *corev1.Event {
	for _, event := range events.events {
		if event.Reason == reason {
			return event
		}
	}
	return nil
}

func reconcileNow(t *testing.T, controller *Controller, secrets *fakeSecrets) {
	t.Helper()
	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
}

func TestReconcileInjectsPreinstallBeforeP2P(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	const body = "echo pre-install"
	setPreinstall(clusters, body)
	reconcileNow(t, controller, secrets)

	value := string(secrets.secret.Data[bootstrapValueKey])
	if count := strings.Count(value, bootstrap.PreinstallMarker+"\n"); count != 1 {
		t.Fatalf("pre-install marker count = %d, want 1: %q", count, value)
	}
	if count := strings.Count(value, bootstrap.Marker+"\n"); count != 1 {
		t.Fatalf("p2p marker count = %d, want 1: %q", count, value)
	}
	preAt := strings.Index(value, bootstrap.PreinstallMarker)
	p2pAt := strings.Index(value, bootstrap.Marker)
	if preAt < 0 || p2pAt < 0 || preAt > p2pAt {
		t.Fatalf("pre-install block is not before the p2p block: %q", value)
	}
	if !strings.Contains(value, body+"\n") {
		t.Fatalf("pre-install body is missing: %q", value)
	}
	injected := eventByReason(events, "BootstrapInjected")
	if injected == nil || !strings.Contains(injected.Message, "included") {
		t.Fatalf("BootstrapInjected message does not indicate an included pre-install: %+v", events.events)
	}
	if strings.Contains(injected.Message, body) {
		t.Fatalf("BootstrapInjected event contains the annotation body: %q", injected.Message)
	}
}

func TestReconcileEmptyPreinstallIsP2POnly(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	setPreinstall(clusters, "  \n\t ")
	reconcileNow(t, controller, secrets)

	value := string(secrets.secret.Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("pre-install block was injected for a whitespace annotation: %q", value)
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value)
	}
	injected := eventByReason(events, "BootstrapInjected")
	if injected == nil || !strings.Contains(injected.Message, "p2p-only") {
		t.Fatalf("BootstrapInjected message is not explicit about p2p-only: %+v", events.events)
	}
}

func TestReconcileNonGatedClusterIgnoresPreinstall(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	setPreinstall(clusters, "echo pre")
	clusters.clusters["fleet-default/c1"].Labels[labelEnabled] = "false"
	original := string(secrets.secret.Data[bootstrapValueKey])

	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if value != original || secrets.updates != 0 || strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("non-gated cluster was modified: %q", value)
	}
}

func TestReconcilePreinstallTextChangeReplacesBlock(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	setPreinstall(clusters, "echo first")
	reconcileNow(t, controller, secrets)
	firstFingerprint := secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation]

	setPreinstall(clusters, "echo second")
	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if count := strings.Count(value, bootstrap.PreinstallMarker+"\n"); count != 1 {
		t.Fatalf("pre-install marker count = %d, want 1: %q", count, value)
	}
	if strings.Contains(value, "echo first") || !strings.Contains(value, "echo second") {
		t.Fatalf("pre-install body was not replaced: %q", value)
	}
	if secrets.updates != 2 {
		t.Errorf("Secret updates = %d, want 2", secrets.updates)
	}
	if fingerprint := secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation]; fingerprint == firstFingerprint {
		t.Error("bootstrap-inputs fingerprint did not change with the pre-install body")
	}

	reconcileNow(t, controller, secrets)
	if secrets.updates != 2 {
		t.Errorf("Secret updates = %d, want 2 for identical text", secrets.updates)
	}
}

func TestReconcilePreinstallRemovalKeepsP2P(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	setPreinstall(clusters, "echo pre")
	reconcileNow(t, controller, secrets)
	beforeFingerprint := secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation]

	delete(clusters.clusters["fleet-default/c1"].Annotations, bootstrap.PreinstallAnnotation)
	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("pre-install block survived annotation removal: %q", value)
	}
	if !strings.Contains(value, bootstrap.Marker) || !strings.Contains(value, `do_install "$@"`) {
		t.Fatalf("p2p block or do_install was lost: %q", value)
	}
	if secrets.updates != 2 {
		t.Errorf("Secret updates = %d, want 2", secrets.updates)
	}
	if fingerprint := secrets.secret.Annotations[bootstrap.BootstrapInputsAnnotation]; fingerprint == beforeFingerprint {
		t.Error("bootstrap-inputs fingerprint did not change after annotation removal")
	}
}

func TestReconcilePreinstallRepeatedTenTimesIsStable(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	setPreinstall(clusters, "echo pre")
	for i := 0; i < 10; i++ {
		reconcileNow(t, controller, secrets)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	if secrets.updates != 1 {
		t.Errorf("Secret updates = %d, want 1", secrets.updates)
	}
	if count := strings.Count(value, bootstrap.PreinstallMarker+"\n"); count != 1 {
		t.Fatalf("pre-install marker count = %d, want 1: %q", count, value)
	}
	if count := strings.Count(value, bootstrap.Marker+"\n"); count != 1 {
		t.Fatalf("p2p marker count = %d, want 1: %q", count, value)
	}
}

func TestReconcilePreinstallWithLegacyP2PBlock(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	legacy := strings.TrimSuffix(inlineInstallBlockFor("echo legacy\n", "SCHEDULER_IPS=''\n"), bootstrap.EndMarker+"\n")
	secrets.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\n" + legacy + `do_install "$@"` + "\nexit 0\n")
	setPreinstall(clusters, "echo pre")

	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if strings.Contains(value, "echo legacy") {
		t.Fatalf("legacy p2p block was not replaced: %q", value)
	}
	if count := strings.Count(value, bootstrap.PreinstallMarker+"\n"); count != 1 {
		t.Fatalf("pre-install marker count = %d, want 1: %q", count, value)
	}
	if count := strings.Count(value, bootstrap.Marker+"\n"); count != 1 {
		t.Fatalf("p2p marker count = %d, want 1: %q", count, value)
	}
	if strings.Index(value, bootstrap.PreinstallMarker) > strings.Index(value, bootstrap.Marker) {
		t.Fatalf("pre-install block is not before the p2p block: %q", value)
	}
}

func TestBootstrapInputsFingerprintTracksPreinstall(t *testing.T) {
	cluster := &provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "fleet-default"}}
	base := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "")
	first := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo first")
	second := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo second")
	if base == first || first == second {
		t.Fatalf("fingerprints do not track the pre-install body: %q %q %q", base, first, second)
	}
	padded := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "  echo first\n")
	if padded != first {
		t.Error("fingerprint is sensitive to surrounding whitespace")
	}
}

func TestBootstrapInputsFingerprintPreservesLegacyEmptyPreinstall(t *testing.T) {
	cluster := &provv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "c1", Namespace: "fleet-default",
			Annotations: map[string]string{
				bootstrap.SchedulerIPsAnnotation:       "203.0.113.10",
				bootstrap.SchedulerClusterIDAnnotation: "2",
			},
		},
		Spec: provv1.ClusterSpec{KubernetesVersion: "v1.30.0+rke2r1"},
	}
	legacy := sha256.Sum256([]byte("rev" + "\x00" + "203.0.113.10" + "\x00" + "2" + "\x00" + "rke2" +
		"\x00" + "v1" + "\x00" + "v2" + "\x00" + "v3"))
	want := hex.EncodeToString(legacy[:])
	if got := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", ""); got != want {
		t.Fatalf("empty preinstall fingerprint = %s, want legacy v0.3.21 %s", got, want)
	}
	if got := bootstrapInputsFingerprint(cluster, "rev", "v1", "v2", "v3", "echo pre"); got == want {
		t.Fatal("non-empty preinstall fingerprint still matches the legacy hash")
	}
}

func TestReconcileInvalidPreinstallIsSkipped(t *testing.T) {
	bodies := map[string]string{
		"NUL":          "echo bad\x00",
		"start marker": "echo ok\n" + bootstrap.PreinstallMarker + "\necho bad",
		"end marker":   bootstrap.PreinstallEndMarker,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			controller, secrets, _, clusters, events, _ := testController()
			setPreinstall(clusters, body)
			reconcileNow(t, controller, secrets)
			value := string(secrets.secret.Data[bootstrapValueKey])
			if strings.Contains(value, bootstrap.PreinstallMarker) {
				t.Fatalf("pre-install block was injected for an invalid body: %q", value)
			}
			if strings.Contains(value, "echo bad") {
				t.Fatalf("invalid body leaked into the bootstrap value: %q", value)
			}
			if !strings.Contains(value, bootstrap.Marker) {
				t.Fatalf("p2p block is missing: %q", value)
			}
			event := eventByReason(events, "PreinstallInvalid")
			if event == nil || event.Type != corev1.EventTypeWarning {
				t.Fatalf("events = %+v, want a PreinstallInvalid warning", events.events)
			}
			if strings.Contains(event.Message, "echo bad") {
				t.Fatalf("PreinstallInvalid event contains the annotation body: %q", event.Message)
			}
		})
	}
}

func TestReconcileOversizedPreinstallAnnotationIsSkipped(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	body := strings.Repeat("x", bootstrap.MaxPreinstallAnnotationBytes+1)
	setPreinstall(clusters, body)
	reconcileNow(t, controller, secrets)

	value := string(secrets.secret.Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("pre-install block was injected for an oversized annotation: %q", value)
	}
	if strings.Contains(value, body) {
		t.Fatal("oversized annotation was truncated into the bootstrap value")
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value)
	}
	event := eventByReason(events, "PreinstallTooLarge")
	if event == nil || event.Type != corev1.EventTypeWarning {
		t.Fatalf("events = %+v, want a PreinstallTooLarge warning", events.events)
	}
	if strings.Contains(event.Message, body) {
		t.Fatal("PreinstallTooLarge event contains the annotation body")
	}

	reconcileNow(t, controller, secrets)
	if secrets.updates != 1 {
		t.Errorf("Secret updates = %d, want 1 after an oversized-annotation reconcile pair", secrets.updates)
	}
}

func TestReconcileOversizedSerializedSecretSkipsPreinstall(t *testing.T) {
	controller, secrets, _, clusters, events, _ := testController()
	base := "#!/bin/bash\n#" + strings.Repeat("x", 620*1024) + "\ndo_install \"$@\"\nexit 0\n"
	secrets.secret.Data[bootstrapValueKey] = []byte(base)
	body := "echo " + strings.Repeat("y", 200*1024)
	setPreinstall(clusters, body)

	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatal("pre-install block was injected into an oversized Secret")
	}
	if strings.Contains(value, "yyyy") {
		t.Fatal("pre-install body leaked into an oversized Secret")
	}
	if !strings.Contains(value, bootstrap.Marker) {
		t.Fatalf("p2p block is missing: %q", value[:min(len(value), 200)])
	}
	if len(value) < 620*1024 {
		t.Fatalf("bootstrap value was truncated to %d bytes", len(value))
	}
	raw, err := json.Marshal(secrets.secret)
	if err != nil {
		t.Fatalf("json.Marshal(secret) error = %v", err)
	}
	if len(raw) > 1<<20 {
		t.Fatalf("serialized Secret is %d bytes, want <= 1 MiB", len(raw))
	}
	event := eventByReason(events, "PreinstallTooLarge")
	if event == nil || event.Type != corev1.EventTypeWarning {
		t.Fatalf("events = %+v, want a PreinstallTooLarge warning", events.events)
	}
}

func TestReconcileLargeSecretWithoutAnnotationDoesNotWarn(t *testing.T) {
	controller, secrets, _, _, events, _ := testController()
	secrets.secret.Data[bootstrapValueKey] = []byte("#!/bin/bash\n#" + strings.Repeat("x", 990*1024) + "\ndo_install \"$@\"\nexit 0\n")
	reconcileNow(t, controller, secrets)
	if event := eventByReason(events, "PreinstallTooLarge"); event != nil {
		t.Fatalf("unexpected PreinstallTooLarge without a pre-install annotation: %+v", event)
	}
}

func TestReconcileEventsDoNotContainPreinstallBody(t *testing.T) {
	const sentinel = "D7Y_TEST_SENTINEL_BODY"
	controller, secrets, _, clusters, events, _ := testController()
	setPreinstall(clusters, "echo "+sentinel)
	reconcileNow(t, controller, secrets)
	for _, event := range events.events {
		if strings.Contains(event.Message, sentinel) {
			t.Fatalf("event %q contains the annotation body", event.Reason)
		}
	}
}

func TestReconcileNonBootstrapSecretIsUntouched(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	setPreinstall(clusters, "echo pre")
	secrets.secret.Type = corev1.SecretTypeOpaque
	original := string(secrets.secret.Data[bootstrapValueKey])

	reconcileNow(t, controller, secrets)
	value := string(secrets.secret.Data[bootstrapValueKey])
	if value != original || secrets.updates != 0 || strings.Contains(value, bootstrap.PreinstallMarker) {
		t.Fatalf("non-bootstrap Secret was modified: %q", value)
	}
}

func TestClusterChangedEnqueuesOnPreinstallChange(t *testing.T) {
	controller, secrets, _, clusters, _, _ := testController()
	cluster := clusters.clusters["fleet-default/c1"]
	if _, err := controller.clusterChanged("", cluster); err != nil {
		t.Fatalf("clusterChanged() error = %v", err)
	}
	if len(secrets.enqueued) != 1 {
		t.Fatalf("enqueued = %v, want the bootstrap Secret", secrets.enqueued)
	}
	setPreinstall(clusters, "echo pre")
	if _, err := controller.clusterChanged("", cluster); err != nil {
		t.Fatalf("clusterChanged(preinstall) error = %v", err)
	}
	if len(secrets.enqueued) != 2 {
		t.Fatalf("enqueued = %v, want a second enqueue after the pre-install change", secrets.enqueued)
	}
}
