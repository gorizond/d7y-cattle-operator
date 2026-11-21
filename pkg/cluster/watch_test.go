package cluster

import (
	"context"
	"testing"
	"time"

	provv1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

type fakeWatch struct {
	ch chan watch.Event
}

func (f *fakeWatch) Stop() {}
func (f *fakeWatch) ResultChan() <-chan watch.Event {
	return f.ch
}

type fakeBundleWatcher struct {
	started chan metav1.ListOptions
	events  chan watch.Event
}

func (f *fakeBundleWatcher) Watch(_ context.Context, _ string, opts metav1.ListOptions) (watch.Interface, error) {
	f.started <- opts
	return &fakeWatch{ch: f.events}, nil
}

type enqueueRecorder struct {
	enqueued []string
}

func (f *enqueueRecorder) Update(*provv1.Cluster) (*provv1.Cluster, error) { return nil, nil }
func (f *enqueueRecorder) UpdateStatus(*provv1.Cluster) (*provv1.Cluster, error) {
	return nil, nil
}
func (f *enqueueRecorder) Enqueue(namespace, name string) {
	f.enqueued = append(f.enqueued, namespace+"/"+name)
}

func TestWatchBundlesFiltersOwnerLabel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recorder := &enqueueRecorder{}
	controller := &Controller{ctx: ctx, clusters: recorder}
	watcher := &fakeBundleWatcher{started: make(chan metav1.ListOptions, 1), events: make(chan watch.Event, 2)}
	done := make(chan struct{})
	go func() {
		controller.watchBundles(watcher)
		close(done)
	}()

	select {
	case opts := <-watcher.started:
		if opts.LabelSelector != "p2p-dragonfly-io=p2p-mtls" {
			t.Errorf("watch selector = %q", opts.LabelSelector)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher did not start")
	}

	owned := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fleet.cattle.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]any{
			"name":      "c1-p2p-scheduler",
			"namespace": "fleet-default",
			"labels": map[string]any{
				"p2p-dragonfly-io":                   "p2p-mtls",
				"p2p.dragonfly.io/cluster-name":      "c1",
				"p2p.dragonfly.io/cluster-namespace": "fleet-default",
			},
		},
	}}
	watcher.events <- watch.Event{Type: watch.Added, Object: owned}
	deadline := time.Now().Add(time.Second)
	for len(recorder.enqueued) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(recorder.enqueued) != 1 || recorder.enqueued[0] != "fleet-default/c1" {
		t.Fatalf("enqueued = %v, want only fleet-default/c1", recorder.enqueued)
	}

	unowned := owned.DeepCopy()
	unowned.SetLabels(map[string]string{"p2p-dragonfly-io": "p2p-mtls"})
	watcher.events <- watch.Event{Type: watch.Modified, Object: unowned}
	time.Sleep(50 * time.Millisecond)
	if len(recorder.enqueued) != 1 {
		t.Errorf("unowned Bundle enqueued %v", recorder.enqueued)
	}

	cancel()
	close(watcher.events)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
}
