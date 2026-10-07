package runtime

import "testing"

// TestAlarmRegistryRejectsDuplicateAndStaleUnregister covers the overlap
// between startup recovery and a delayed alarm-created control event.
func TestAlarmRegistryRejectsDuplicateAndStaleUnregister(t *testing.T) {
	registry := &alarmRegistryType{}
	first := &alarmRegistration{cancel: func() {}}
	second := &alarmRegistration{cancel: func() {}}
	if !registry.register(7, first) || registry.register(7, second) {
		t.Fatal("same scheduled event was armed twice")
	}
	registry.cancelAll()
	if !registry.register(7, second) {
		t.Fatal("alarm could not be rearmed after shutdown")
	}
	registry.unregister(7, first)
	if registry.alarms[7] != second {
		t.Fatal("old waiter removed a newly recovered alarm")
	}
	registry.unregister(7, second)
	if _, exists := registry.alarms[7]; exists {
		t.Fatal("exited waiter remained registered")
	}
}
