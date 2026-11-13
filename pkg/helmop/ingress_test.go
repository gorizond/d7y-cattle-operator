package helmop

import "testing"

func TestManagerGRPCIngress(t *testing.T) {
	ingress := ManagerGRPCIngress(testCluster())

	if ingress.Name != "c1-manager-grpc" || ingress.Namespace != "fleet-default" {
		t.Errorf("name/namespace = %s/%s", ingress.Name, ingress.Namespace)
	}
	if len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != "grpc.example.com" {
		t.Fatalf("rules = %+v", ingress.Spec.Rules)
	}
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Backend.Service == nil || path.Backend.Service.Name != "c1-manager" ||
		path.Backend.Service.Port.Number != ManagerGRPCPort {
		t.Errorf("backend = %+v", path.Backend.Service)
	}
	if got := ingress.Annotations["nginx.ingress.kubernetes.io/backend-protocol"]; got != GRPCBackendProtocol {
		t.Errorf("backend-protocol = %q", got)
	}
	if got := ingress.Annotations["nginx.ingress.kubernetes.io/auth-tls-verify-client"]; got != "on" {
		t.Errorf("auth-tls-verify-client = %q", got)
	}
	if got := ingress.Annotations["nginx.ingress.kubernetes.io/auth-tls-secret"]; got != "fleet-default/dragonfly-p2p-mtls" {
		t.Errorf("auth-tls-secret = %q", got)
	}
}

func TestManagerRESTIngress(t *testing.T) {
	ingress := ManagerRESTIngress(testCluster(), "api.example.com")

	if ingress.Name != "c1-manager-api" {
		t.Errorf("name = %q", ingress.Name)
	}
	if len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != "api.example.com" {
		t.Fatalf("rules = %+v", ingress.Spec.Rules)
	}
	path := ingress.Spec.Rules[0].HTTP.Paths[0]
	if path.Backend.Service == nil || path.Backend.Service.Name != "c1-manager" ||
		path.Backend.Service.Port.Number != ManagerRESTPort {
		t.Errorf("backend = %+v", path.Backend.Service)
	}
	if got := ingress.Annotations["nginx.ingress.kubernetes.io/backend-protocol"]; got != HTTPSBackendProtocol {
		t.Errorf("backend-protocol = %q", got)
	}
}
