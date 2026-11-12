//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Windows itself is asked whether the request is on its list of what
// keeps the PC awake while it is held, and off it once it is let go.
// "powercfg /requests" answers only an administrator: GitHub's Windows
// machine is one, an owner's PC usually is not, so elsewhere the test is
// skipped unless LM_REQUIRE_POWERCFG says it must run.
func TestWindowsListsTheRequestWhileItIsHeld(t *testing.T) {
	b := make([]byte, 6)
	rand.Read(b)
	reason := "Local Media test " + hex.EncodeToString(b)
	listed := func() (string, bool) {
		out, err := exec.Command("powercfg", "/requests").CombinedOutput()
		if err != nil {
			if os.Getenv("LM_REQUIRE_POWERCFG") != "" {
				t.Fatalf("powercfg /requests could not be asked, and LM_REQUIRE_POWERCFG says it must: %v %s", err, out)
			}
			t.Skipf("powercfg /requests answers only an administrator: %v", err)
		}
		return string(out), strings.Contains(string(out), reason)
	}
	req, err := newPowerRequest(reason)
	if err != nil {
		t.Fatalf("Windows would not make a request: %v", err)
	}
	defer req.close()
	if out, yes := listed(); yes {
		t.Fatalf("the request is listed before it is held:\n%s", out)
	}
	if _, err := req.hold(); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := req.hold(); err != nil { // a second time changes nothing
		t.Fatalf("hold again: %v", err)
	}
	out, yes := listed()
	if !yes {
		t.Fatalf("the request is held, but Windows does not list it:\n%s", out)
	}
	// It is listed as keeping the system awake, and not as keeping the
	// screen on. The list has a heading for each kind of request ("SYSTEM:")
	// with the requests of that kind under it.
	under := map[string]string{}
	heading := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ":") && line == strings.ToUpper(line) && !strings.Contains(line, " ") {
			heading = strings.TrimSuffix(line, ":")
			continue
		}
		under[heading] += line + "\n"
	}
	if _, ok := under["SYSTEM"]; !ok {
		t.Fatalf("the list does not have the headings expected:\n%s", out)
	}
	if !strings.Contains(under["SYSTEM"], reason) {
		t.Errorf("the request is not under SYSTEM:\n%s", out)
	}
	if list, ok := under["EXECUTION"]; ok && !strings.Contains(list, reason) {
		t.Errorf("the request is not under EXECUTION:\n%s", out)
	}
	for _, kind := range []string{"DISPLAY", "AWAYMODE"} {
		if strings.Contains(under[kind], reason) {
			t.Errorf("the request is listed under %s, which it must not be:\n%s", kind, out)
		}
	}
	req.release()
	req.release() // letting go twice is harmless
	if out, yes := listed(); yes {
		t.Fatalf("the request was let go, but Windows still lists it:\n%s", out)
	}
	// One clear undoes it however often it was set: held again and let go
	// once, it is gone.
	req.hold()
	req.release()
	if out, yes := listed(); yes {
		t.Fatalf("held and let go a second time, Windows still lists it:\n%s", out)
	}
}
