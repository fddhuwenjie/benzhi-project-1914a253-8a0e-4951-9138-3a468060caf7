package httpapi

import (
	"bytes"
	"encoding/json"
	"microclimate/internal/casecore"
	"microclimate/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndGet(t *testing.T) {
	h := New(casecore.New(store.New(""))).Handler()
	r := httptest.NewRequest("POST", "/v1/microclimate-events", bytes.NewBufferString(`{"cabinet_id":"c1","temperature":20,"humidity":50,"duration_minutes":1,"artifact_sensitivity":"普通"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	var c store.MicroclimateCase
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/cases/"+c.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("get status %d", w.Code)
	}
}
