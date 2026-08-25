package store

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"microclimate/internal/rules"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("revision conflict")
var ErrIdempotencyConflict = errors.New("idempotency payload conflict")
var ErrClosed = errors.New("case is closed")
var ErrDuplicateEvent = errors.New("duplicate cabinet collection event")

type MicroclimateCase struct {
	ID                      string                `json:"id"`
	CaseNo                  string                `json:"case_no"`
	CabinetID               string                `json:"cabinet_id"`
	TriggeredAt             string                `json:"triggered_at"`
	SensorSnapshot          rules.SensorSnapshot  `json:"sensor_snapshot"`
	ArtifactSensitivity     string                `json:"artifact_sensitivity"`
	RiskLevel               rules.RiskLevel       `json:"risk_level"`
	Status                  string                `json:"status"`
	AssigneeID              string                `json:"assignee_id"`
	Revision                int                   `json:"revision"`
	ClosedAt                string                `json:"closed_at,omitempty"`
	Reasons                 []string              `json:"reasons"`
	RiskBasis               []string              `json:"risk_basis"`
	RuleVersion             string                `json:"rule_version,omitempty"`
	RuleBasis               rules.RuleBasis       `json:"rule_basis,omitempty"`
	Checklist               []string              `json:"checklist"`
	ChecklistReceipts       []ChecklistReceipt    `json:"checklist_receipts,omitempty"`
	Stability               StabilityWindow       `json:"stability,omitempty"`
	EvidenceBindings        []EvidenceBinding     `json:"evidence_bindings,omitempty"`
	Escalation              *EscalationRecord     `json:"escalation,omitempty"`
	EscalationEvents        []EscalationRecord    `json:"escalation_events,omitempty"`
	RelatedCaseID           string                `json:"related_case_id,omitempty"`
	TriggerOrder            int                   `json:"trigger_order,omitempty"`
	IdempotencyKey          string                `json:"idempotency_key,omitempty"`
	Inspections             []InspectionRecord    `json:"inspections"`
	EvidenceDigests         []string              `json:"evidence_digests,omitempty"`
	Reviews                 []ReviewDecision      `json:"reviews"`
	Audit                   []string              `json:"audit"`
	Trends                  []TrendRecord         `json:"trends"`
	Rectifications          []RectificationTask   `json:"rectifications"`
	CloseSummary            *TraceSummary         `json:"close_summary,omitempty"`
	MitigationTimeline      []MitigationEffect    `json:"mitigation_timeline,omitempty"`
	AuditEvents             []AuditEvent          `json:"audit_events,omitempty"`
	UpdatedAt               string                `json:"updated_at"`
	RecoveryReport          map[string]any        `json:"recovery_report,omitempty"`
	SensorQualityScore      float64               `json:"sensor_quality_score,omitempty"`
	QualityFlags            []string              `json:"quality_flags,omitempty"`
	QualityStatus           string                `json:"quality_status,omitempty"`
	QualityReviewedBy       string                `json:"quality_reviewed_by,omitempty"`
	QualityReviewedAt       string                `json:"quality_reviewed_at,omitempty"`
	RelatedCaseIDs          []string              `json:"related_case_ids,omitempty"`
	RelatedCaseCount        int                   `json:"related_case_count,omitempty"`
	HighestRelatedRisk      rules.RiskLevel       `json:"highest_related_risk,omitempty"`
	RiskTrend               string                `json:"risk_trend,omitempty"`
	AssignmentPending       bool                  `json:"assignment_pending,omitempty"`
	HandoverReason          string                `json:"handover_reason,omitempty"`
	EvidenceReplacements    []EvidenceReplacement `json:"evidence_replacements,omitempty"`
	SensorDriftRisk         bool                  `json:"sensor_drift_risk,omitempty"`
	CalibrationGoodCount    int                   `json:"calibration_good_count,omitempty"`
	OscillationAlerts       int                   `json:"oscillation_alerts,omitempty"`
	SensorSerial            string                `json:"sensor_serial,omitempty"`
	CalibrationAt           string                `json:"calibration_at,omitempty"`
	PendingCountersign      bool                  `json:"pending_countersign,omitempty"`
	CountersignReviewers    []string              `json:"countersign_reviewers,omitempty"`
	ChecklistReconfirmation []string              `json:"checklist_reconfirmation,omitempty"`
}
type AuditEvent struct {
	Type     string          `json:"type"`
	CaseID   string          `json:"case_id"`
	Revision int             `json:"revision"`
	At       string          `json:"at"`
	Actor    string          `json:"actor,omitempty"`
	Role     string          `json:"role,omitempty"`
	Details  string          `json:"details,omitempty"`
	State    json.RawMessage `json:"state,omitempty"`
	Seq      int             `json:"seq,omitempty"`
	PrevHash string          `json:"prev_hash,omitempty"`
	Hash     string          `json:"hash,omitempty"`
}
type InspectionRecord struct {
	ID                         string                      `json:"id"`
	CaseID                     string                      `json:"case_id"`
	InspectorID                string                      `json:"inspector_id"`
	ObservedAt                 string                      `json:"observed_at"`
	Readings                   rules.SensorSnapshot        `json:"readings"`
	Observations               string                      `json:"observations"`
	MitigationActions          string                      `json:"mitigation_actions"`
	EvidenceRefs               []string                    `json:"evidence_refs"`
	ReviewStatus               string                      `json:"review_status"`
	Trend                      string                      `json:"trend"`
	RelatedInspectionID        string                      `json:"related_inspection_id,omitempty"`
	RuleVersion                string                      `json:"rule_version,omitempty"`
	RuleBasis                  rules.RuleBasis             `json:"rule_basis,omitempty"`
	ActionEffect               string                      `json:"action_effect,omitempty"`
	ChecklistReceipts          []ChecklistReceipt          `json:"checklist_receipts,omitempty"`
	EvidenceBindings           []EvidenceBinding           `json:"evidence_bindings,omitempty"`
	StabilityCount             int                         `json:"stability_count,omitempty"`
	StabilityTarget            int                         `json:"stability_target,omitempty"`
	StabilityResetReason       string                      `json:"stability_reset_reason,omitempty"`
	NextRetestAt               string                      `json:"next_retest_at,omitempty"`
	RectificationConfirmations []RectificationConfirmation `json:"rectification_confirmations,omitempty"`
	SensorSerial               string                      `json:"sensor_serial,omitempty"`
	CalibrationAt              string                      `json:"calibration_at,omitempty"`
	QualityScore               float64                     `json:"quality_score,omitempty"`
	QualityFlags               []string                    `json:"quality_flags,omitempty"`
	Oscillation                bool                        `json:"oscillation,omitempty"`
	OscillationReason          string                      `json:"oscillation_reason,omitempty"`
}
type MitigationEffect struct {
	Action        string `json:"action"`
	ExecutedAt    string `json:"executed_at"`
	WindowMinutes int    `json:"window_minutes"`
	Effect        string `json:"effect"`
	InspectionID  string `json:"inspection_id,omitempty"`
}
type ReviewDecision struct {
	ID                 string         `json:"id"`
	CaseID             string         `json:"case_id"`
	ReviewerID         string         `json:"reviewer_id"`
	Decision           string         `json:"decision"`
	Findings           string         `json:"findings"`
	Rectification      string         `json:"rectification"`
	DecidedAt          string         `json:"decided_at"`
	DecisionRevision   int            `json:"decision_revision"`
	IdempotencyKey     string         `json:"idempotency_key,omitempty"`
	PreviousDecisionID string         `json:"previous_decision_id,omitempty"`
	Diff               map[string]any `json:"diff,omitempty"`
	Current            bool           `json:"current,omitempty"`
}
type TrendRecord struct {
	At     string          `json:"at"`
	From   rules.RiskLevel `json:"from"`
	To     rules.RiskLevel `json:"to"`
	Result string          `json:"result"`
	Reason string          `json:"reason"`
}
type RectificationTask struct {
	ID              string   `json:"id"`
	Requirement     string   `json:"requirement"`
	Completed       bool     `json:"completed"`
	CompletedAt     string   `json:"completed_at,omitempty"`
	CompletedBy     string   `json:"completed_by,omitempty"`
	EvidenceRef     string   `json:"evidence_ref,omitempty"`
	SourceReviewID  string   `json:"source_review_id,omitempty"`
	Priority        string   `json:"priority,omitempty"`
	DueAt           string   `json:"due_at,omitempty"`
	Overdue         bool     `json:"overdue,omitempty"`
	ExtensionReason string   `json:"extension_reason,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty"`
}

