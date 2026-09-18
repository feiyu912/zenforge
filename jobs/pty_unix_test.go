//go:build unix

package jobs

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

var childPIDPattern = regexp.MustCompile(`child=(\d+)`)

// processAlive reports whether a pid still exists. Signal 0 performs the
// permission and existence check without delivering anything.
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func TestKillingAPTYSessionStopsItsProcessGroup(t *testing.T) {
	manager := New(Config{DefaultTimeout: 30 * time.Second, DrainGrace: 2 * time.Second})
	defer manager.Close()
	// The session forks a long-running child that ignores SIGHUP, which is
	// what a terminal sends its foreground group when the session leader
	// exits; only a signal to the whole group reaches it, and until it is
	// gone it holds the terminal open.
	job, err := manager.Start(context.Background(), Spec{Command: "trap '' HUP; sleep 30 & echo child=$!; wait", PTY: true})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	seen := waitForOutput(t, manager, job.ID, "child=")
	match := childPIDPattern.FindStringSubmatch(seen)
	if match == nil {
		t.Fatalf("the session never reported its child: %q", seen)
	}
	childPID, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("child pid %q: %v", match[1], err)
	}
	if !processAlive(childPID) {
		t.Fatalf("child %d was not running before the kill", childPID)
	}
	if err := manager.Kill(job.ID, "test"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}
	final := waitFor(t, manager, job.ID)
	if final.Status != StatusKilled {
		t.Fatalf("killed session status = %s (error %q)", final.Status, final.Error)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if processAlive(childPID) {
		t.Fatalf("session child %d survived the kill", childPID)
	}
}
