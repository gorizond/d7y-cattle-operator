package helmop

import (
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	GRPCBackendProtocol  = "GRPCS"
	HTTPSBackendProtocol = "HTTPS"
)

// ManagerGRPCIngress exposes the manager gRPC endpoint with client mTLS
// terminated at ingress-nginx.
func ManagerGRPCIngress(c Cluster) *networkingv1.Ingress {
	annotations := map[string]string{
		"nginx.ingress.kubernetes.io/backend-protocol":       GRPCBackendProtocol,
		"nginx.ingress.kubernetes.io/auth-tls-verify-client": "on",
		"nginx.ingress.kubernetes.io/auth-tls-secret":        c.Namespace + "/" + c.MTLSSecretName,
		"nginx.ingress.kubernetes.io/auth-tls-verify-depth":  "1",
	}
	return managerIngress(c, ManagerGRPCIngressName(c.Name), c.ManagerGRPCDomain, ManagerGRPCPort, annotations)
}

// ManagerRESTIngress exposes the manager REST API over HTTPS.
func ManagerRESTIngress(c Cluster, host string) *networkingv1.Ingress {
	annotations := map[string]string{
		"nginx.ingress.kubernetes.io/backend-protocol": HTTPSBackendProtocol,
	}
	return managerIngress(c, ManagerRESTIngressName(c.Name), host, ManagerRESTPort, annotations)
}

// ManagerGRPCIngressName returns the Ingress name for the manager gRPC endpoint.
func ManagerGRPCIngressName(cluster string) string { return objectName(cluster, "manager-grpc") }

// ManagerRESTIngressName returns the Ingress name for the manager REST API.
func ManagerRESTIngressName(cluster string) string { return objectName(cluster, "manager-api") }

func managerIngress(c Cluster, name, host string, port int32, annotations map[string]string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "Ingress"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   c.Namespace,
			Labels:      c.Labels,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: ManagerServiceName(c.Name),
											Port: networkingv1.ServiceBackendPort{Number: port},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