type EvidenceReplacement struct {
	OldDigest          string `json:"old_digest"`
	NewDigest          string `json:"new_digest"`
	Reason             string `json:"reason"`
	Operator           string `json:"operator"`
	At                 string `json:"at"`
	SourceInspectionID string `json:"source_inspection_id"`
}
type ChecklistReceipt struct {
	Item        string `json:"item"`
	Status      string `json:"status"`
	CompletedAt string `json:"completed_at,omitempty"`
	Operator    string `json:"operator"`
	Note        string `json:"note,omitempty"`
}
type EvidenceBinding struct {
	Digest             string `json:"digest"`
	Type               string `json:"type"`
	SourceInspectionID string `json:"source_inspection_id"`
	Observation        string `json:"observation,omitempty"`
	Mitigation         string `json:"mitigation,omitempty"`
}
type StabilityWindow struct {
	Count        int    `json:"count"`
	Target       int    `json:"target"`
	Qualified    bool   `json:"qualified"`
	NextRetestAt string `json:"next_retest_at,omitempty"`
	LastFailure  string `json:"last_failure,omitempty"`
}
type EscalationRecord struct {
	At           string `json:"at"`
	FromAssignee string `json:"from_assignee,omitempty"`
	NotifyRole   string `json:"notify_role"`
	Deadline     string `json:"deadline"`
	Reason       string `json:"reason,omitempty"`
	Revision     int    `json:"revision"`
}
type RectificationConfirmation struct {
	TaskID          string `json:"task_id"`
	CompletedAt     string `json:"completed_at"`
	Operator        string `json:"operator"`
	EvidenceRef     string `json:"evidence_ref"`
	NewDueAt        string `json:"new_due_at,omitempty"`
	ExtensionReason string `json:"extension_reason,omitempty"`
}
type TraceSummary struct {
	ClosedAt string          `json:"closed_at"`
	Hash     string          `json:"hash"`
	Content  json.RawMessage `json:"content"`
}

