package store

import (
	"microclimate/internal/rules"
	"path/filepath"
	"testing"
)

func TestSnapshotRestoreAndConditionalWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "snapshot.json")
	s := New(path)
	c := &MicroclimateCase{ID: "one", CaseNo: "one", CabinetID: "cab", SensorSnapshot: rules.SensorSnapshot{Temperature: 20, Humidity: 50}, Status: "待分派", Revision: 1}
	if err := s.Create(c); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update("one", 0, func(*MicroclimateCase) error { return nil }); err != ErrConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	s2 := New(path)
	got, err := s2.Find("one")
	if err != nil || got.CabinetID != "cab" {
		t.Fatalf("restore failed: %v", err)
	}
}
