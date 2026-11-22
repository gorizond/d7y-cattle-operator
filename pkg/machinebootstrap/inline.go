package machinebootstrap

import (
	"fmt"
	"strings"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

// inlineInstallBlock renders the legacy v0.3.21-v0.3.23 inline block. It is
// retained for migration tests; production reconciles use shortInstallBlock.
func inlineInstallBlock(env string) string {
	return inlineInstallBlockFor(p2pInstallBody, env)
}

func inlineInstallBlockFor(rawBody, env string) string {
	body := strings.TrimSpace(rawBody)
	delimiter := "D7Y_P2P_EOF"
	for suffix := 1; strings.Contains(body, delimiter); suffix++ {
		delimiter = fmt.Sprintf("D7Y_P2P_EOF_%d", suffix)
	}
	return bootstrap.Marker + "\n" +
		"if command -v bash >/dev/null 2>&1; then\n" +
		"cat > /tmp/d7y-p2p-install.sh <<'" + delimiter + "'\n" +
		quoteEnv(env) + body + "\n" +
		delimiter + "\n" +
		"bash /tmp/d7y-p2p-install.sh || true\n" +
		"fi\n" +
		bootstrap.EndMarker + "\n"
}

// shortInstallSignature identifies the short static-delivery p2p block without
// matching the legacy inline script body.
const shortInstallSignature = `p2p-install.sh" | bash`

// shortInstallBlock returns the marked short p2p block. The env-file lines are
// exported to the piped bash with set -a/set +a; the static script itself is
// fetched without query parameters, so no bootstrap value appears in the URL.
func shortInstallBlock(staticURL, env string) string {
	base := strings.TrimRight(strings.TrimSpace(staticURL), "/")
	return bootstrap.Marker + "\n" +
		"set -a\n" +
		quoteEnv(env) +
		"set +a\n" +
		"curl -sfL \"" + base + "/p2p-install.sh\" | bash || true\n" +
		bootstrap.EndMarker + "\n"
}

func quoteEnv(env string) string {
	var builder strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		builder.WriteString(key)
		builder.WriteString("=")
		builder.WriteString(shellQuote(value))
		builder.WriteString("\n")
	}
	return builder.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// insertInlineBlock replaces an existing injected block, otherwise inserts a
// new block before the first do_install "$@" line, falling back to before the
// last exit 0 and then the end.
func insertInlineBlock(raw, block string) (string, error) {
	lines := strings.Split(raw, "\n")
	if start, end, ok, err := existingBlock(lines); err != nil {
		return "", err
	} else if ok {
		blockLines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
		out := make([]string, 0, len(lines)-(end-start)+len(blockLines))
		out = append(out, lines[:start]...)
		out = append(out, blockLines...)
		out = append(out, lines[end:]...)
		return strings.Join(out, "\n"), nil
	}
	at := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == `do_install "$@"` {
			at = i
			break
		}
	}
	if at < 0 {
		for i, line := range lines {
			if strings.TrimSpace(line) == "exit 0" {
				at = i
			}
		}
	}
	if at < 0 {
		if raw == "" {
			return block, nil
		}
		if strings.HasSuffix(raw, "\n") {
			return raw + block, nil
		}
		return raw + "\n" + block, nil
	}
	blockLines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(blockLines))
	out = append(out, lines[:at]...)
	out = append(out, blockLines...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), nil
}

// inlinePreinstallBlock wraps the Cluster annotation body in a fail-open
// subshell. The quoted heredoc keeps the body literal; the subshell and the
// namespaced rc variable keep the outer Rancher bootstrap script untouched.
func inlinePreinstallBlock(body string) string {
	body = strings.TrimSpace(body)
	delimiter := "D7Y_PREINSTALL_EOF"
	for suffix := 1; strings.Contains(body, delimiter); suffix++ {
		delimiter = fmt.Sprintf("D7Y_PREINSTALL_EOF_%d", suffix)
	}
	return bootstrap.PreinstallMarker + "\n" +
		"if command -v bash >/dev/null 2>&1; then\n" +
		"cat > /tmp/d7y-preinstall.sh <<'" + delimiter + "'\n" +
		body + "\n" +
		delimiter + "\n" +
		"(\n" +
		"  d7y_preinstall_rc=0\n" +
		"  bash /tmp/d7y-preinstall.sh >/tmp/d7y-preinstall.log 2>&1 || d7y_preinstall_rc=$?\n" +
		"  while IFS= read -r line || [ -n \"$line\" ]; do printf '%s\\n' \"[d7y-preinstall] $line\"; done < /tmp/d7y-preinstall.log\n" +
		"  echo \"[d7y-preinstall] exit=$d7y_preinstall_rc\"\n" +
		")\n" +
		"fi\n" +
		bootstrap.PreinstallEndMarker + "\n"
}

// removePreinstallBlock removes the injected pre-install block, if present.
func removePreinstallBlock(raw string) (string, error) {
	lines := strings.Split(raw, "\n")
	start, end, ok := existingMarkedBlock(lines, bootstrap.PreinstallMarker, bootstrap.PreinstallEndMarker)
	if !ok {
		return raw, nil
	}
	if strings.TrimSpace(lines[end-1]) != bootstrap.PreinstallEndMarker {
		return raw, fmt.Errorf("machinebootstrap: pre-install marker has an unrecognized block shape")
	}
	out := make([]string, 0, len(lines)-(end-start))
	out = append(out, lines[:start]...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}

// insertPreinstallBlock inserts the pre-install block immediately before the
// p2p marker, falling back to the same positions as the p2p block.
func insertPreinstallBlock(raw, block string) (string, error) {
	lines := strings.Split(raw, "\n")
	at := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == bootstrap.Marker {
			at = i
			break
		}
	}
	if at < 0 {
		for i, line := range lines {
			if strings.TrimSpace(line) == `do_install "$@"` {
				at = i
				break
			}
		}
	}
	if at < 0 {
		for i, line := range lines {
			if strings.TrimSpace(line) == "exit 0" {
				at = i
			}
		}
	}
	if at < 0 {
		if raw == "" {
			return block, nil
		}
		if strings.HasSuffix(raw, "\n") {
			return raw + block, nil
		}
		return raw + "\n" + block, nil
	}
	blockLines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(blockLines))
	out = append(out, lines[:at]...)
	out = append(out, blockLines...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n"), nil
}

// existingMarkedBlock returns the half-open line range of the first block
// delimited by marker and endMarker. A missing end marker extends the range to
// the end of the input; callers that need a stricter shape must verify
// lines[end-1].
func existingMarkedBlock(lines []string, marker, endMarker string) (int, int, bool) {
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == marker {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == endMarker {
			return start, i + 1, true
		}
	}
	return start, len(lines), true
}

// existingBlock returns the half-open line range of the previously injected
// block. Blocks written by older versions have no end marker; their shape is
// still recognized by the bash invocation followed by fi.
func existingBlock(lines []string) (int, int, bool, error) {
	start, end, ok := existingMarkedBlock(lines, bootstrap.Marker, bootstrap.EndMarker)
	if !ok {
		return 0, 0, false, nil
	}
	if end > start+1 && strings.TrimSpace(lines[end-1]) == bootstrap.EndMarker {
		return start, end, true, nil
	}
	bashLine := -1
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == `bash /tmp/d7y-p2p-install.sh || true` {
			bashLine = i
			continue
		}
		if bashLine >= 0 && trimmed == "fi" {
			return start, i + 1, true, nil
		}
	}
	return 0, 0, false, fmt.Errorf("machinebootstrap: existing p2p marker has an unrecognized block shape")
}
