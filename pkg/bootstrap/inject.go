package bootstrap

import (
	"encoding/base64"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Marker identifies an injected bootstrap command for idempotency.
const Marker = "# d7y-cattle-operator:p2p-install"

// InjectionLine returns the shell command inserted into cloud-init runcmd.
func InjectionLine(staticURL, payload string) string {
	base := strings.TrimRight(strings.TrimSpace(staticURL), "/")
	return fmt.Sprintf("curl -sfL %q | bash  %s", base+"/p2p-install.sh?cfg="+payload, Marker)
}

// InjectUserData appends line to runcmd in plain or base64-encoded cloud-init
// userData. It returns changed=false when the marker is already present.
func InjectUserData(raw, line string) (string, bool, error) {
	if strings.Contains(raw, Marker) {
		return raw, false, nil
	}

	decoded, encoded := decodeUserData(raw)
	if strings.Contains(decoded, Marker) {
		return raw, false, nil
	}

	patched, err := appendRunCmd(decoded, line)
	if err != nil {
		return "", false, err
	}
	if encoded {
		return base64.StdEncoding.EncodeToString([]byte(patched)), true, nil
	}
	return patched, true, nil
}

func decodeUserData(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, " \n\r\t") || len(trimmed)%4 != 0 {
		return raw, false
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || !looksLikeYAML(decoded) {
		return raw, false
	}
	return string(decoded), true
}

func looksLikeYAML(data []byte) bool {
	text := string(data)
	return strings.Contains(text, "\n") ||
		strings.Contains(text, ":") ||
		strings.Contains(text, "runcmd") ||
		strings.HasPrefix(strings.TrimSpace(text), "#cloud-config")
}

func appendRunCmd(raw, line string) (string, error) {
	var document yaml.Node
	if strings.TrimSpace(raw) != "" {
		if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
			return "", fmt.Errorf("bootstrap: invalid userData YAML: %w", err)
		}
	}
	if len(document.Content) == 0 {
		document = yaml.Node{
			Kind:        yaml.DocumentNode,
			HeadComment: "#cloud-config",
			Content:     []*yaml.Node{{Kind: yaml.MappingNode}},
		}
	}

	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", fmt.Errorf("bootstrap: userData root is not a YAML mapping")
	}

	runcmd := mapValue(root, "runcmd")
	if runcmd == nil {
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: "runcmd"}
		runcmd = &yaml.Node{Kind: yaml.SequenceNode}
		root.Content = append(root.Content, key, runcmd)
	} else if runcmd.Tag == "!!null" {
		runcmd.Kind = yaml.SequenceNode
		runcmd.Tag = ""
		runcmd.Value = ""
		runcmd.Content = nil
	}
	if runcmd.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("bootstrap: runcmd is not a YAML sequence")
	}

	runcmd.Content = append(runcmd.Content, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Style: yaml.SingleQuotedStyle,
		Value: line,
	})

	out, err := yaml.Marshal(&document)
	if err != nil {
		return "", fmt.Errorf("bootstrap: encode userData YAML: %w", err)
	}
	return string(out), nil
}

func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
