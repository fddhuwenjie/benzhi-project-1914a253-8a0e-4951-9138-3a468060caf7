package cancelledrequestpersist

import (
	"bytes"
	"context"
	"encoding/json"
	"microclimate/internal/casecore"
	"microclimate/internal/httpapi"
	"microclimate/internal/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCancelledRequestDoesNotPersist(t *testing.T) {
	st := store.New("")
	core := casecore.New(st)
	h := httpapi.New(core).Handler()
	payload := map[string]any{
		"cabinet_id":           "cancel-cabinet",
		"temperature":          20,
		"humidity":             50,
		"duration_minutes":     15,
		"artifact_sensitivity": "普通",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/microclimate-events", bytes.NewReader(body)).WithContext(ctx)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if got := len(coreCases(core)); got != 0 {
		t.Fatalf("cancelled request persisted %d case(s), status=%d", got, resp.Code)
	}
}

func coreCases(core *casecore.Service) []*store.MicroclimateCase {
	cases, _, _ := core.List("", "", "")
	return cases
}
