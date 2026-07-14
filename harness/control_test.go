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
