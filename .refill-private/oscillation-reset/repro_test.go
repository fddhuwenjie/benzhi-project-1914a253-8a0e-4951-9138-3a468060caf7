package oscillationreset

import (
    "microclimate/internal/casecore"
    "microclimate/internal/rules"
    "microclimate/internal/store"
    "testing"
    "time"
)

func TestOscillationResetsStabilityWindow(t *testing.T) {
    svc := casecore.New(store.New(""))
    c, err := svc.Create("cab-osc", rules.SensorSnapshot{Temperature: 37, Humidity: 70, DurationMinutes: 1}, "普通", "osc-key")
    if err != nil { t.Fatal(err) }
    c, err = svc.Assign(c.ID, "keeper", c.Revision)
    if err != nil { t.Fatal(err) }
    readings := []rules.SensorSnapshot{
        {Temperature: 22, Humidity: 50, DurationMinutes: 1},
        {Temperature: 42, Humidity: 70, DurationMinutes: 2},
        {Temperature: 22, Humidity: 50, DurationMinutes: 3},
    }
    base := time.Now().UTC()
    for i, snap := range readings {
        at := base.Add(time.Duration(i-3) * time.Minute).Format(time.RFC3339Nano)
        snap.CollectedAt = at
        c, err = svc.AddInspection(c.ID, "keeper", store.InspectionRecord{
            Readings: snap, ObservedAt: at, Observations: "现场复测", MitigationActions: "调控动作-" + string(rune('a'+i)),
            EvidenceRefs: []string{"sha256:0000000000000000000000000000000000000000000000000000000000000000"},
        }, c.Revision)
        if err != nil { t.Fatalf("inspection %d: %v", i, err) }
    }
    if c.OscillationAlerts == 0 { t.Fatalf("expected oscillation alert") }
    if c.Stability.Count != 0 {
        t.Fatalf("TestOscillationResetsStabilityWindow: oscillation left stability count=%d", c.Stability.Count)
    }
}
