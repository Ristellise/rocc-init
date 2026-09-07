package proc

import (
	"testing"
	"time"
)

// Regression test: a subcommand invocation (`rocc install uv` from a shell
// over ssh) never calls StartReaper — that happens in the PID-1 boot flow.
// Before the fix, runCmd's Wait blocked forever — nothing existed to
// deliver the exit code — and the runtime died with
// "fatal error: all goroutines are asleep - deadlock!" while the child
// process kept running on the inherited stdio.
func TestRunWithoutExplicitStartReaper(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- Run("sh", "-c", "exit 3")
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "exit status 3" {
			t.Fatalf("want exit status 3, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run deadlocked without an explicit StartReaper")
	}
}

func TestRunCaptureWithoutExplicitStartReaper(t *testing.T) {
	out, err := RunCapture("sh", "-c", "echo captured")
	if err != nil || out != "captured\n" {
		t.Fatalf("want captured, got %q (err %v)", out, err)
	}
}