type Store struct {
	mu       sync.RWMutex
	cases    map[string]*MicroclimateCase
	path     string
	recovery string
	byEvent  map[string]string
}

func New(path string) *Store {
	s := &Store{cases: map[string]*MicroclimateCase{}, path: path, byEvent: map[string]string{}}
	s.load()
	return s
}
func (s *Store) load() {
	if s.path == "" {
		return
	}
	b, e := os.ReadFile(s.path)
	snapshotLoaded := e == nil
	var v map[string]*MicroclimateCase
	if snapshotLoaded && json.Unmarshal(b, &v) == nil && v != nil {
		s.cases = v
		for id, c := range v {
			if c.SensorSnapshot.CollectedAt != "" {
				s.byEvent[c.CabinetID+"|"+c.SensorSnapshot.CollectedAt] = id
			}
		}
	} else if snapshotLoaded {
		s.recovery = "快照损坏，已安全降级为空索引"
	}
	if s.path != "" {
		if f, err := os.Open(s.path + ".events.jsonl"); err == nil {
			sc := bufio.NewScanner(f)
			last := map[string]int{}
			lastHash := map[string]string{}
			for sc.Scan() {
				var ev AuditEvent
				if json.Unmarshal(sc.Bytes(), &ev) != nil {
					s.recovery = "审计日志含非法 JSON 行"
					continue
				}
				if ev.CaseID != "" && ev.Revision > 0 && last[ev.CaseID] > 0 && ev.Revision <= last[ev.CaseID] {
					s.recovery = "审计日志含重复 revision"
					continue
				}
				if ev.CaseID != "" && ev.Revision > 0 && last[ev.CaseID] > 0 && ev.Revision != last[ev.CaseID]+1 {
					s.recovery = "审计日志截断或 revision 不连续"
					continue
				}
				if ev.Seq > 0 {
					if ev.PrevHash != lastHash[ev.CaseID] || (ev.Hash != "" && ev.Hash != eventHash(ev)) {
						s.recovery = "审计日志哈希链断裂"
						continue
					}
				}
				if len(ev.State) > 0 {
					var c MicroclimateCase
					if json.Unmarshal(ev.State, &c) == nil && c.ID != "" && (!snapshotLoaded || s.cases[c.ID] == nil || s.cases[c.ID].Revision < c.Revision) {
						s.cases[c.ID] = clone(&c)
						if c.SensorSnapshot.CollectedAt != "" {
							s.byEvent[c.CabinetID+"|"+c.SensorSnapshot.CollectedAt] = c.ID
						}
					}
				} else if ev.CaseID != "" && s.cases[ev.CaseID] == nil {
					s.recovery = "审计日志含孤儿 CaseID"
				}
				if ev.CaseID != "" {
					last[ev.CaseID] = ev.Revision
					lastHash[ev.CaseID] = ev.Hash
				}
			}
			if sc.Err() != nil {
				s.recovery = "审计日志读取截断"
			}
			_ = f.Close()
			if !snapshotLoaded && len(s.cases) > 0 {
				s.persist()
			}
		}
	}
}
func (s *Store) persist() {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return
	}
	b, _ := json.MarshalIndent(s.cases, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}
