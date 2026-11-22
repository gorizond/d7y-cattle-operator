package machinebootstrap

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/gorizond/d7y-cattle-operator/pkg/bootstrap"
)

func TestInlinePreinstallBlockFormat(t *testing.T) {
	block := inlinePreinstallBlock("echo hi")
	if !strings.HasPrefix(block, bootstrap.PreinstallMarker+"\n") {
		t.Fatalf("block does not start with the pre-install marker: %q", block)
	}
	if !strings.HasSuffix(block, bootstrap.PreinstallEndMarker+"\n") {
		t.Fatalf("block does not end with the pre-install end marker: %q", block)
	}
	for _, want := range []string{
		"if command -v bash >/dev/null 2>&1; then\n",
		"cat > /tmp/d7y-preinstall.sh <<'D7Y_PREINSTALL_EOF'\n",
		"\necho hi\nD7Y_PREINSTALL_EOF\n",
		"(\n  d7y_preinstall_rc=0\n",
		"bash /tmp/d7y-preinstall.sh >/tmp/d7y-preinstall.log 2>&1 || d7y_preinstall_rc=$?\n",
		"while IFS= read -r line || [ -n \"$line\" ]; do printf '%s\\n' \"[d7y-preinstall] $line\"; done < /tmp/d7y-preinstall.log\n",
		"echo \"[d7y-preinstall] exit=$d7y_preinstall_rc\"\n)\nfi\n",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("block missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "\nrc=0\n") {
		t.Fatalf("wrapper uses an unnamespaced rc variable that can clobber the outer script: %q", block)
	}
}

func TestInlinePreinstallBlockDelimiterCollision(t *testing.T) {
	block := inlinePreinstallBlock("echo D7Y_PREINSTALL_EOF")
	if !strings.Contains(block, "cat > /tmp/d7y-preinstall.sh <<'D7Y_PREINSTALL_EOF_1'") {
		t.Fatalf("block did not choose a suffixed delimiter: %q", block)
	}
	if !strings.Contains(block, "\nD7Y_PREINSTALL_EOF_1\n(") {
		t.Fatalf("block did not close the heredoc with the suffixed delimiter: %q", block)
	}
}

func TestInsertPreinstallBlockBeforeP2P(t *testing.T) {
	p2p := inlineInstallBlockFor("echo p2p-ok\n", "STATIC_URL=http://static.example.com\n")
	raw := "#!/bin/bash\n" + p2p + "do_install \"$@\"\nexit 0\n"
	got, err := insertPreinstallBlock(raw, inlinePreinstallBlock("echo pre-ok"))
	if err != nil {
		t.Fatalf("insertPreinstallBlock() error = %v", err)
	}
	if count := strings.Count(got, bootstrap.PreinstallMarker+"\n"); count != 1 {
		t.Fatalf("pre-install marker count = %d, want 1: %q", count, got)
	}
	preAt := strings.Index(got, bootstrap.PreinstallMarker)
	p2pAt := strings.Index(got, bootstrap.Marker)
	if preAt < 0 || p2pAt < 0 || preAt > p2pAt {
		t.Fatalf("pre-install block is not before the p2p block: %q", got)
	}
}

func TestInsertPreinstallBlockFallsBackToDoInstall(t *testing.T) {
	raw := "#!/bin/bash\necho start\ndo_install \"$@\"\nexit 0\n"
	got, err := insertPreinstallBlock(raw, inlinePreinstallBlock("echo pre"))
	if err != nil {
		t.Fatalf("insertPreinstallBlock() error = %v", err)
	}
	preAt := strings.Index(got, bootstrap.PreinstallMarker)
	doInstallAt := strings.Index(got, `do_install "$@"`)
	if preAt < 0 || doInstallAt < 0 || preAt > doInstallAt {
		t.Fatalf("pre-install block is not before do_install: %q", got)
	}
}

func TestRemovePreinstallBlock(t *testing.T) {
	block := inlinePreinstallBlock("echo pre")
	raw := "#!/bin/bash\n" + block + "do_install \"$@\"\n"
	got, err := removePreinstallBlock(raw)
	if err != nil {
		t.Fatalf("removePreinstallBlock() error = %v", err)
	}
	if got != "#!/bin/bash\ndo_install \"$@\"\n" {
		t.Fatalf("removePreinstallBlock() = %q", got)
	}
	same, err := removePreinstallBlock("#!/bin/bash\n")
	if err != nil || same != "#!/bin/bash\n" {
		t.Fatalf("removePreinstallBlock(absent) = %q, %v", same, err)
	}
}

func TestRemovePreinstallBlockRejectsMissingEndMarker(t *testing.T) {
	raw := "#!/bin/bash\n" + bootstrap.PreinstallMarker + "\necho pre\ndo_install \"$@\"\n"
	got, err := removePreinstallBlock(raw)
	if err == nil {
		t.Fatal("removePreinstallBlock() error = nil, want a malformed block error")
	}
	if got != raw {
		t.Fatalf("removePreinstallBlock() modified the input on error: %q", got)
	}
	if !strings.Contains(err.Error(), "unrecognized block shape") {
		t.Fatalf("removePreinstallBlock() error = %v", err)
	}
}

func requirePreinstallShell(t *testing.T) {
	t.Helper()
	for _, name := range []string{"sh", "bash"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not available", name)
		}
	}
}

func runPreinstallShell(t *testing.T, script string) string {
	t.Helper()
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh rejected the script: %v\n%s", err, out)
	}
	return string(out)
}

func TestPreinstallBlockFailsOpen(t *testing.T) {
	requirePreinstallShell(t)
	out := runPreinstallShell(t, "#!/bin/sh\n"+inlinePreinstallBlock("false")+"echo next-ok\n")
	if !strings.Contains(out, "[d7y-preinstall] exit=1") {
		t.Fatalf("output does not report the failed command: %q", out)
	}
	if !strings.Contains(out, "next-ok") {
		t.Fatalf("bootstrap did not continue after the failed command: %q", out)
	}
}

func TestPreinstallBlockPrefixesOutput(t *testing.T) {
	requirePreinstallShell(t)
	out := runPreinstallShell(t, "#!/bin/sh\n"+inlinePreinstallBlock("echo hi"))
	if !strings.Contains(out, "[d7y-preinstall] hi") {
		t.Fatalf("output does not carry the pre-install prefix: %q", out)
	}
}

func TestPreinstallBlockPreservesBackslashesInLog(t *testing.T) {
	requirePreinstallShell(t)
	out := runPreinstallShell(t, "#!/bin/sh\n"+inlinePreinstallBlock(`printf 'a\\b\n'`))
	if !strings.Contains(out, `[d7y-preinstall] a\b`) {
		t.Fatalf("log prefix mangled a backslash in the command output: %q", out)
	}
}

func TestPreinstallBlockKeepsLastLineWithoutNewline(t *testing.T) {
	requirePreinstallShell(t)
	out := runPreinstallShell(t, "#!/bin/sh\n"+inlinePreinstallBlock("printf no-newline"))
	if !strings.Contains(out, "[d7y-preinstall] no-newline") {
		t.Fatalf("output lost the final line without a newline: %q", out)
	}
}

func TestInjectedBootstrapValueParsesUnderPOSIXShell(t *testing.T) {
	requirePreinstallShell(t)
	value := "#!/bin/sh\n" +
		inlinePreinstallBlock("echo pre-ok") +
		inlineInstallBlockFor("echo p2p-ok\n", "STATIC_URL=http://static.example.com\n") +
		"do_install \"$@\"\nexit 0\n"
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -n rejected the injected value: %v\n%s", err, out)
	}
}
