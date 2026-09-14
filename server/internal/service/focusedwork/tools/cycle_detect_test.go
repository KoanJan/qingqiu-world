package tools

import (
	"testing"
)

// TestCycleDetect_NoCycle verifies that varying calls do not trigger detection.
func TestCycleDetect_NoCycle(t *testing.T) {
	d := &CycleDetector{}

	args1 := map[string]interface{}{"command": "ls"}
	args2 := map[string]interface{}{"command": "pwd"}

	cs := d.CycleDetect(args1, "output-a")
	if cs != NoCycleDetected {
		t.Fatalf("first call should not detect cycle, got %+v", cs)
	}

	cs = d.CycleDetect(args2, "output-a")
	if cs != NoCycleDetected {
		t.Fatalf("different args should not detect cycle, got %+v", cs)
	}
}

// TestCycleDetect_WarnThreshold verifies that the warning fires exactly at
// warnThreshold (3) consecutive identical (args, result) pairs.
func TestCycleDetect_WarnThreshold(t *testing.T) {
	d := &CycleDetector{}

	args := map[string]interface{}{"command": "echo hello"}
	result := "hello"

	// Calls 1-3: count=0,1,2 — no detection
	for i := 0; i < 3; i++ {
		cs := d.CycleDetect(args, result)
		if cs != NoCycleDetected {
			t.Fatalf("call %d: expected no detection, got %+v", i+1, cs)
		}
	}

	// Call 4: count=3 → warn
	cs := d.CycleDetect(args, result)
	if cs.Warning == "" {
		t.Fatal("call 4: expected warning at count=3")
	}
	if cs.Blocked {
		t.Fatal("call 4: should not be blocked at count=3")
	}

	// Call 5: count=4 → still warn
	cs = d.CycleDetect(args, result)
	if cs.Warning == "" {
		t.Fatal("call 5: expected warning at count=4")
	}
	if cs.Blocked {
		t.Fatal("call 5: should not be blocked at count=4")
	}
}

// TestCycleDetect_BlockThreshold verifies the full warn→block transition.
// First call initializes lastSignature (count=0); block fires when count
// reaches blockThreshold (8), which requires 9 total calls.
func TestCycleDetect_BlockThreshold(t *testing.T) {
	d := &CycleDetector{}

	args := map[string]interface{}{"command": "npm run dev"}
	result := `{"exit_code":1,"stderr":"Error: Cannot find module"}`

	// Calls 1-3: count=0,1,2 — no detection
	for i := 0; i < 3; i++ {
		cs := d.CycleDetect(args, result)
		if cs != NoCycleDetected {
			t.Fatalf("call %d: expected no detection, got %+v", i+1, cs)
		}
	}

	// Calls 4-8: count=3→7 — warnings
	for i := 4; i <= 8; i++ {
		cs := d.CycleDetect(args, result)
		if cs.Warning == "" {
			t.Fatalf("call %d: expected warning, got %+v", i, cs)
		}
		if cs.Blocked {
			t.Fatalf("call %d: should not be blocked before count=8", i)
		}
	}

	// Call 9: count=8 → block
	cs := d.CycleDetect(args, result)
	if !cs.Blocked {
		t.Fatal("call 9: expected block at count=8")
	}
	if cs.Reason == "" {
		t.Fatal("call 9: block reason should not be empty")
	}
}

// TestCycleDetect_BlockIsPersistent verifies that once blocked, the detector
// always returns blocked regardless of subsequent input.
func TestCycleDetect_BlockIsPersistent(t *testing.T) {
	d := &CycleDetector{}

	args := map[string]interface{}{"command": "ls"}
	result := "file1 file2"

	// Trigger block: first call initializes (count=0), then 8 more to reach count=8
	for i := 0; i < 9; i++ {
		d.CycleDetect(args, result)
	}

	// Blocked — any call should return blocked
	cs := d.CycleDetect(map[string]interface{}{"command": "pwd"}, "different result")
	if !cs.Blocked {
		t.Fatal("after block, different args should still return blocked")
	}

	cs = d.CycleDetect(args, result)
	if !cs.Blocked {
		t.Fatal("after block, same args should still return blocked")
	}
}

// TestCycleDetect_NilArgs verifies that nil args don't panic.
func TestCycleDetect_NilArgs(t *testing.T) {
	d := &CycleDetector{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("CycleDetect panicked on nil args: %v", r)
		}
	}()

	d.CycleDetect(nil, "result")
}

// TestCycleDetect_ArgsOrderInvariant verifies that the same args with different
// map key order produce the same signature (json.Marshal sorts keys).
func TestCycleDetect_ArgsOrderInvariant(t *testing.T) {
	d := &CycleDetector{}

	args1 := map[string]interface{}{"a": "1", "b": "2"}
	args2 := map[string]interface{}{"b": "2", "a": "1"}
	result := "output"

	// Calls 1-3: count=0,1,2 — no detection
	d.CycleDetect(args1, result)
	d.CycleDetect(args2, result) // same content, different key order → count=1
	cs := d.CycleDetect(args1, result)
	if cs.Warning != "" {
		t.Fatalf("count=2 should not warn, got %+v", cs)
	}

	// Call 4: count=3 → warning
	cs = d.CycleDetect(args2, result)
	if cs.Warning == "" {
		t.Fatal("count=3 should warn — args key order should not matter")
	}
}
