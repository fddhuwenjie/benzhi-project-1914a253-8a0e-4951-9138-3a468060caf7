package offsetreplay

import (
    "bytes"
    "encoding/json"
    "microclimate/internal/casecore"
    "microclimate/internal/httpapi"
    "microclimate/internal/store"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"
)

func TestOffsetDuplicateIsReplay(t *testing.T) {
    h := httpapi.New(casecore.New(store.New(""))).Handler()
    collected := time.Now().UTC().Add(-time.Minute).In(time.FixedZone("CST", 8*60*60)).Format(time.RFC3339)
    body := []byte(`{"cabinet_id":"cab-offset","temperature":20,"humidity":50,"duration_minutes":5,"artifact_sensitivity":"普通","collected_at":"` + collected + `"}`)
    req := httptest.NewRequest(http.MethodPost, "/v1/microclimate-events", bytes.NewReader(body))
    w := httptest.NewRecorder(); h.ServeHTTP(w, req)
    if w.Code != http.StatusCreated { t.Fatalf("first status=%d body=%s", w.Code, w.Body.String()) }
    var first store.MicroclimateCase
    if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil { t.Fatal(err) }
    req = httptest.NewRequest(http.MethodPost, "/v1/microclimate-events", bytes.NewReader(body))
    w = httptest.NewRecorder(); h.ServeHTTP(w, req)
    if w.Code != http.StatusOK { t.Fatalf("expected replay status 200, got %d body=%s case=%s", w.Code, w.Body.String(), first.ID) }
}
