package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"microclimate/internal/casecore"
	"microclimate/internal/evidence"
	"microclimate/internal/rules"
	"microclimate/internal/store"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type API struct {
	core *casecore.Service
	mux  *http.ServeMux
}

func New(core *casecore.Service) *API {
	a := &API{core: core, mux: http.NewServeMux()}
	a.routes()
	return a
}
func (a *API) Handler() http.Handler { return a.mux }
func (a *API) routes() {
	a.mux.HandleFunc("/v1/microclimate-events", a.CreateEvent)
	a.mux.HandleFunc("/v1/cases", a.CaseRouter)
	a.mux.HandleFunc("/v1/cases/", a.CaseRouter)
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) CreateEvent(w http.ResponseWriter, r *http.Request)           { a.createEvent(w, r) }
func (a *API) CaseRouter(w http.ResponseWriter, r *http.Request)            { a.caseRouter(w, r) }
func (a *API) AssignCase(w http.ResponseWriter, r *http.Request, id string) { a.assign(w, r, id) }
func (a *API) RecordInspection(w http.ResponseWriter, r *http.Request, id string) {
	a.inspect(w, r, id)
}
func (a *API) ReviewCase(w http.ResponseWriter, r *http.Request, id string) { a.review(w, r, id) }
func (a *API) CloseCase(w http.ResponseWriter, r *http.Request, id string)  { a.close(w, r, id) }
func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}

type eventReq struct {
	CabinetID           string   `json:"cabinet_id"`
	Temperature         *float64 `json:"temperature"`
	Humidity            *float64 `json:"humidity"`
	DurationMinutes     *int     `json:"duration_minutes"`
	ArtifactSensitivity string   `json:"artifact_sensitivity"`
	IdempotencyKey      string   `json:"idempotency_key"`
	CollectedAt         string   `json:"collected_at"`
	TemperatureUnit     string   `json:"temperature_unit"`
	HumidityUnit        string   `json:"humidity_unit"`
	RuleVersion         string   `json:"rule_version"`
	SensorSerial        string   `json:"sensor_serial"`
	CalibrationAt       string   `json:"calibration_at"`
}

