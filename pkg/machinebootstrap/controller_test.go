package machinebootstrap

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	capiv1beta2 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

type fakeSecrets struct {
	secret    *corev1.Secret
	mtls      *corev1.Secret
	updates   int
	updateErr error
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
	got := insertInlineBlock(raw, next)
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