func (s *Store) appendEvent(ev AuditEvent) {
	if s.path == "" {
		return
	}
	b, _ := json.Marshal(ev)
	f, err := os.OpenFile(s.path+".events.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
}

func eventHash(ev AuditEvent) string {
	b, _ := json.Marshal(struct {
		Type, CaseID                       string
		Revision, Seq                      int
		At, Actor, Role, Details, PrevHash string
		State                              json.RawMessage
	}{ev.Type, ev.CaseID, ev.Revision, ev.Seq, ev.At, ev.Actor, ev.Role, ev.Details, ev.PrevHash, ev.State})
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func chainEvent(events []AuditEvent, ev AuditEvent) AuditEvent {
	ev.Seq = len(events) + 1
	if len(events) > 0 {
		ev.PrevHash = events[len(events)-1].Hash
	}
	ev.Hash = eventHash(ev)
	return ev
}
func (s *Store) Create(c *MicroclimateCase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.IdempotencyKey != "" {
		for _, old := range s.cases {
			if old.IdempotencyKey == c.IdempotencyKey {
				return ErrIdempotencyConflict
			}
		}
	}
	if c.SensorSnapshot.CollectedAt != "" {
		key := c.CabinetID + "|" + c.SensorSnapshot.CollectedAt
		if old, ok := s.byEvent[key]; ok && old != c.ID {
			return ErrDuplicateEvent
		}
		s.byEvent[key] = c.ID
	}
	if c.UpdatedAt == "" {
		c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	event := chainEvent(c.AuditEvents, AuditEvent{Type: "create", CaseID: c.ID, Revision: c.Revision, At: c.UpdatedAt, Details: "异常事件接收"})
	event.State, _ = json.Marshal(c)
	event.Hash = eventHash(event)
	c.AuditEvents = append(c.AuditEvents, event)
	s.cases[c.ID] = clone(c)
	s.appendEvent(event)
	s.persist()
	return nil
}
func (s *Store) Find(id string) (*MicroclimateCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(c), nil
}
func (s *Store) FindByIdempotency(k string) (*MicroclimateCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.cases {
		if c.IdempotencyKey == k {
			return clone(c), nil
		}
	}
	return nil, ErrNotFound
}
func (s *Store) FindByEvent(cabinet, collectedAt string) (*MicroclimateCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if id, ok := s.byEvent[cabinet+"|"+collectedAt]; ok {
		if c, exists := s.cases[id]; exists {
			return clone(c), nil
		}
	}
	return nil, ErrNotFound
}

// AppendAuditNoRevision records a conflict or other non-state-changing event
// without advancing the optimistic-lock revision.
func (s *Store) AppendAuditNoRevision(id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return ErrNotFound
	}
	c = clone(c)
	c.Audit = append(c.Audit, message)
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	s.cases[id] = c
	s.persist()
	return nil
}

func (s *Store) FindOpenRecent(cabinet string, at time.Time) (*MicroclimateCase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found *MicroclimateCase
	for _, c := range s.cases {
		if c.CabinetID != cabinet || c.Status == "已关闭" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, c.TriggeredAt)
		if err != nil || t.Before(at.Add(-24*time.Hour)) || t.After(at.Add(5*time.Minute)) {
			continue
		}
		if found == nil || t.After(parseTime(found.TriggeredAt)) {
			found = c
		}
	}
	if found == nil {
		return nil, ErrNotFound
	}
	return clone(found), nil
}

func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func (s *Store) Update(id string, rev int, fn func(*MicroclimateCase) error) (*MicroclimateCase, error) {
	return s.UpdateActor(id, rev, "", "", fn)
}
func (s *Store) UpdateActor(id string, rev int, actor, role string, fn func(*MicroclimateCase) error) (*MicroclimateCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cases[id]
	if !ok {
		return nil, ErrNotFound
	}
	if c.Status == "已关闭" {
		return nil, ErrClosed
	}
	if c.Revision != rev {
		return nil, ErrConflict
	}
	work := clone(c)
	if err := fn(work); err != nil {
		return nil, err
	}
	work.Revision++
	work.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	typeName := "update"
	details := ""
	if len(work.Audit) > 0 {
		details = work.Audit[len(work.Audit)-1]
		switch {
		case strings.Contains(details, "分派"), strings.Contains(details, "改派"):
			typeName = "assign"
		case strings.Contains(details, "检查"):
			typeName = "inspection"
		case strings.Contains(details, "复核"):
			typeName = "review"
		case strings.Contains(details, "关闭"):
			typeName = "close"
		}
	}
	if actor == "" || role == "" {
		for i := len(work.AuditEvents) - 1; i >= 0; i-- {
			if actor == "" {
				actor = work.AuditEvents[i].Actor
			}
			if role == "" {
				role = work.AuditEvents[i].Role
			}
			if actor != "" || role != "" {
				break
			}
		}
	}
	// Normalize actor-only audit entries added by the domain layer into the hash chain.
	prevHash := ""
	for i := range work.AuditEvents {
		if work.AuditEvents[i].Seq == 0 {
			work.AuditEvents[i].Seq = i + 1
		}
		if i > 0 && work.AuditEvents[i].PrevHash == "" {
			work.AuditEvents[i].PrevHash = prevHash
		}
		if work.AuditEvents[i].Hash == "" {
			work.AuditEvents[i].Hash = eventHash(work.AuditEvents[i])
		}
		prevHash = work.AuditEvents[i].Hash
	}
	ev := chainEvent(work.AuditEvents, AuditEvent{Type: typeName, CaseID: id, Revision: work.Revision, At: work.UpdatedAt, Actor: actor, Role: role, Details: details})
	work.AuditEvents = append(work.AuditEvents, ev)
	work.AuditEvents[len(work.AuditEvents)-1].State, _ = json.Marshal(work)
	work.AuditEvents[len(work.AuditEvents)-1].Hash = eventHash(work.AuditEvents[len(work.AuditEvents)-1])
	s.cases[id] = work
	s.appendEvent(work.AuditEvents[len(work.AuditEvents)-1])
	s.persist()
	return clone(work), nil
}
func (s *Store) List() []*MicroclimateCase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*MicroclimateCase, 0, len(s.cases))
	for _, c := range s.cases {
		out = append(out, clone(c))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt < out[j].UpdatedAt
	})
	return out
}

func (s *Store) RecoveryDiagnostic() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.recovery }
func (s *Store) RecoveryReport() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h := sha256.Sum256([]byte(s.recovery))
	return map[string]any{"ok": s.recovery == "", "message": s.recovery, "checksum": hex.EncodeToString(h[:])}
}
func clone(c *MicroclimateCase) *MicroclimateCase {
	b, _ := json.Marshal(c)
	var out MicroclimateCase
	_ = json.Unmarshal(b, &out)
	return &out
}
