package apply

import (
	"context"
	"testing"

	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestEnsureHelmOp(t *testing.T) {
	ctx := context.Background()
	store := &fakeStore{objects: map[string]*unstructured.Unstructured{}}

	desired := &fleetv1alpha1.HelmOp{
		TypeMeta: metav1.TypeMeta{APIVersion: "fleet.cattle.io/v1alpha1", Kind: "HelmOp"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "c1-keydb",
			Namespace: "fleet-default",
			Labels:    map[string]string{ManagedByLabel: ManagedByValue},
		},
	}
	desired.Spec.TargetNamespace = "dragonfly-system"

	if _, err := Ensure(ctx, store, desired); err != nil {
		t.Fatalf("Ensure(create) error = %v", err)
	}
	if len(store.objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(store.objects))
	}

	if _, err := Ensure(ctx, store, desired); err != nil {
		t.Fatalf("Ensure(no-op) error = %v", err)
	}
	if store.updates != 0 {
		t.Errorf("updates after identical apply = %d, want 0", store.updates)
	}

	changed := desired.DeepCopy()
	changed.Spec.TargetNamespace = "other-system"
	if _, err := Ensure(ctx, store, changed); err != nil {
		t.Fatalf("Ensure(update) error = %v", err)
	}
	if store.updates != 1 {
		t.Errorf("updates after changed apply = %d, want 1", store.updates)
	}
	got := store.objects["fleet-default/c1-keydb"]
	if got.Object["spec"].(map[string]any)["namespace"] != "other-system" {
		t.Errorf("spec not updated: %v", got.Object["spec"])
	}
}

type fakeStore struct {
	objects map[string]*unstructured.Unstructured
	updates int
}

func (f *fakeStore) Get(_ context.Context, namespace, name string, result runtime.Object, _ metav1.GetOptions) error {
	stored, ok := f.objects[namespace+"/"+name]
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "helmops"}, name)
	}
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(stored.Object)
	return nil
}

func (f *fakeStore) Create(_ context.Context, namespace string, obj, result runtime.Object, _ metav1.CreateOptions) error {
	created := obj.(*unstructured.Unstructured).DeepCopy()
	f.objects[namespace+"/"+created.GetName()] = created
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(created.Object)
	return nil
}

func (f *fakeStore) Update(_ context.Context, namespace string, obj, result runtime.Object, _ metav1.UpdateOptions) error {
	f.updates++
	updated := obj.(*unstructured.Unstructured).DeepCopy()
	f.objects[namespace+"/"+updated.GetName()] = updated
	result.(*unstructured.Unstructured).Object = runtime.DeepCopyJSON(updated.Object)
	return nil
}

func (f *fakeStore) Delete(_ context.Context, namespace, name string, _ metav1.DeleteOptions) error {
	delete(f.objects, namespace+"/"+name)
	return nil
}
