package helmop

import (
	"fmt"
	"strconv"
	"strings"
)

// ReplicasTemplate returns a Fleet values template that reads a replica count
// from the Cluster annotations and falls back to "1".
func ReplicasTemplate(annotation string) string {
	return fmt.Sprintf(`${ index .ClusterAnnotations %q | default "1" }`, annotation)
}

// ReplicasOrOne returns the Fleet replica template when value is empty or a
// positive integer. Invalid values fall back to "1" so a bad annotation cannot
// break the control/data releases.
func ReplicasOrOne(annotation, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ReplicasTemplate(annotation)
	}
	if n, err := strconv.Atoi(trimmed); err != nil || n < 1 {
		return "1"
	}
	return ReplicasTemplate(annotation)
}

// certRevisionAnnotation is stamped on every workload that mounts the P2P
// Secret. The value is the leaf certificate hash, so unchanged certificates do
// not cause rollouts and a rotation changes the pod template exactly once.
const certRevisionAnnotation = "p2p.dragonfly.io/cert-revision"

func certPodAnnotations(revision string) map[string]any {
	if revision == "" {
		return nil
	}
	return map[string]any{certRevisionAnnotation: revision}
}

func tlsPaths() map[string]any {
	return map[string]any{
		"caCert": CertDir + "/ca.crt",
		"cert":   CertDir + "/tls.crt",
		"key":    CertDir + "/tls.key",
	}
}

func mtlsVolume(secretName string) map[string]any {
	return map[string]any{
		"name": "mtls",
		"secret": map[string]any{
			"secretName": secretName,
		},
	}
}

func mtlsVolumeMount() map[string]any {
	return map[string]any{
		"name":      "mtls",
		"mountPath": CertDir,
		"readOnly":  true,
	}
}

func volumes(secretName string, base ...map[string]any) []any {
	result := make([]any, 0, len(base)+1)
	for _, volume := range base {
		result = append(result, volume)
	}
	return append(result, mtlsVolume(secretName))
}

func volumeMounts(base ...map[string]any) []any {
	result := make([]any, 0, len(base)+1)
	for _, mount := range base {
		result = append(result, mount)
	}
	return append(result, mtlsVolumeMount())
}

func logVolume() map[string]any {
	return map[string]any{"name": "logs", "emptyDir": map[string]any{}}
}

func logVolumeMount(path string) map[string]any {
	return map[string]any{"name": "logs", "mountPath": path}
}

// monitoringComponents are the Dragonfly components with top-level chart
// metrics and ServiceMonitor toggles.
var monitoringComponents = []string{"manager", "scheduler", "seedClient", "client"}

// applyMonitoring enables chart-level metrics and ServiceMonitors on every
// Dragonfly component when the Cluster monitoring selector matched. When
// disabled it leaves the chart defaults untouched (no forced ServiceMonitor).
func applyMonitoring(values map[string]any, enabled bool) {
	if !enabled {
		return
	}
	for _, name := range monitoringComponents {
		component, ok := values[name].(map[string]any)
		if !ok {
			continue
		}
		if enabled, _ := component["enable"].(bool); !enabled {
			continue
		}
		component["metrics"] = map[string]any{
			"enable": true,
			"serviceMonitor": map[string]any{
				"enable": true,
			},
		}
	}
}