func (a *API) createEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		write(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var raw json.RawMessage
	if err := decode(r, &raw); err != nil {
		write(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if len(raw) > 0 && raw[0] == '[' {
		var batchRaw []json.RawMessage
		if err := json.Unmarshal(raw, &batchRaw); err != nil || len(batchRaw) == 0 || len(batchRaw) > 100 {
			write(w, 400, map[string]string{"error": "batch must contain 1..100 events"})
			return
		}
		items := make([]any, 0, len(batchRaw))
		for _, rawItem := range batchRaw {
			var q eventReq
			dec := json.NewDecoder(bytes.NewReader(rawItem))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&q); err != nil {
				items = append(items, map[string]any{"status": "validation_failed", "error": err.Error(), "field_errors": map[string]string{"body": err.Error()}})
				continue
			}
			c, status, err := a.processEvent(r.Context(), q)
			item := map[string]any{"status": status}
			if c != nil {
				item["case"] = c
				item["case_id"] = c.ID
			}
			if err != nil {
				item["error"] = err.Error()
				if fields, ok := err.(fieldValidationError); ok {
					item["field_errors"] = fields.Fields
				}
			}
			items = append(items, item)
		}
		write(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	var q eventReq
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	c, status, err := a.processEvent(r.Context(), q)
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			write(w, 409, map[string]string{"error": err.Error()})
		} else if fields, ok := err.(fieldValidationError); ok {
			write(w, 400, map[string]any{"error": fields.Error(), "field_errors": fields.Fields})
		} else {
			write(w, 400, map[string]string{"error": err.Error()})
		}
		return
	}
	if status == "replay" {
		write(w, 200, c)
	} else {
		write(w, 201, c)
	}
}

func (a *API) processEvent(ctx context.Context, q eventReq) (*store.MicroclimateCase, string, error) {
	missing := map[string]string{}
	if q.Temperature == nil {
		missing["temperature"] = "required"
	}
	if q.Humidity == nil {
		missing["humidity"] = "required"
	}
	if q.DurationMinutes == nil {
		missing["duration_minutes"] = "required"
	}
	if q.CabinetID == "" {
		missing["cabinet_id"] = "required"
	}
	if q.ArtifactSensitivity == "" {
		missing["artifact_sensitivity"] = "required"
	} else if q.ArtifactSensitivity != "低" && q.ArtifactSensitivity != "普通" && q.ArtifactSensitivity != "中" && q.ArtifactSensitivity != "高" {
		missing["artifact_sensitivity"] = "unsupported sensitivity"
	}
	if q.CollectedAt != "" {
		if _, err := time.Parse(time.RFC3339, q.CollectedAt); err != nil {
			missing["collected_at"] = "must be RFC3339"
		}
	}
	if q.CalibrationAt != "" {
		if _, err := time.Parse(time.RFC3339, q.CalibrationAt); err != nil {
			missing["calibration_at"] = "must be RFC3339"
		}
	}
	if q.TemperatureUnit != "" {
		u := strings.ToUpper(strings.TrimSpace(q.TemperatureUnit))
		if u != "C" && u != "°C" && u != "℃" && u != "CELSIUS" && u != "F" && u != "°F" && u != "FAHRENHEIT" {
			missing["temperature_unit"] = "unsupported unit"
		}
	}
	if q.HumidityUnit != "" {
		u := strings.ToUpper(strings.TrimSpace(q.HumidityUnit))
		if u != "%" && u != "%RH" && u != "RH" {
			missing["humidity_unit"] = "unsupported unit"
		}
	}
	if len(missing) > 0 {
		return nil, "validation_failed", fieldValidationError{Fields: missing}
	}
	if *q.Temperature < -50 || *q.Temperature > 80 {
		missing["temperature"] = "out of range"
	}
	if *q.Humidity < 0 || *q.Humidity > 100 {
		missing["humidity"] = "out of range"
	}
	if *q.DurationMinutes <= 0 || *q.DurationMinutes > rules.MaxDurationMinutes {
		missing["duration_minutes"] = "out of range"
	}
	if len(missing) > 0 {
		return nil, "validation_failed", fieldValidationError{Fields: missing}
	}
	if q.IdempotencyKey != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`).MatchString(q.IdempotencyKey) {
		return nil, "validation_failed", errors.New("invalid idempotency_key")
	}
	var replay error
	if q.IdempotencyKey != "" {
		_, replay = a.core.GetByIdempotency(q.IdempotencyKey)
	} else {
		replay = store.ErrNotFound
	}
	if replay != nil && q.CollectedAt != "" {
		if _, eventErr := a.core.GetByEvent(q.CabinetID, q.CollectedAt); eventErr == nil {
			replay = nil
		}
	}
	if q.RuleVersion != "" {
		if _, ok := rules.Basis(q.RuleVersion); !ok {
			return nil, "validation_failed", errors.New("unknown rule_version")
		}
	}
	// Observe the cancellation signal before performing the side-effectful
	// Create call so a cancelled request does not persist a disposal ticket.
	if err := ctx.Err(); err != nil {
		return nil, "cancelled", err
	}
	c, e := a.core.Create(q.CabinetID, rules.SensorSnapshot{Temperature: *q.Temperature, Humidity: *q.Humidity, DurationMinutes: *q.DurationMinutes, CollectedAt: q.CollectedAt, TemperatureUnit: q.TemperatureUnit, HumidityUnit: q.HumidityUnit}, q.ArtifactSensitivity, q.IdempotencyKey, q.RuleVersion, q.CalibrationAt, q.SensorSerial)
	if e != nil {
		if errors.Is(e, store.ErrIdempotencyConflict) {
			return nil, "conflict", e
		} else {
			return nil, "validation_failed", e
		}
	}
	if replay == nil {
		return c, "replay", nil
	}
	return c, "created", nil
}

type fieldValidationError struct{ Fields map[string]string }

func (e fieldValidationError) Error() string { return "field validation failed" }
func (a *API) caseRouter(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "v1" && parts[1] == "cases" && r.Method == "GET" {
		q := r.URL.Query()
		riskRaw := q.Get("risk_level")
		if riskRaw == "" {
			riskRaw = q.Get("risk")
		}
		risk := rules.RiskLevel(riskRaw)
		if risk != "" && rules.Rank(risk) == 0 {
			write(w, 400, map[string]string{"error": "unknown risk_level"})
			return
		}
		limit := 20
		if raw := q.Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				write(w, 400, map[string]string{"error": "limit must be 1..100"})
				return
			}
		}
		if cursor := q.Get("cursor"); cursor != "" {
			filtered, _, _ := a.core.List(q.Get("cabinet_id"), q.Get("status"), risk)
			found := false
			for _, item := range filtered {
				if item.ID == cursor {
					found = true
					break
				}
			}
			if !found {
				write(w, 400, map[string]string{"error": "invalid cursor"})
				return
			}
		}
		assignee := q.Get("assignee_id")
		if assignee != "" && (strings.TrimSpace(assignee) != assignee || strings.ContainsAny(assignee, " \t\n")) {
			write(w, 400, map[string]string{"error": "invalid assignee_id"})
			return
		}
		before := q.Get("updated_before")
		if before != "" {
			if _, err := time.Parse(time.RFC3339, before); err != nil {
				write(w, 400, map[string]string{"error": "invalid updated_before"})
				return
			}
		}
		overdue := false
		if raw := q.Get("overdue"); raw != "" {
			var err error
			overdue, err = strconv.ParseBool(raw)
			if err != nil {
				write(w, 400, map[string]string{"error": "invalid overdue"})
				return
			}
		}
		if cursor := q.Get("cursor"); cursor != "" {
			candidate := a.core.ListPageFiltered(q.Get("cabinet_id"), q.Get("status"), risk, assignee, before, overdue, 100000, "")
			found := false
			for _, item := range candidate.Cases {
				if item.ID == cursor {
					found = true
					break
				}
			}
			if !found {
				write(w, 400, map[string]string{"error": "invalid cursor"})
				return
			}
		}
		from, to := q.Get("from"), q.Get("to")
		if from == "" {
			from = q.Get("window_start")
		}
		if to == "" {
			to = q.Get("window_end")
		}
		if from != "" || to != "" {
			ft, ferr := time.Parse(time.RFC3339, from)
			tt, terr := time.Parse(time.RFC3339, to)
			if ferr != nil || terr != nil || !tt.After(ft) || tt.Sub(ft) > 31*24*time.Hour {
				write(w, 400, map[string]string{"error": "invalid statistics window"})
				return
			}
		}
		result := a.core.ListPageFiltered(q.Get("cabinet_id"), q.Get("status"), risk, assignee, before, overdue, limit, q.Get("cursor"))
		stats := a.core.TrendStats(q.Get("cabinet_id"), from, to, q.Get("status"))
		write(w, 200, map[string]any{"cases": result.Cases, "total": result.Total, "counts": result.Counts, "risk_summary": result.Counts, "latest_updated_at": result.Latest, "next_cursor": result.NextCursor, "overdue_count": result.OverdueCount, "quality_pending": result.QualityPending, "rectification_pending": result.RectificationPending, "rectification_overdue": result.RectificationOverdue, "loads": result.Loads, "recovery": a.core.RecoveryDiagnostic(), "recovery_report": a.core.RecoveryReport(), "statistics": stats})
		return
	}
	if len(parts) < 3 {
		write(w, 404, nil)
		return
	}
	id := parts[2]
	if len(parts) == 3 && r.Method == "GET" {
		c, e := a.core.Get(id)
		if e != nil {
			write(w, 404, map[string]string{"error": "not found"})
			return
		}
		c.RecoveryReport = a.core.RecoveryReport()
		write(w, 200, c)
		return
	}
	if len(parts) == 4 && parts[3] == "trace-summary" && r.Method == "GET" {
		q := r.URL.Query()
		if q.Get("verify_chain") != "" || q.Get("role") != "" || q.Get("type") != "" || q.Get("from") != "" || q.Get("to") != "" || q.Get("cursor") != "" || q.Get("limit") != "" {
			limit := 20
			if q.Get("limit") != "" {
				var le error
				limit, le = strconv.Atoi(q.Get("limit"))
				if le != nil || limit < 1 || limit > 100 {
					write(w, 400, map[string]string{"error": "invalid limit"})
					return
				}
			}
			verify := false
			if q.Get("verify_chain") != "" {
				var ve error
				verify, ve = strconv.ParseBool(q.Get("verify_chain"))
				if ve != nil {
					write(w, 400, map[string]string{"error": "invalid verify_chain"})
					return
				}
			}
			v, e := a.core.TraceEvents(id, q.Get("role"), q.Get("type"), q.Get("from"), q.Get("to"), q.Get("cursor"), limit, verify)
			if e != nil {
				write(w, 422, map[string]any{"error": e.Error(), "verified": false, "first_invalid_seq": v["first_invalid_seq"]})
				return
			}
			write(w, 200, v)
			return
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "json"
		}
		redact := false
		if raw := r.URL.Query().Get("redact"); raw != "" {
			var err error
			redact, err = strconv.ParseBool(raw)
			if err != nil {
				write(w, 400, map[string]string{"error": "invalid redact"})
				return
			}
		}
		v, e := a.core.TraceSummaryOptions(id, format, redact)
		if e != nil {
			if errors.Is(e, casecore.ErrInvalidState) {
				write(w, 422, map[string]string{"error": e.Error()})
			} else if strings.Contains(e.Error(), "unknown format") || strings.Contains(e.Error(), "hash mismatch") {
				write(w, 422, map[string]string{"error": e.Error()})
			} else {
				write(w, 404, map[string]string{"error": "not found"})
			}
			return
		}
		write(w, 200, v)
		return
	}
	if len(parts) == 5 && parts[3] == "evidence" && parts[4] == "replace" && r.Method == "POST" {
		a.replaceEvidence(w, r, id)
		return
	}
	if len(parts) != 4 || r.Method != "POST" {
		write(w, 404, map[string]string{"error": "not found"})
		return
	}
	switch parts[3] {
	case "assign":
		a.assign(w, r, id)
	case "accept", "handover":
		a.acceptAssignment(w, r, id)
	case "quality-review", "quality":
		a.qualityReview(w, r, id)
	case "inspections":
		a.inspect(w, r, id)
	case "evidence-replace":
		a.replaceEvidence(w, r, id)
	case "reviews":
		a.review(w, r, id)
	case "close":
		a.close(w, r, id)
	default:
		write(w, 404, map[string]string{"error": "not found"})
	}
}
func revision(r *http.Request) (int, error) {
	v := r.Header.Get("If-Match")
	if v == "" {
		v = r.URL.Query().Get("revision")
	}
	v = strings.Trim(v, "\"")
	return strconv.Atoi(v)
}

type assignReq struct {
	AssigneeID string `json:"assignee_id"`
	Reason     string `json:"reason"`
}

type qualityReq struct {
	Decision string `json:"decision"`
}

func (a *API) qualityReview(w http.ResponseWriter, r *http.Request, id string) {
	if strings.TrimSpace(r.Header.Get("X-Role")) != "保护专员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q qualityReq
	if decode(r, &q) != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	c, e := a.core.ReviewQuality(id, q.Decision, r.Header.Get("X-Operator"), r.Header.Get("X-Role"), rev)
	a.respond(w, c, e)
}

type acceptReq struct {
	Accept *bool `json:"accept"`
}

func (a *API) acceptAssignment(w http.ResponseWriter, r *http.Request, id string) {
	if strings.TrimSpace(r.Header.Get("X-Role")) != "值班保管员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q acceptReq
	if decode(r, &q) != nil || q.Accept == nil {
		write(w, 400, map[string]string{"error": "accept required"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	c, e := a.core.AcceptAssignment(id, r.Header.Get("X-Operator"), r.Header.Get("X-Role"), rev, *q.Accept)
	a.respond(w, c, e)
}

type replacementReq struct {
	OldDigest string `json:"old_digest"`
	NewDigest string `json:"new_digest"`
	Reason    string `json:"reason"`
	Operator  string `json:"operator"`
}

func (a *API) replaceEvidence(w http.ResponseWriter, r *http.Request, id string) {
	role := strings.TrimSpace(r.Header.Get("X-Role"))
	if role != "值班保管员" && role != "保护专员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q replacementReq
	if decode(r, &q) != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	op := q.Operator
	if op == "" {
		op = r.Header.Get("X-Operator")
	}
	c, e := a.core.ReplaceEvidence(id, q.OldDigest, q.NewDigest, q.Reason, op, role, rev)
	a.respond(w, c, e)
}

func (a *API) assign(w http.ResponseWriter, r *http.Request, id string) {
	if role := strings.TrimSpace(r.Header.Get("X-Role")); role != "保护专员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q assignReq
	if decode(r, &q) != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	if q.Reason != "" && strings.TrimSpace(q.Reason) != q.Reason {
		write(w, 400, map[string]string{"error": "invalid reason"})
		return
	}
	c, e := a.core.Assign(id, q.AssigneeID, rev, r.Header.Get("X-Operator"), q.Reason, r.Header.Get("X-Role"))
	a.respond(w, c, e)
}

type inspectReq struct {
	InspectorID                string                            `json:"inspector_id"`
	Temperature                float64                           `json:"temperature"`
	Humidity                   float64                           `json:"humidity"`
	DurationMinutes            int                               `json:"duration_minutes"`
	Observations               string                            `json:"observations"`
	MitigationActions          string                            `json:"mitigation_actions"`
	EvidenceRefs               []string                          `json:"evidence_refs"`
	CollectedAt                string                            `json:"collected_at"`
	TemperatureUnit            string                            `json:"temperature_unit"`
	HumidityUnit               string                            `json:"humidity_unit"`
	RelatedInspectionID        string                            `json:"related_inspection_id"`
	RuleVersion                string                            `json:"rule_version"`
	Recalculate                bool                              `json:"recalculate"`
	ChecklistReceipts          []store.ChecklistReceipt          `json:"checklist_receipts"`
	RectificationConfirmations []store.RectificationConfirmation `json:"rectification_confirmations"`
	EvidenceBindings           []store.EvidenceBinding           `json:"evidence_bindings"`
	DryRun                     bool                              `json:"dry_run"`
	ConfirmToken               string                            `json:"confirm_token"`
	SensorSerial               string                            `json:"sensor_serial"`
	CalibrationAt              string                            `json:"calibration_at"`
}

func (a *API) inspect(w http.ResponseWriter, r *http.Request, id string) {
	if role := strings.TrimSpace(r.Header.Get("X-Role")); role != "值班保管员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q inspectReq
	if decode(r, &q) != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		if q.DryRun {
			if current, ge := a.core.Get(id); ge == nil {
				rev = current.Revision
			} else {
				write(w, 404, map[string]string{"error": "not found"})
				return
			}
		} else {
			write(w, 400, map[string]string{"error": "revision required"})
			return
		}
	}
	if q.DryRun {
		version := q.RuleVersion
		if version == "" {
			version = "v1"
		}
		v, err := a.core.PreviewRecalculate(id, version, rev)
		if err != nil {
			a.respond(w, nil, err)
			return
		}
		write(w, 200, v)
		return
	}
	if q.ConfirmToken != "" {
		version := q.RuleVersion
		c, err := a.core.ConfirmRecalculate(id, version, rev, q.ConfirmToken, r.Header.Get("X-Operator"), r.Header.Get("X-Role"))
		a.respond(w, c, err)
		return
	}
	if q.Recalculate {
		version := q.RuleVersion
		if version == "" {
			write(w, 400, map[string]string{"error": "rule_version required for recalculation"})
			return
		}
		c, err := a.core.Recalculate(id, version, rev, r.Header.Get("X-Operator"))
		a.respond(w, c, err)
		return
	}
	if q.RuleVersion != "" {
		if _, ok := rules.Basis(q.RuleVersion); !ok {
			write(w, 400, map[string]string{"error": "unknown rule_version"})
			return
		}
	}
	rec := store.InspectionRecord{Readings: rules.SensorSnapshot{Temperature: q.Temperature, Humidity: q.Humidity, DurationMinutes: q.DurationMinutes, CollectedAt: q.CollectedAt, TemperatureUnit: q.TemperatureUnit, HumidityUnit: q.HumidityUnit}, ObservedAt: q.CollectedAt, RelatedInspectionID: q.RelatedInspectionID, Observations: q.Observations, MitigationActions: q.MitigationActions, EvidenceRefs: q.EvidenceRefs, RuleVersion: q.RuleVersion, ChecklistReceipts: q.ChecklistReceipts, RectificationConfirmations: q.RectificationConfirmations, EvidenceBindings: q.EvidenceBindings}
	if e := evidence.ValidateStrict(evidence.NormalizeRecord(rec)); e != nil {
		write(w, 400, map[string]string{"error": e.Error()})
		return
	}
	rec.SensorSerial, rec.CalibrationAt = q.SensorSerial, q.CalibrationAt
	c, e := a.core.AddInspectionWithRole(id, q.InspectorID, rec, rev, r.Header.Get("X-Role"))
	a.respond(w, c, e)
}

type reviewReq struct {
	ReviewerID         string                    `json:"reviewer_id"`
	Decision           string                    `json:"decision"`
	Findings           string                    `json:"findings"`
	Rectification      string                    `json:"rectification"`
	IdempotencyKey     string                    `json:"idempotency_key"`
	Rectifications     []store.RectificationTask `json:"rectifications"`
	PreviousDecisionID string                    `json:"previous_decision_id"`
}

func (a *API) review(w http.ResponseWriter, r *http.Request, id string) {
	role := strings.TrimSpace(r.Header.Get("X-Role"))
	if role != "文保专家" && role != "文保技术专家" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	var q reviewReq
	if decode(r, &q) != nil {
		write(w, 400, map[string]string{"error": "invalid body"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	if q.Rectification == "" && len(q.Rectifications) == 0 && q.Decision == "退回" {
		write(w, 400, map[string]string{"error": "rectification required"})
		return
	}
	inspector := ""
	if current, ge := a.core.Get(id); ge == nil && len(current.Inspections) > 0 {
		inspector = current.Inspections[len(current.Inspections)-1].InspectorID
	}
	c, e := a.core.ReviewDetailed(id, q.ReviewerID, q.Decision, q.Findings, q.Rectification, rev, r.Header.Get("X-Role"), inspector, []string{q.IdempotencyKey}, q.Rectifications, q.PreviousDecisionID)
	a.respond(w, c, e)
}
func (a *API) close(w http.ResponseWriter, r *http.Request, id string) {
	if role := strings.TrimSpace(r.Header.Get("X-Role")); role != "保护专员" {
		write(w, 403, map[string]string{"error": "operator role unauthorized"})
		return
	}
	rev, e := revision(r)
	if e != nil {
		write(w, 400, map[string]string{"error": "revision required"})
		return
	}
	c, e := a.core.CloseWithRole(id, rev, r.Header.Get("X-Operator"), r.Header.Get("X-Role"))
	a.respond(w, c, e)
}
func (a *API) respond(w http.ResponseWriter, c *store.MicroclimateCase, e error) {
	if e != nil {
		status := 400
		if errors.Is(e, casecore.ErrUnauthorized) {
			status = 403
		}
		if errors.Is(e, store.ErrConflict) || errors.Is(e, store.ErrIdempotencyConflict) {
			status = 409
		}
		if errors.Is(e, casecore.ErrInvalidState) {
			status = 422
		}
		if errors.Is(e, casecore.ErrReviewerConflict) {
			status = 422
		}
		if errors.Is(e, store.ErrNotFound) {
			status = 404
		}
		if errors.Is(e, store.ErrDuplicateEvent) {
			status = 409
		}
		if errors.Is(e, casecore.ErrIncomplete) || errors.Is(e, store.ErrClosed) {
			status = 422
		}
		if strings.HasPrefix(e.Error(), "recovery diagnostic:") {
			status = 422
		}
		if errors.Is(e, casecore.ErrIncomplete) {
			msg := strings.TrimSpace(strings.TrimPrefix(e.Error(), casecore.ErrIncomplete.Error()+":"))
			missing := []string{}
			if msg != "" {
				missing = strings.Split(msg, ",")
			}
			write(w, status, map[string]any{"error": e.Error(), "missing": missing})
			return
		}
		write(w, status, map[string]string{"error": e.Error()})
		return
	}
	write(w, 200, c)
}
