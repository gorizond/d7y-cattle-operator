package machinebootstrap

import (
	"errors"
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
		Data:       map[string][]byte{bootstrapValueKey: []byte("#!/bin/bash\necho start\nexit 0\necho after\nexit 0\n")},
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

func TestReconcileInsertsBeforeLastExit(t *testing.T) {
	controller, secrets, _, _, events, cfg := testController()
	mtls, _ := secrets.Get("dragonfly-system", "dragonfly-p2p-mtls")
	line, err := bootstrap.BuildInstallLine(
		&provv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "fleet-default", Annotations: map[string]string{"p2p.dragonfly.io/scheduler-cluster-id": "2"}}, Status: provv1.ClusterStatus{ClusterName: "c-m-test"}},
		mtls, "", cfg,
	)
	if err != nil {
		t.Fatalf("BuildInstallLine() error = %v", err)
	}
	if _, err := controller.reconcile("fleet-default/machine-1-machine-bootstrap", secrets.secret.DeepCopy()); err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	value := string(secrets.secret.Data[bootstrapValueKey])
	want := "#!/bin/bash\necho start\nexit 0\necho after\n" + line + "\nexit 0\n"
	if value != want {
		t.Fatalf("bootstrap value = %q, want %q", value, want)
	}
	if strings.Count(value, bootstrap.Marker) != 1 {
		t.Errorf("marker count = %d, want 1", strings.Count(value, bootstrap.Marker))
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
	if !strings.HasSuffix(value, bootstrap.Marker+"\n") {
		t.Errorf("bootstrap value = %q, want the line appended", value)
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
