package bootstrap

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const testLine = `curl -sfL "http://static.example.com/p2p-install.sh?cfg=abc" | bash  # d7y-cattle-operator:p2p-install`

func TestInjectionLine(t *testing.T) {
	got := InjectionLine("http://static.example.com/", "abc")
	if !strings.Contains(got, `http://static.example.com/p2p-install.sh?cfg=abc`) {
		t.Errorf("line = %q", got)
	}
	if !strings.Contains(got, Marker) {
		t.Errorf("line lacks marker: %q", got)
	}
}

func TestInjectPlainUserData(t *testing.T) {
	raw := "#cloud-config\nruncmd:\n  - echo hi\n"
	patched, changed, err := InjectUserData(raw, testLine)
	if err != nil {
		t.Fatalf("InjectUserData() error = %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if !strings.Contains(patched, Marker) || !strings.Contains(patched, "echo hi") {
		t.Errorf("patched userData = %q", patched)
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(patched), &doc); err != nil {
		t.Fatalf("patched YAML invalid: %v", err)
	}
	runcmd, ok := doc["runcmd"].([]any)
	if !ok || len(runcmd) != 2 {
		t.Fatalf("runcmd = %#v, want two entries", doc["runcmd"])
	}

	again, changed, err := InjectUserData(patched, testLine)
	if err != nil || changed || again != patched {
		t.Fatalf("repeat injection = changed %v, err %v", changed, err)
	}
}

func TestInjectBase64UserData(t *testing.T) {
	rawYAML := "#cloud-config\nruncmd:\n  - echo hi\n"
	raw := base64.StdEncoding.EncodeToString([]byte(rawYAML))
	patched, changed, err := InjectUserData(raw, testLine)
	if err != nil || !changed {
		t.Fatalf("InjectUserData() = changed %v, err %v", changed, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(patched)
	if err != nil {
		t.Fatalf("patched value is not base64: %v", err)
	}
	if !strings.Contains(string(decoded), Marker) {
		t.Errorf("decoded patched userData = %q", decoded)
	}
	_, changed, err = InjectUserData(patched, testLine)
	if err != nil || changed {
		t.Fatalf("repeat base64 injection = changed %v, err %v", changed, err)
	}
}

func TestInjectEmptyUserData(t *testing.T) {
	patched, changed, err := InjectUserData("", testLine)
	if err != nil || !changed {
		t.Fatalf("InjectUserData() = changed %v, err %v", changed, err)
	}
	if !strings.Contains(patched, "runcmd:") || !strings.Contains(patched, Marker) {
		t.Errorf("patched empty userData = %q", patched)
	}
}

func TestInjectCommentOnlyUserData(t *testing.T) {
	patched, changed, err := InjectUserData("#cloud-config\n", testLine)
	if err != nil || !changed {
		t.Fatalf("InjectUserData() = changed %v, err %v", changed, err)
	}
	if !strings.Contains(patched, "#cloud-config") || !strings.Contains(patched, Marker) {
		t.Errorf("patched comment-only userData = %q", patched)
	}
}

func TestInjectMalformedUserData(t *testing.T) {
	if _, _, err := InjectUserData("runcmd: not-a-sequence\n", testLine); err == nil {
		t.Fatal("InjectUserData() error = nil, want malformed runcmd error")
	}
}

func TestInjectAlreadyMarked(t *testing.T) {
	raw := "#cloud-config\nruncmd:\n  - " + testLine + "\n"
	patched, changed, err := InjectUserData(raw, testLine)
	if err != nil || changed || patched != raw {
		t.Fatalf("already marked = changed %v, err %v", changed, err)
	}
}

func TestInjectEmptyRuncmd(t *testing.T) {
	patched, changed, err := InjectUserData("#cloud-config\nruncmd:\n", testLine)
	if err != nil || !changed {
		t.Fatalf("InjectUserData() = changed %v, err %v", changed, err)
	}
	if !strings.Contains(patched, Marker) {
		t.Errorf("patched userData = %q", patched)
	}
}

func TestInjectPlainShellScript(t *testing.T) {
	raw := "#!/bin/bash\necho hello\n"
	patched, changed, err := InjectUserData(raw, testLine)
	if err != nil || !changed {
		t.Fatalf("InjectUserData() = changed %v, err %v", changed, err)
	}
	if !strings.HasPrefix(patched, raw) {
		t.Errorf("existing script was modified: %q", patched)
	}
	if !strings.Contains(patched, Marker) {
		t.Errorf("patched script = %q", patched)
	}
	if strings.Contains(patched, "runcmd:") {
		t.Errorf("shell script was converted to cloud-config: %q", patched)
	}

	again, changed, err := InjectUserData(patched, testLine)
	if err != nil || changed || again != patched {
		t.Fatalf("repeat injection = changed %v, err %v", changed, err)
	}
}

func TestInjectScriptEmpty(t *testing.T) {
	patched, changed, err := InjectScript("", testLine)
	if err != nil || !changed {
		t.Fatalf("InjectScript() = changed %v, err %v", changed, err)
	}
	if strings.Contains(patched, "#cloud-config") || strings.Contains(patched, "runcmd:") {
		t.Errorf("empty script became cloud-config: %q", patched)
	}
	if !strings.Contains(patched, Marker) {
		t.Errorf("patched empty script = %q", patched)
	}
}

func TestInjectScriptCloudConfig(t *testing.T) {
	patched, changed, err := InjectScript("#cloud-config\nruncmd:\n  - echo hi\n", testLine)
	if err != nil || !changed {
		t.Fatalf("InjectScript() = changed %v, err %v", changed, err)
	}
	if !strings.Contains(patched, "runcmd:") || !strings.Contains(patched, Marker) {
		t.Errorf("patched cloud-config = %q", patched)
	}
}
