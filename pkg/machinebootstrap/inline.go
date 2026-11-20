package machinebootstrap

import (
	"fmt"
	"strings"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

// inlineInstallBlock wraps the env-file assignments and the embedded script in
// a POSIX heredoc, then executes the literal body under bash. Marker is the
// first line for idempotency.
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
		"fi\n"
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

// insertInlineBlock inserts block before the first do_install "$@" line,
// falling back to before the last exit 0 and then the end.
func insertInlineBlock(raw, block string) string {
	if strings.Contains(raw, bootstrap.Marker) {
		return raw
	}
	lines := strings.Split(raw, "\n")
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
			return block
		}
		if strings.HasSuffix(raw, "\n") {
			return raw + block
		}
		return raw + "\n" + block
	}
	blockLines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	out := make([]string, 0, len(lines)+len(blockLines))
	out = append(out, lines[:at]...)
	out = append(out, blockLines...)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
}
