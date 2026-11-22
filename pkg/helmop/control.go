package helmop

import (
	fleetv1alpha1 "github.com/rancher/fleet/pkg/apis/fleet.cattle.io/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Control returns the scheduler HelmOp. It is the only Dragonfly release that
// talks to the manager and keeps all scheduler TLS wiring.
func Control(c Cluster) *fleetv1alpha1.HelmOp {
	names := ClusterNames(c.Name)
	op := newHelmOp(ControlName(c.Name), c, c.Chart, "control", controlValues(c, names), []string{names.KeyDB})
	withBundleDependency(op, SchedulerCertName(c.Name))
	return withMTLS(op, c.MTLSSecretName)
}

// SharedControl returns the shared scheduler HelmOp. The scheduler cluster ID,
// public advertiseIP, and scheduler certificate revision are per-target Fleet
// templates; the release name is fixed.
func SharedControl(c Cluster) *fleetv1alpha1.HelmOp {
	c = sharedCluster(c)
	names := SharedNames()
	values := controlValues(c, names)
	op := newSharedHelmOp(SharedControlName(), TargetNamespace, c, c.Chart, "control",
		values, []string{SharedKeyDBName()}, SharedSelector())
	withSchedulerCertTemplate(op, values)
	withBundleSelector(op, map[string]string{SchedulerCertLabel: "true"})
	return withMTLS(op, c.MTLSSecretName)
}

func controlValues(c Cluster, names Names) map[string]any {
	grpcPort := c.ManagerGRPCPort
	if grpcPort == 0 {
		grpcPort = ManagerGRPCEdgePort
	}
	scheduler := map[string]any{
		"enable":      true,
		"hostNetwork": true,
		"replicas":    c.SchedulerReplicas,
		"config": map[string]any{
			"manager": map[string]any{
				"schedulerClusterID": schedulerClusterIDValue(c),
				// The manager client uses the shared client identity.
				"tls": tlsPathsAt(ClientCertDir),
			},
			"server": map[string]any{
				"tls": tlsPathsAt(CertDir),
				// Bootstrap nodes receive the public scheduler address from
				// the manager dynconfig; DaemonSets use their local dynconfig.
				"advertiseIP":   SharedSchedulerPublicIPTemplate,
				"advertisePort": SchedulerPort,
			},
			"seedPeer": seedPeerConfig(c.P2PDataTLS),
		},
		"extraVolumes": volumes(
			secretVolume("scheduler-mtls", SchedulerMTLSSecretName),
			secretVolume("client-mtls", c.MTLSSecretName),
			logVolume(),
		),
		"extraVolumeMounts": volumeMounts(
			secretVolumeMount("scheduler-mtls", CertDir),
			secretVolumeMount("client-mtls", ClientCertDir),
			logVolumeMount("/var/log/dragonfly/scheduler"),
		),
		// The client identity is shared; its revision is a literal. The
		// shared control release replaces the scheduler revision with a
		// per-target Fleet template.
		"podAnnotations": map[string]any{},
	}
	if c.MTLSCertRevision != "" {
		scheduler["podAnnotations"].(map[string]any)[certRevisionAnnotation] = c.MTLSCertRevision
	}
	if c.SchedulerCertRevision != "" {
		scheduler["podAnnotations"].(map[string]any)[AnnotationMTLSCertRevision] = c.SchedulerCertRevision
	}
	if len(scheduler["podAnnotations"].(map[string]any)) == 0 {
		delete(scheduler, "podAnnotations")
	}
	values := map[string]any{
		"fullnameOverride": names.Control,
		"manager":          map[string]any{"enable": false},
		"mysql":            map[string]any{"enable": false},
		"redis":            map[string]any{"enable": false},
		"externalRedis":    map[string]any{"addrs": []any{names.KeyDB + ":6379"}},
		"externalManager": map[string]any{
			"host":     c.ManagerGRPCDomain,
			"grpcPort": grpcPort,
		},
		"scheduler":  scheduler,
		"seedClient": map[string]any{"enable": false},
		"client":     map[string]any{"enable": false},
	}
	applyMonitoring(values, c.Monitoring)
	return values
}

// withSchedulerCertTemplate supplies the scheduler component through Fleet
// template values so only the target seed's scheduler rolls on its SAN change.
// The client revision is global and can stay a literal in the same annotation
// map.
func withSchedulerCertTemplate(op *fleetv1alpha1.HelmOp, values map[string]any) {
	scheduler, ok := values["scheduler"].(map[string]any)
	if !ok {
		return
	}
	copyScheduler := make(map[string]any, len(scheduler))
	for key, value := range scheduler {
		copyScheduler[key] = value
	}
	annotations := map[string]any{AnnotationMTLSCertRevision: SchedulerCertRevisionTemplate}
	if revision, _ := scheduler["podAnnotations"].(map[string]any); revision != nil {
		for key, value := range revision {
			annotations[key] = value
		}
	}
	copyScheduler["podAnnotations"] = annotations
	raw, err := yaml.Marshal(copyScheduler)
	if err != nil {
		return
	}
	if op.Spec.Helm == nil {
		op.Spec.Helm = &fleetv1alpha1.HelmOptions{}
	}
	op.Spec.Helm.TemplateValues = map[string]string{"scheduler": string(raw)}
}

func withBundleDependency(op *fleetv1alpha1.HelmOp, name string) {
	op.Spec.DependsOn = append(op.Spec.DependsOn, fleetv1alpha1.BundleRef{Name: name})
}

func withBundleSelector(op *fleetv1alpha1.HelmOp, labels map[string]string) {
	op.Spec.DependsOn = append(op.Spec.DependsOn, fleetv1alpha1.BundleRef{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
	})
}

func seedPeerConfig(tlsEnabled bool) map[string]any {
	seedPeer := map[string]any{}
	if tlsEnabled {
		seedPeer["tls"] = tlsPaths()
	}
	return seedPeer
}
