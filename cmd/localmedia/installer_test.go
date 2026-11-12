package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The installer stops helper programs that are still running by asking
// Windows for them (StopHelpersIn in packaging/installer.iss). Windows only
// lets a program be stopped through a description of it that carries what
// it is identified by. A question that picks out a few fields gets
// descriptions without that, and stopping through one of those fails. The
// installer cannot be run here (GitHub's Windows machine builds and runs
// it), so this at least keeps the question whole.
func TestTheInstallerAsksWindowsForWholeDescriptions(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "installer.iss"))
	if err != nil {
		t.Fatal(err)
	}
	questions := regexp.MustCompile(`(?i)SELECT\s+(.+?)\s+FROM\s+Win32_Process`).FindAllStringSubmatch(string(script), -1)
	if len(questions) == 0 {
		t.Fatal("the installer no longer asks Windows which helper programs are running")
	}
	for _, q := range questions {
		if strings.TrimSpace(q[1]) != "*" {
			t.Errorf("the installer asks Windows for %q only (%q); a program cannot be stopped through such a description, ask for * instead", q[1], q[0])
		}
	}
}
