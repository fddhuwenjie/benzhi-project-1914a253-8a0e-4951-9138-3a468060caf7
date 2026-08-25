package casecore

import (
	"microclimate/internal/rules"
	"microclimate/internal/store"
	"testing"
)

func TestLifecycleAndConflict(t *testing.T) {
	s := New(store.New(""))
	c, err := s.Create("cab-1", rules.SensorSnapshot{Temperature: 30, Humidity: 70, DurationMinutes: 90}, "高", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Assign(c.ID, "keeper", 99); err != store.ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	c, err = s.Assign(c.ID, "keeper", c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.AddInspection(c.ID, "keeper", store.InspectionRecord{Observations: "ok", EvidenceRefs: []string{"sha256:test"}}, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Review(c.ID, "expert", "通过", "完整", "持续监测", c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Close(c.ID, c.Revision)
	if err != nil || c.Status != "已关闭" {
		t.Fatalf("close failed: %v", err)
	}
}
