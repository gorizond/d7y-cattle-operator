// Package apply creates and updates managed objects through Wrangler and the
// dynamic client. Ownership is expressed with labels because managed objects
// may live in a different namespace than the Cluster that owns them, and
// cross-namespace owner references are invalid.
package apply

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
)

const (
	ManagedByLabel        = "app.kubernetes.io/managed-by"
	ManagedByValue        = "d7y-cattle-operator"
	ClusterNameLabel      = "p2p.dragonfly.io/cluster-name"
	ClusterNamespaceLabel = "p2p.dragonfly.io/cluster-namespace"
)

// Labels returns the ownership labels attached to every managed object.
func Labels(clusterNamespace, clusterName string) map[string]string {
	return map[string]string{
		ManagedByLabel:        ManagedByValue,
		ClusterNameLabel:      clusterName,
		ClusterNamespaceLabel: clusterNamespace,
	}
}

// Ensure creates obj or replaces its mutable fields with the desired state.
// Labels and annotations added by other controllers are preserved.
func Ensure(ctx context.Context, resource dynamic.ResourceInterface, obj runtime.Object) (*unstructured.Unstructured, error) {
	desired, err := toUnstructured(obj)
	if err != nil {
		return nil, err
	}

	current, err := resource.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return resource.Create(ctx, desired, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", desired.GetNamespace(), desired.GetName(), err)
	}

	labels := merge(current.GetLabels(), desired.GetLabels())
	annotations := merge(current.GetAnnotations(), desired.GetAnnotations())
	if equality.Semantic.DeepEqual(current.Object["spec"], desired.Object["spec"]) &&
		equality.Semantic.DeepEqual(labels, current.GetLabels()) &&
		equality.Semantic.DeepEqual(annotations, current.GetAnnotations()) {
		return current, nil
	}

	desired.SetResourceVersion(current.GetResourceVersion())
	desired.SetLabels(labels)
	desired.SetAnnotations(annotations)
	return resource.Update(ctx, desired, metav1.UpdateOptions{})
}

// Delete removes the named object and reports whether it existed.
func Delete(ctx context.Context, resource dynamic.ResourceInterface, name string) (bool, error) {
	err := resource.Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		return u.DeepCopy(), nil
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("convert object to unstructured: %w", err)
	}
	return &unstructured.Unstructured{Object: content}, nil
}

func merge(current, desired map[string]string) map[string]string {
	if len(desired) == 0 {
		return current
	}
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}
