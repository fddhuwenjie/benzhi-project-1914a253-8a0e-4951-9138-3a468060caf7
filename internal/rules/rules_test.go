package rules

import "testing"

func TestAssessLevels(t *testing.T) {
	if got := Assess(SensorSnapshot{Temperature: 22, Humidity: 50, DurationMinutes: 5}, "普通").Level; got != Low {
		t.Fatalf("expected low, got %s", got)
	}
	if got := Assess(SensorSnapshot{Temperature: 30, Humidity: 70, DurationMinutes: 240}, "高").Level; got != Emergency {
		t.Fatalf("expected emergency, got %s", got)
	}
}

func TestChecklistSensitive(t *testing.T) {
	items := Checklist(High, "高")
	if len(items) < 5 {
		t.Fatalf("expected detailed checklist")
	}
}
