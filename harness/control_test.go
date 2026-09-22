package harness

import "testing"

func TestRunControllerQueuesFIFOAndRejectsClosedRuns(t *testing.T) {
	controller := NewRunController()
	if err := controller.Open("run_1"); err != nil {
		t.Fatal(err)
	}
	first, ok := controller.EnqueueSteer("run_1", "", "first")
	if !ok || first.ID == "" {
		t.Fatalf("first steer = %#v accepted=%v", first, ok)
	}
	second, ok := controller.EnqueueSteer("run_1", "steer_2", "second")
	if !ok || second.ID != "steer_2" {
		t.Fatalf("second steer = %#v accepted=%v", second, ok)
	}
	queued := controller.DrainSteers("run_1")
	if len(queued) != 2 || queued[0].Message != "first" || queued[1].Message != "second" {
		t.Fatalf("queue = %#v", queued)
	}
	if len(controller.DrainSteers("run_1")) != 0 {
		t.Fatal("drain did not clear queue")
	}
	controller.Close("run_1")
	if _, ok := controller.EnqueueSteer("run_1", "", "late"); ok {
		t.Fatal("closed run accepted steer")
	}
}

func TestRunControllerShowsAndEditsThePendingQueue(t *testing.T) {
	controller := NewRunController()
	if err := controller.Open("run_1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := controller.EnqueueSteer("run_1", "steer_1", "first"); !ok {
		t.Fatal("enqueue first")
	}
	if _, ok := controller.EnqueueSteer("run_1", "steer_2", "second"); !ok {
		t.Fatal("enqueue second")
	}
	// Reading the queue does not deliver it: a console renders the pending rows
	// and the run must still receive them.
	pending := controller.PendingSteers("run_1")
	if len(pending) != 2 || pending[0].Message != "first" || pending[1].Message != "second" {
		t.Fatalf("pending = %#v", pending)
	}
	pending[0].Message = "mutated by the caller"
	if again := controller.PendingSteers("run_1"); again[0].Message != "first" {
		t.Fatalf("pending copy shared with the controller: %#v", again)
	}
	// An edit keeps the row's place and its identity, so the console's own
	// correlation of the row to the submission it made survives.
	if !controller.ReplaceSteer("run_1", "steer_1", "first, rewritten") {
		t.Fatal("replace a pending steer")
	}
	if got := controller.PendingSteers("run_1"); got[0].ID != "steer_1" || got[0].Message != "first, rewritten" || got[1].Message != "second" {
		t.Fatalf("after replace = %#v", got)
	}
	if controller.ReplaceSteer("run_1", "steer_9", "nothing") {
		t.Fatal("replaced an id that is not pending")
	}
	if controller.ReplaceSteer("run_1", "steer_1", "   ") {
		t.Fatal("accepted a blank message")
	}
	// Dropping removes only the named row, and the remaining order is unchanged.
	if !controller.RemoveSteer("run_1", "steer_1") {
		t.Fatal("remove a pending steer")
	}
	if got := controller.PendingSteers("run_1"); len(got) != 1 || got[0].ID != "steer_2" {
		t.Fatalf("after remove = %#v", got)
	}
	if controller.RemoveSteer("run_1", "steer_1") {
		t.Fatal("removed the same steer twice")
	}
	// What survives is what the boundary delivers, in order.
	drained := controller.DrainSteers("run_1")
	if len(drained) != 1 || drained[0].Message != "second" {
		t.Fatalf("drained = %#v", drained)
	}
	if got := controller.PendingSteers("run_1"); len(got) != 0 {
		t.Fatalf("a delivered queue is empty: %#v", got)
	}
	// A closed run has no queue to show or edit, and neither has an unknown one.
	if controller.ReplaceSteer("run_1", "steer_2", "late") || controller.RemoveSteer("run_1", "steer_2") {
		t.Fatal("a delivered queue accepted an edit")
	}
	controller.Close("run_1")
	if got := controller.PendingSteers("run_1"); len(got) != 0 {
		t.Fatalf("closed run still shows a queue: %#v", got)
	}
	if controller.ReplaceSteer("run_1", "steer_2", "late") || controller.RemoveSteer("run_1", "steer_2") {
		t.Fatal("closed run accepted a queue edit")
	}
	if got := controller.PendingSteers("run_missing"); len(got) != 0 {
		t.Fatalf("unknown run shows a queue: %#v", got)
	}
}
