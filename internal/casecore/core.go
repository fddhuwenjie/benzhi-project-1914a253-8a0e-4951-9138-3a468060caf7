package casecore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"microclimate/internal/evidence"
	"microclimate/internal/rules"
	"microclimate/internal/store"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrInvalidState = errors.New("invalid state transition")
var ErrIncomplete = errors.New("evidence incomplete")
var ErrReviewerConflict = errors.New("reviewer cannot inspect own case")
var ErrUnauthorized = errors.New("operator role unauthorized")

type Service struct {
	st                    *store.Store
	mu                    sync.Mutex
	usedRecalculateTokens map[string]time.Time
}

func New(st *store.Store) *Service {
	return &Service{st: st, usedRecalculateTokens: map[string]time.Time{}}
}
func now() string                    { return time.Now().UTC().Format(time.RFC3339Nano) }
func validSensitivity(s string) bool { return s == "低" || s == "普通" || s == "中" || s == "高" }
func samePayload(c *store.MicroclimateCase, cab string, s rules.SensorSnapshot, sens string, compareTime bool) bool {
	if c.CabinetID != cab || c.SensorSnapshot.Temperature != s.Temperature || c.SensorSnapshot.Humidity != s.Humidity || c.SensorSnapshot.DurationMinutes != s.DurationMinutes || c.ArtifactSensitivity != sens {
		return false
	}
	return !compareTime || c.SensorSnapshot.CollectedAt == s.CollectedAt
}
func (s *Service) Create(cab string, snap rules.SensorSnapshot, sensitivity, idem string, versions ...string) (*store.MicroclimateCase, error) {
	cab = strings.TrimSpace(cab)
	sensitivity = strings.TrimSpace(sensitivity)
	if cab == "" || !validSensitivity(sensitivity) {
		return nil, errors.New("invalid event")
	}
	version := "v1"
	if len(versions) > 0 && versions[0] != "" {
		version = versions[0]
	}
	if _, ok := rules.Basis(version); !ok {
		return nil, errors.New("unknown rule_version")
	}
	providedCollectedAt := snap.CollectedAt != ""
	var err error
	snap, err = rules.NormalizeSnapshot(snap, time.Now(), true)
	if err != nil {
		return nil, err
	}
	if idem != "" {
		if c, e := s.st.FindByIdempotency(idem); e == nil {
			if len(versions) > 0 && versions[0] != "" && c.RuleVersion != versions[0] {
				return nil, store.ErrIdempotencyConflict
			}
			if !samePayload(c, cab, snap, sensitivity, providedCollectedAt) {
				return nil, store.ErrIdempotencyConflict
			}
			return c, nil
		}
	}
	basis, ok := rules.Basis(version)
	if !ok {
		return nil, errors.New("unknown rule_version")
	}
	a := rules.AssessVersion(snap, sensitivity, version)
	calibrationAt := ""
	if len(versions) > 1 {
		calibrationAt = versions[1]
		if calibrationAt != "" {
			t, err := time.Parse(time.RFC3339, calibrationAt)
			if err != nil || t.After(time.Now().Add(5*time.Minute)) {
				return nil, errors.New("invalid calibration_at")
			}
		}
	}
	var previous *rules.SensorSnapshot
	if prior, e := s.st.FindOpenRecent(cab, time.Now()); e == nil {
		p := prior.SensorSnapshot
		previous = &p
	}
	qa := rules.AssessQuality(snap, calibrationAt, previous)
	id := fmt.Sprintf("MC-%d", time.Now().UnixNano())
	qualityStatus := "有效"
	if qa.Score < 80 {
		qualityStatus = "待复核"
	}
	c := &store.MicroclimateCase{ID: id, CaseNo: id, CabinetID: cab, TriggeredAt: now(), SensorSnapshot: snap, ArtifactSensitivity: sensitivity, RiskLevel: a.Level, Status: "待分派", Revision: 1, Reasons: a.Reasons, RiskBasis: a.Reasons, RuleVersion: version, RuleBasis: basis, Checklist: a.Checklist, IdempotencyKey: idem, Audit: []string{"异常事件接收"}, SensorQualityScore: qa.Score, QualityFlags: qa.Flags, QualityStatus: qualityStatus}
	if qualityStatus == "待复核" {
		c.Audit = append(c.Audit, "传感器质量进入人工复核队列")
	}
	if len(versions) > 1 {
		c.CalibrationAt = versions[1]
	}
	if len(versions) > 2 {
		c.SensorSerial = versions[2]
	}
	if qa.Score < 80 {
		c.SensorDriftRisk = true
		c.Checklist = mergeChecklist(c.Checklist, []string{"传感器校准核验"})
	}
	if prior, err := s.st.FindOpenRecent(cab, time.Now()); err == nil {
		c.RelatedCaseID = prior.ID
		c.RelatedCaseIDs = append(append([]string{}, prior.RelatedCaseIDs...), prior.ID)
		c.RelatedCaseCount = prior.RelatedCaseCount + 1
		if c.RelatedCaseCount == 1 {
			c.RelatedCaseCount = 1
		}
		c.HighestRelatedRisk = prior.HighestRelatedRisk
		if c.HighestRelatedRisk == "" || rules.Rank(prior.RiskLevel) > rules.Rank(c.HighestRelatedRisk) {
			c.HighestRelatedRisk = prior.RiskLevel
		}
		c.RiskTrend = rules.TrendDirection(prior.RiskLevel, c.RiskLevel)
		c.TriggerOrder = prior.TriggerOrder + 1
		if c.TriggerOrder == 1 {
			c.TriggerOrder = 2
		}
		c.Audit = append(c.Audit, "复发关联处置单:"+prior.ID)
		if qualityStatus == "待复核" {
			c.RiskTrend = "待质量复核"
		} else if rules.Rank(c.RiskLevel) > rules.Rank(prior.RiskLevel) {
			c.Audit = append(c.Audit, "风险升级审计："+string(prior.RiskLevel)+"->"+string(c.RiskLevel))
			c.Checklist = mergeChecklist(c.Checklist, rules.Checklist(c.RiskLevel, sensitivity))
		} else if rules.Rank(c.RiskLevel) < rules.Rank(prior.RiskLevel) {
			c.RiskLevel = prior.RiskLevel
			c.Reasons = append(c.Reasons, "复发事件保留关联处置单风险结论："+string(prior.RiskLevel))
			c.RiskBasis = c.Reasons
			c.Checklist = mergeChecklist(c.Checklist, prior.Checklist)
		}
	}
	if e := s.st.Create(c); e != nil {
		if errors.Is(e, store.ErrDuplicateEvent) {
			if old, findErr := s.st.FindByEvent(cab, snap.CollectedAt); findErr == nil {
				if samePayload(old, cab, snap, sensitivity, true) {
					return old, nil
				}
				_ = s.st.AppendAuditNoRevision(old.ID, "幂等冲突：展柜与采集时间载荷不一致")
				return nil, store.ErrIdempotencyConflict
			}
		}
		if errors.Is(e, store.ErrIdempotencyConflict) {
			if old, findErr := s.st.FindByIdempotency(idem); findErr == nil {
				_ = s.st.AppendAuditNoRevision(old.ID, "幂等冲突：重复键载荷或规则版本不一致")
			}
		}
		return nil, e
	}
	return c, nil
}
func (s *Service) Assign(id, assignee string, rev int, actors ...string) (*store.MicroclimateCase, error) {
	rawAssignee := assignee
	assignee = strings.TrimSpace(assignee)
	if assignee == "" || rawAssignee != assignee || strings.ContainsAny(assignee, " \t\n") || strings.HasPrefix(strings.ToLower(assignee), "disabled") {
		return nil, errors.New("invalid assignee")
	}
	return s.st.Update(id, rev, func(c *store.MicroclimateCase) error {
		if c.Status != "待分派" && c.Status != "处理中" && c.Status != "已退回" {
			return ErrInvalidState
		}
		old := c.AssigneeID
		actor := ""
		role := ""
		reason := ""
		if len(actors) > 0 {
			actor = strings.TrimSpace(actors[0])
		}
		if len(actors) > 1 {
			reason = strings.TrimSpace(actors[1])
		}
		if len(actors) > 2 {
			role = strings.TrimSpace(actors[2])
		}
		if old != "" && reason == "" {
			return errors.New("reassignment reason required")
		}
		if (actor != "" || role != "") && (strings.HasPrefix(strings.ToLower(actor), "disabled") || role != "保护专员") {
			return ErrUnauthorized
		}
		c.AssigneeID = assignee
		c.HandoverReason = reason
		c.AssignmentPending = true
		if old == "" {
			c.Status = "处理中"
			c.Audit = append(c.Audit, "分派保管员:"+assignee+operatorSuffix(actor))
		} else {
			c.Audit = append(c.Audit, "改派保管员:"+old+"->"+assignee+operatorSuffix(actor))
		}
		if reason != "" {
			c.Audit = append(c.Audit, "改派原因:"+reason)
		}
		if actor != "" {
			c.AuditEvents = append(c.AuditEvents, store.AuditEvent{Actor: actor, Role: role, Details: "assign"})
		}
		c.Escalation = nil
		return nil
	})
}
func operatorSuffix(actor string) string {
	if actor == "" {
		return ""
	}
	return "（操作者:" + actor + "）"
}

// ReviewQuality records the protection specialist's quality gate conclusion.
func (s *Service) ReviewQuality(id, decision, actor, role string, rev int) (*store.MicroclimateCase, error) {
	if role != "保护专员" {
		return nil, ErrUnauthorized
	}
	decision = strings.TrimSpace(decision)
	if decision != "确认" && decision != "驳回" {
		return nil, errors.New("decision must be 确认 or 驳回")
	}
	return s.st.UpdateActor(id, rev, actor, role, func(c *store.MicroclimateCase) error {
		if c.QualityStatus != "待复核" {
			return errors.New("quality review not pending")
		}
		c.QualityReviewedBy, c.QualityReviewedAt = strings.TrimSpace(actor), now()
		if decision == "确认" {
			c.QualityStatus = "已确认"
			c.SensorDriftRisk = false
			c.QualityFlags = nil
			c.Audit = append(c.Audit, "传感器质量复核确认")
		} else {
			c.QualityStatus = "已驳回"
			c.Audit = append(c.Audit, "传感器质量复核驳回")
		}
		c.RiskTrend = ""
		return nil
	})
}

func (s *Service) AcceptAssignment(id, operator, role string, rev int, accept bool) (*store.MicroclimateCase, error) {
	if role != "值班保管员" {
		return nil, ErrUnauthorized
	}
	return s.st.UpdateActor(id, rev, operator, role, func(c *store.MicroclimateCase) error {
		if c.AssigneeID == "" || c.AssigneeID != strings.TrimSpace(operator) {
			return ErrUnauthorized
		}
		if accept {
			c.AssignmentPending = false
			c.Status = "处理中"
			c.Audit = append(c.Audit, "保管员确认接收")
		} else {
			c.AssigneeID = ""
			c.AssignmentPending = false
			c.Status = "待分派"
			c.Audit = append(c.Audit, "保管员拒绝接收")
		}
		return nil
	})
}
func (s *Service) AddInspection(id, inspector string, rec store.InspectionRecord, rev int) (*store.MicroclimateCase, error) {
	return s.AddInspectionWithRole(id, inspector, rec, rev, "")
}

func (s *Service) ReplaceEvidence(id, oldDigest, newDigest, reason, operator, role string, rev int) (*store.MicroclimateCase, error) {
	if role != "值班保管员" && role != "保护专员" {
		return nil, ErrUnauthorized
	}
	oldDigest = strings.ToLower(strings.TrimSpace(oldDigest))
	newDigest = strings.ToLower(strings.TrimSpace(newDigest))
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(oldDigest) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(newDigest) {
		return nil, errors.New("invalid evidence digest")
	}
	if strings.TrimSpace(reason) == "" || strings.TrimSpace(operator) == "" {
		return nil, errors.New("replacement reason and operator required")
	}
	return s.st.UpdateActor(id, rev, operator, role, func(c *store.MicroclimateCase) error {
		var source string
		found := false
		for i := range c.Inspections {
			for j, ref := range c.Inspections[i].EvidenceRefs {
				if ref == oldDigest {
					source = c.Inspections[i].ID
					c.Inspections[i].EvidenceRefs[j] = newDigest
					found = true
				}
			}
		}
		if !found {
			return errors.New("old evidence not found")
		}
		for _, ref := range c.EvidenceDigests {
			if ref == newDigest {
				return errors.New("duplicate evidence digest")
			}
		}
		c.EvidenceDigests = append(c.EvidenceDigests, newDigest)
		for i := range c.EvidenceBindings {
			if c.EvidenceBindings[i].Digest == oldDigest {
				c.EvidenceBindings[i].Digest = newDigest
				c.EvidenceBindings[i].SourceInspectionID = source
			}
		}
		c.EvidenceReplacements = append(c.EvidenceReplacements, store.EvidenceReplacement{OldDigest: oldDigest, NewDigest: newDigest, Reason: evidence.NormalizeObservation(reason), Operator: operator, At: now(), SourceInspectionID: source})
		c.Audit = append(c.Audit, "证据替换:"+oldDigest+"->"+newDigest)
		return nil
	})
}
func (s *Service) AddInspectionWithRole(id, inspector string, rec store.InspectionRecord, rev int, role string) (*store.MicroclimateCase, error) {
	inspector = strings.TrimSpace(inspector)
	if inspector == "" {
		return nil, errors.New("inspection incomplete")
	}
	if rec.CalibrationAt != "" {
		t, err := time.Parse(time.RFC3339, rec.CalibrationAt)
		if err != nil || t.After(time.Now().Add(5*time.Minute)) {
			return nil, errors.New("invalid calibration_at")
		}
	}
	rec = evidence.NormalizeRecord(rec)
	if rec.Readings.Temperature == 0 && rec.Readings.Humidity == 0 && rec.Readings.DurationMinutes == 0 {
		if existing, err := s.st.Find(id); err == nil {
			rec.Readings = existing.SensorSnapshot
		}
	}
	var normalizeErr error
	rec.Readings, normalizeErr = rules.NormalizeSnapshot(rec.Readings, time.Now(), true)
	if normalizeErr != nil {
		return nil, normalizeErr
	}
	if err := evidence.ValidateRecord(rec); err != nil {
		return nil, err
	}
	if err := evidence.ValidateStrict(rec); err != nil {
		legacy := false
		for _, ref := range rec.EvidenceRefs {
			if len(ref) < 20 {
				legacy = true
			}
		}
		if !legacy {
			return nil, err
		}
	}
	legacyEvidence := false
	for _, ref := range rec.EvidenceRefs {
		if len(ref) < 20 {
			legacyEvidence = true
			break
		}
	}
	if rec.RuleVersion == "" {
		rec.RuleVersion = "v1"
		if existing, e := s.st.Find(id); e == nil && existing.RuleVersion != "" {
			rec.RuleVersion = existing.RuleVersion
		}
	}
	basis, ok := rules.Basis(rec.RuleVersion)
	if !ok {
		return nil, errors.New("unknown rule_version")
	}
	rec.RuleBasis = basis
	if t, e := time.Parse(time.RFC3339Nano, rec.ObservedAt); e == nil && t.After(time.Now()) {
		return nil, errors.New("observed_at is in the future")
	}
	if t, e := time.Parse(time.RFC3339Nano, rec.Readings.CollectedAt); e == nil && t.After(time.Now()) {
		return nil, errors.New("collected_at is in the future")
	}
	return s.st.Update(id, rev, func(c *store.MicroclimateCase) error {
		if role != "" && role != "值班保管员" {
			return ErrUnauthorized
		}
		if c.AssigneeID != "" && c.AssigneeID != inspector {
			return ErrUnauthorized
		}
		if c.Status != "处理中" && c.Status != "已退回" {
			return ErrInvalidState
		}
		if rec.RelatedInspectionID != "" {
			found := false
			for _, prior := range c.Inspections {
				if prior.ID == rec.RelatedInspectionID {
					found = true
					break
				}
			}
			if !found {
				return errors.New("related inspection not found")
			}
		}
		if len(c.Inspections) > 0 {
			last := c.Inspections[len(c.Inspections)-1]
			prevAt, e1 := time.Parse(time.RFC3339Nano, last.ObservedAt)
			curAt, e2 := time.Parse(time.RFC3339Nano, rec.ObservedAt)
			if e1 == nil && e2 == nil && !curAt.After(prevAt) {
				return errors.New("observed_at must be later than previous inspection")
			}
			if e2 == nil && curAt.After(time.Now()) {
				return errors.New("observed_at is in the future")
			}
			if collected, e := time.Parse(time.RFC3339Nano, rec.Readings.CollectedAt); e == nil && collected.After(time.Now()) {
				return errors.New("collected_at is in the future")
			}
			if rec.Readings.DurationMinutes < last.Readings.DurationMinutes {
				return errors.New("duration_minutes cannot decrease")
			}
			if d := rec.Readings.Temperature - last.Readings.Temperature; d > 20 || d < -20 {
				return errors.New("temperature jump exceeds 20C")
			}
			if d := rec.Readings.Humidity - last.Readings.Humidity; d > 60 || d < -60 {
				return errors.New("humidity jump exceeds 60 percentage points")
			}
			if strings.TrimSpace(rec.MitigationActions) != "" && strings.EqualFold(rec.MitigationActions, last.MitigationActions) {
				return errors.New("mitigation action must differ from previous")
			}
		}
		old := c.RiskLevel
		receipts, receiptErr := evidence.NormalizeReceipts(rec.ChecklistReceipts, c.Checklist)
		if receiptErr != nil {
			return receiptErr
		}
		for _, receipt := range receipts {
			for _, prior := range c.ChecklistReceipts {
				if prior.Item == receipt.Item {
					return errors.New("duplicate checklist receipt")
				}
			}
		}
		if len(receipts) > 0 {
			rec.ChecklistReceipts = receipts
		}
		for _, confirmation := range rec.RectificationConfirmations {
			if confirmation.TaskID == "" || confirmation.Operator == "" || confirmation.EvidenceRef == "" || confirmation.Operator != inspector {
				return errors.New("invalid rectification confirmation")
			}
			if !regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`).MatchString(strings.TrimSpace(confirmation.EvidenceRef)) {
				return errors.New("invalid rectification evidence")
			}
			found := false
			for i := range c.Rectifications {
				if c.Rectifications[i].ID == confirmation.TaskID {
					for _, dep := range c.Rectifications[i].DependsOn {
						for _, p := range c.Rectifications {
							if p.ID == dep && !p.Completed {
								return fmt.Errorf("rectification dependency incomplete: %s", dep)
							}
						}
					}
					if c.Rectifications[i].DueAt != "" {
						if d, e := time.Parse(time.RFC3339, c.Rectifications[i].DueAt); e == nil && d.Before(time.Now()) {
							if confirmation.NewDueAt == "" || confirmation.ExtensionReason == "" {
								return fmt.Errorf("rectification task overdue: %s", confirmation.TaskID)
							}
							nd, ne := time.Parse(time.RFC3339, confirmation.NewDueAt)
							if ne != nil || !nd.After(time.Now()) {
								return errors.New("invalid extension deadline")
							}
							c.Rectifications[i].DueAt = confirmation.NewDueAt
							c.Rectifications[i].ExtensionReason = confirmation.ExtensionReason
						}
					}
					c.Rectifications[i].Completed = true
					c.Rectifications[i].CompletedAt = confirmation.CompletedAt
					c.Rectifications[i].CompletedBy = confirmation.Operator
					c.Rectifications[i].EvidenceRef = confirmation.EvidenceRef
					found = true
				}
			}
			if !found {
				return errors.New("unknown rectification task")
			}
		}
		a := rules.AssessVersion(rec.Readings, c.ArtifactSensitivity, rec.RuleVersion)
		trend := rules.TrendDirection(old, a.Level)
		rec.ID = fmt.Sprintf("IN-%d", time.Now().UnixNano())
		rec.CaseID = id
		rec.InspectorID = inspector
		var prevSnap *rules.SensorSnapshot
		if len(c.Inspections) > 0 {
			p := c.Inspections[len(c.Inspections)-1].Readings
			prevSnap = &p
		}
		qa := rules.AssessQuality(rec.Readings, rec.CalibrationAt, prevSnap)
		rec.QualityScore, rec.QualityFlags = qa.Score, qa.Flags
		if qa.Score < 60 {
			c.SensorDriftRisk = true
			c.QualityStatus = "待复核"
			c.SensorQualityScore = qa.Score
			c.QualityFlags = qa.Flags
			c.Checklist = mergeChecklist(c.Checklist, []string{"传感器校准核验"})
		} else if c.SensorDriftRisk {
			c.CalibrationGoodCount++
			if c.CalibrationGoodCount >= 2 {
				c.SensorDriftRisk = false
				c.QualityFlags = nil
				c.Audit = append(c.Audit, "传感器质量连续两次合格，清除漂移标记")
			}
		}
		for i := range rec.EvidenceBindings {
			rec.EvidenceBindings[i].Digest = strings.ToLower(strings.TrimSpace(rec.EvidenceBindings[i].Digest))
			if rec.EvidenceBindings[i].Type == "" {
				rec.EvidenceBindings[i].Type = "attachment"
			}
			if rec.EvidenceBindings[i].SourceInspectionID == "" {
				rec.EvidenceBindings[i].SourceInspectionID = rec.ID
			}
		}
		bound := map[string]bool{}
		for _, b := range rec.EvidenceBindings {
			bound[b.Digest] = true
		}
		for _, ref := range rec.EvidenceRefs {
			if !bound[ref] {
				rec.EvidenceBindings = append(rec.EvidenceBindings, store.EvidenceBinding{Digest: ref, Type: "attachment", SourceInspectionID: rec.ID, Observation: rec.Observations, Mitigation: rec.MitigationActions})
			}
		}
		if err := evidence.ValidateEvidenceBindings(rec.EvidenceBindings, rec); err != nil && !legacyEvidence {
			return err
		}
		if rec.ObservedAt == "" {
			rec.ObservedAt = now()
		}
		rec.ReviewStatus = "待复核"
		rec.Trend = trend
		directions := []string{}
		for _, prior := range c.Inspections {
			directions = append(directions, prior.Trend)
		}
		directions = append(directions, trend)
		if rules.Oscillation(directions) {
			rec.Oscillation = true
			rec.OscillationReason = "连续复测风险方向两次反转"
			c.OscillationAlerts++
			c.Stability.Count = 0
			c.Stability.Qualified = false
			c.Stability.LastFailure = rec.OscillationReason
			c.Checklist = mergeChecklist(c.Checklist, []string{"检查气流与调控参数，补充复测"})
			c.Status = "处理中"
		}
		if rec.MitigationActions != "" {
			effect := "未定"
			if rules.Rank(a.Level) < rules.Rank(old) {
				effect = "有效"
			} else if rules.Rank(a.Level) >= rules.Rank(old) {
				effect = "无效"
			}
			rec.ActionEffect = effect
			c.MitigationTimeline = append(c.MitigationTimeline, store.MitigationEffect{Action: rec.MitigationActions, ExecutedAt: rec.ObservedAt, WindowMinutes: 30, Effect: effect, InspectionID: rec.ID})
		}
		c.Inspections = append(c.Inspections, rec)
		seenBinding := map[string]bool{}
		for _, b := range c.EvidenceBindings {
			seenBinding[b.Digest] = true
		}
		for _, b := range rec.EvidenceBindings {
			if !seenBinding[b.Digest] {
				c.EvidenceBindings = append(c.EvidenceBindings, b)
				seenBinding[b.Digest] = true
			}
		}
		for _, receipt := range rec.ChecklistReceipts {
			c.ChecklistReceipts = append(c.ChecklistReceipts, receipt)
		}
		seenEvidence := map[string]bool{}
		for _, ref := range c.EvidenceDigests {
			seenEvidence[ref] = true
		}
		for _, ref := range rec.EvidenceRefs {
			if !seenEvidence[ref] {
				c.EvidenceDigests = append(c.EvidenceDigests, ref)
				seenEvidence[ref] = true
			}
		}
		c.Trends = append(c.Trends, store.TrendRecord{At: rec.ObservedAt, From: old, To: a.Level, Result: trend, Reason: strings.Join(a.Reasons, "；")})
		if c.QualityStatus == "待复核" {
			c.Audit = append(c.Audit, "质量复核未完成，禁止自动升级风险")
		} else if rules.Rank(a.Level) > rules.Rank(c.RiskLevel) {
			c.RiskLevel = a.Level
			c.Reasons = append(c.Reasons, "现场复评："+strings.Join(a.Reasons, "；"))
			c.RiskBasis = c.Reasons
			c.Checklist = mergeChecklist(c.Checklist, rules.Checklist(a.Level, c.ArtifactSensitivity))
		} else if rules.Rank(a.Level) < rules.Rank(c.RiskLevel) {
			c.Audit = append(c.Audit, "现场复评较低，保留最高风险："+string(c.RiskLevel))
		}
		if legacyEvidence {
			c.Status = "待复核"
			c.Audit = append(c.Audit, "提交现场检查（趋势"+trend+"）")
		} else {
			target := rules.StabilityTarget(c.RiskLevel)
			qualified := rules.QualifiedRetest(rec.Readings, rec.RuleVersion)
			if len(c.Inspections) > 0 {
				prevAt, _ := time.Parse(time.RFC3339Nano, c.Inspections[len(c.Inspections)-1].ObservedAt)
				curAt, _ := time.Parse(time.RFC3339Nano, rec.ObservedAt)
				if curAt.Sub(prevAt) > 2*time.Hour {
					qualified = false
					c.Stability.LastFailure = "间隔超过2小时"
					rec.StabilityResetReason = c.Stability.LastFailure
				}
			}
			if !qualified {
				c.Stability.Count = 0
				c.Stability.LastFailure = "读数未回到阈值内"
				rec.StabilityResetReason = c.Stability.LastFailure
			} else {
				c.Stability.Count++
			}
			c.Stability.Target = target
			c.Stability.Qualified = c.Stability.Count >= target
			rec.StabilityCount, rec.StabilityTarget = c.Stability.Count, target
			if !c.Stability.Qualified {
				c.Status = "处理中"
				next := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339Nano)
				c.Stability.NextRetestAt = next
				rec.NextRetestAt = next
				c.Audit = append(c.Audit, "稳定窗口未达标："+c.Stability.LastFailure)
			} else if len(c.ChecklistReceipts) < len(c.Checklist) && rules.Rank(c.RiskLevel) >= rules.Rank(rules.High) {
				c.Status = "处理中"
				c.Audit = append(c.Audit, "稳定窗口完成但检查清单仍有缺项")
			} else {
				c.Status = "待复核"
				c.Audit = append(c.Audit, "稳定窗口完成")
			}
		}
		return nil
	})
}
func mergeChecklist(a, b []string) []string {
	seen := map[string]bool{}
	o := []string{}
	for _, v := range append(a, b...) {
		if !seen[v] {
			seen[v] = true
			o = append(o, v)
		}
	}
	return o
}

func (s *Service) Recalculate(id, version string, rev int, actor string) (*store.MicroclimateCase, error) {
	if _, ok := rules.Basis(version); !ok {
		return nil, errors.New("unknown rule_version")
	}
	return s.st.Update(id, rev, func(c *store.MicroclimateCase) error {
		if c.Status != "待分派" && c.Status != "处理中" && c.Status != "已退回" {
			return ErrInvalidState
		}
		old := c.RiskLevel
		a := rules.AssessVersion(c.SensorSnapshot, c.ArtifactSensitivity, version)
		c.Audit = append(c.Audit, "规则重算预览已确认（"+version+"）")
		c.RuleVersion, c.RuleBasis = version, a.Basis
		removed := []string{}
		for _, oldItem := range c.Checklist {
			found := false
			for _, newItem := range a.Checklist {
				if oldItem == newItem {
					found = true
					break
				}
			}
			if !found {
				removed = append(removed, oldItem)
			}
		}
		if len(removed) > 0 {
			c.ChecklistReconfirmation = mergeChecklist(c.ChecklistReconfirmation, removed)
			c.Audit = append(c.Audit, "规则重算清单减少，待重新确认："+strings.Join(removed, "、"))
		}
		if rules.Rank(a.Level) > rules.Rank(old) {
			c.Reasons, c.RiskBasis = a.Reasons, a.Reasons
			c.RiskLevel = a.Level
			c.Checklist = mergeChecklist(c.Checklist, a.Checklist)
			c.Audit = append(c.Audit, "规则重算风险升级："+string(old)+"->"+string(a.Level)+operatorSuffix(actor))
			c.Trends = append(c.Trends, store.TrendRecord{At: now(), From: old, To: a.Level, Result: "升级", Reason: "规则版本 " + version + " 重算"})
		} else if rules.Rank(a.Level) < rules.Rank(old) {
			c.Audit = append(c.Audit, "规则重算结果低于历史最高风险，保留："+string(old)+operatorSuffix(actor))
			c.Trends = append(c.Trends, store.TrendRecord{At: now(), From: old, To: a.Level, Result: "保留最高风险", Reason: "规则版本 " + version + " 重算"})
		} else {
			c.Audit = append(c.Audit, "规则重算风险持平（"+version+"）"+operatorSuffix(actor))
		}
		return nil
	})
}
func (s *Service) PreviewRecalculate(id, version string, rev int) (map[string]any, error) {
	c, err := s.st.Find(id)
	if err != nil {
		return nil, err
	}
	if c.Status == "已关闭" {
		return nil, ErrInvalidState
	}
	if rev != c.Revision {
		return nil, store.ErrConflict
	}
	a, ok := rules.Basis(version)
	if !ok {
		return nil, errors.New("unknown rule_version")
	}
	_ = a
	target := rules.AssessVersion(c.SensorSnapshot, c.ArtifactSensitivity, version)
	add, remove := []string{}, []string{}
	for _, x := range target.Checklist {
		found := false
		for _, y := range c.Checklist {
			if x == y {
				found = true
			}
		}
		if !found {
			add = append(add, x)
		}
	}
	for _, x := range c.Checklist {
		found := false
		for _, y := range target.Checklist {
			if x == y {
				found = true
			}
		}
		if !found {
			remove = append(remove, x)
		}
	}
	tokenData := fmt.Sprintf("%s|%d|%s", id, rev, version)
	h := sha256.Sum256([]byte(tokenData))
	token := hex.EncodeToString(h[:])
	return map[string]any{"token": token, "confirm_token": token, "expires_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "snapshot_revision": rev, "current_rule_version": c.RuleVersion, "target_rule_version": version, "current": map[string]any{"risk_level": c.RiskLevel, "reasons": c.Reasons, "checklist": c.Checklist}, "target": map[string]any{"risk_level": target.Level, "reasons": target.Reasons, "checklist": target.Checklist, "rule_version": version}, "added_checklist": add, "removed_checklist": remove}, nil
}
func (s *Service) ConfirmRecalculate(id, version string, rev int, token, actor, role string) (*store.MicroclimateCase, error) {
	if role != "" && role != "保护专员" {
		return nil, ErrUnauthorized
	}
	preview, err := s.PreviewRecalculate(id, version, rev)
	if err != nil {
		return nil, err
	}
	if token != preview["token"] {
		return nil, store.ErrConflict
	}
	s.mu.Lock()
	if usedAt, used := s.usedRecalculateTokens[token]; used && time.Since(usedAt) < 15*time.Minute {
		s.mu.Unlock()
		return nil, store.ErrConflict
	}
	s.mu.Unlock()
	c, err := s.Recalculate(id, version, rev, actor)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.usedRecalculateTokens[token] = time.Now()
	s.mu.Unlock()
	return c, nil
}
func (s *Service) Review(id, reviewer, decision, findings, rect string, rev int, idem ...string) (*store.MicroclimateCase, error) {
	return s.ReviewDetailed(id, reviewer, decision, findings, rect, rev, "", "", idem, nil)
}
func (s *Service) ReviewDetailed(id, reviewer, decision, findings, rect string, rev int, role string, inspectorID string, idem []string, tasks []store.RectificationTask, previous ...string) (*store.MicroclimateCase, error) {
	reviewer = strings.TrimSpace(reviewer)
	decision = strings.TrimSpace(decision)
	findings = evidence.NormalizeObservation(findings)
	rect = evidence.NormalizeObservation(rect)
	key := ""
	if len(idem) > 0 {
		key = strings.TrimSpace(idem[0])
	}
	if decision != "通过" && decision != "退回" {
		return nil, errors.New("decision must be 通过 or 退回")
	}
	if reviewer == "" {
		return nil, errors.New("reviewer required")
	}
	seenTaskIDs := map[string]bool{}
	for _, t := range tasks {
		if strings.TrimSpace(t.Requirement) == "" {
			return nil, errors.New("rectification requirement required")
		}
		if t.ID != "" {
			if seenTaskIDs[t.ID] {
				return nil, errors.New("duplicate rectification id")
			}
			seenTaskIDs[t.ID] = true
		}
		for _, dep := range t.DependsOn {
			if dep == t.ID {
				return nil, errors.New("rectification task cannot depend on itself")
			}
		}
		if t.DueAt != "" {
			d, err := time.Parse(time.RFC3339, t.DueAt)
			if err != nil || !d.After(time.Now()) {
				return nil, errors.New("invalid rectification deadline")
			}
		}
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if !seenTaskIDs[dep] {
				return nil, errors.New("rectification dependency not found")
			}
		}
	}
	if hasTaskCycle(tasks) {
		return nil, errors.New("rectification dependency cycle")
	}
	if key != "" {
		if existing, err := s.st.Find(id); err == nil {
			for _, prior := range existing.Reviews {
				if prior.IdempotencyKey == key {
					if prior.ReviewerID == reviewer && prior.Decision == decision && prior.Findings == findings && prior.Rectification == rect {
						return existing, nil
					}
					return nil, store.ErrIdempotencyConflict
				}
			}
		}
	}
	return s.st.Update(id, rev, func(c *store.MicroclimateCase) error {
		if role != "" && role != "文保专家" && role != "文保技术专家" {
			return ErrUnauthorized
		}
		if strings.HasPrefix(strings.ToLower(reviewer), "disabled") {
			return ErrUnauthorized
		}
		if c.Status != "待复核" && c.Status != "已退回" {
			return ErrInvalidState
		}
		if len(c.Reviews) > 0 && c.Reviews[len(c.Reviews)-1].Decision == "退回" {
			if c.Revision <= c.Reviews[len(c.Reviews)-1].DecisionRevision {
				return fmt.Errorf("%w: new inspection evidence required", ErrIncomplete)
			}
			if len(previous) == 0 || strings.TrimSpace(previous[0]) == "" || c.Reviews[len(c.Reviews)-1].ID != strings.TrimSpace(previous[0]) {
				return errors.New("previous decision reference required")
			}
		}
		if c.PendingCountersign {
			if role != "文保技术专家" {
				return ErrUnauthorized
			}
			if len(c.CountersignReviewers) > 0 && c.CountersignReviewers[0] == reviewer {
				return ErrReviewerConflict
			}
		} else if c.RiskLevel == rules.Emergency && role != "" && role != "文保专家" {
			return ErrUnauthorized
		}
		if findings == "" {
			return errors.New("findings required")
		}
		if len(c.Inspections) > 0 && c.Inspections[len(c.Inspections)-1].InspectorID == reviewer {
			return ErrReviewerConflict
		}
		for _, prior := range c.Reviews {
			if prior.DecisionRevision == rev {
				return store.ErrConflict
			}
		}
		missing := evidence.Missing(c)
		legacy := false
		// Keep compatibility for direct callers using legacy non-digest fixtures;
		// the HTTP boundary always enforces strict evidence and measures.
		if len(c.Inspections) > 0 {
			for _, ref := range c.Inspections[len(c.Inspections)-1].EvidenceRefs {
				if len(ref) < 20 {
					legacy = true
				}
			}
			if legacy {
				filtered := missing[:0]
				for _, item := range missing {
					if item != "mitigation_actions" {
						filtered = append(filtered, item)
					}
				}
				missing = filtered
			}
		}
		if len(missing) > 0 && decision == "通过" {
			return fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(missing, ","))
		}
		if decision == "退回" && rect == "" {
			return errors.New("rectification required")
		}
		if decision == "通过" && !legacy {
			if c.SensorDriftRisk {
				return fmt.Errorf("%w: sensor_quality_pending", ErrIncomplete)
			}
			if !c.Stability.Qualified {
				return fmt.Errorf("%w: 稳定窗口未达标", ErrIncomplete)
			}
			missingChecklist := []string{}
			for _, item := range c.Checklist {
				found := false
				for _, receipt := range c.ChecklistReceipts {
					if receipt.Item == item && receipt.Status == "completed" {
						found = true
					}
				}
				if !found {
					missingChecklist = append(missingChecklist, item)
				}
			}
			if len(missingChecklist) > 0 && rules.Rank(c.RiskLevel) >= rules.Rank(rules.High) {
				return fmt.Errorf("%w: checklist:%s", ErrIncomplete, strings.Join(missingChecklist, ","))
			}
		}
		r := store.ReviewDecision{ID: fmt.Sprintf("RV-%d", time.Now().UnixNano()), CaseID: id, ReviewerID: reviewer, Decision: decision, Findings: findings, Rectification: rect, DecidedAt: now(), DecisionRevision: c.Revision, IdempotencyKey: key, Current: true}
		if len(previous) > 0 && strings.TrimSpace(previous[0]) != "" {
			r.PreviousDecisionID = strings.TrimSpace(previous[0])
			for i := range c.Reviews {
				c.Reviews[i].Current = false
			}
			if len(c.Reviews) > 0 {
				p := c.Reviews[len(c.Reviews)-1]
				r.Diff = map[string]any{}
				if p.Findings != findings {
					r.Diff["findings"] = map[string]string{"from": p.Findings, "to": findings}
				}
				if p.Rectification != rect {
					r.Diff["rectification"] = map[string]string{"from": p.Rectification, "to": rect}
				}
			}
		}
		if c.RiskLevel == rules.Emergency {
			if decision == "通过" && c.PendingCountersign {
				if len(c.CountersignReviewers) > 0 && c.CountersignReviewers[0] == reviewer {
					return ErrReviewerConflict
				}
				c.CountersignReviewers = append(c.CountersignReviewers, reviewer)
				c.PendingCountersign = false
			} else if decision == "退回" && c.PendingCountersign {
				if len(c.CountersignReviewers) > 0 && c.CountersignReviewers[0] == reviewer {
					return ErrReviewerConflict
				}
				c.CountersignReviewers = append(c.CountersignReviewers, reviewer)
				c.PendingCountersign = false
			}
			if decision == "通过" && !c.PendingCountersign && len(c.CountersignReviewers) == 0 {
				c.PendingCountersign = true
				c.CountersignReviewers = []string{reviewer}
			}
			/*
				A first emergency decision is retained as-is but cannot close the
				case until an independent technical expert signs it.
			*/
			/* legacy branch intentionally kept below for non-emergency callers */
		}
		c.Reviews = append(c.Reviews, r)
		if decision == "通过" {
			for _, t := range c.Rectifications {
				if !t.Completed {
					return ErrIncomplete
				}
				for _, dep := range t.DependsOn {
					for _, p := range c.Rectifications {
						if p.ID == dep && !p.Completed {
							return fmt.Errorf("%w: dependency:%s", ErrIncomplete, dep)
						}
					}
				}
			}
			if !c.PendingCountersign {
				c.Status = "待关闭"
			} else {
				c.Status = "待复核"
			}
		} else {
			c.Status = "已退回"
			if len(tasks) > 0 {
				c.Rectifications = normalizeTasks(tasks, r.ID)
			} else {
				c.Rectifications = splitRectifications(rect, r.ID)
			}
			for i := range c.Rectifications {
				if c.Rectifications[i].Priority == "" {
					if c.RiskLevel == rules.Emergency || c.RiskLevel == rules.High {
						c.Rectifications[i].Priority = "高"
					} else {
						c.Rectifications[i].Priority = "普通"
					}
				}
				if c.Rectifications[i].DueAt == "" {
					window := 48 * time.Hour
					if c.RiskLevel == rules.Emergency {
						window = 12 * time.Hour
					}
					c.Rectifications[i].DueAt = time.Now().UTC().Add(window).Format(time.RFC3339)
				}
			}
		}
		c.Audit = append(c.Audit, "专家复核:"+decision)
		if reviewer != "" {
			c.AuditEvents = append(c.AuditEvents, store.AuditEvent{Actor: reviewer, Role: role, Details: "review"})
		}
		return nil
	})
}

func splitRectifications(text, reviewID string) []store.RectificationTask {
	seen := map[string]bool{}
	out := []store.RectificationTask{}
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' || r == ';' || r == '；' }) {
		v := evidence.NormalizeObservation(part)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, store.RectificationTask{ID: fmt.Sprintf("RT-%d", len(out)+1), Requirement: v, SourceReviewID: reviewID})
	}
	return out
}
func normalizeTasks(in []store.RectificationTask, reviewID string) []store.RectificationTask {
	out := []store.RectificationTask{}
	seen := map[string]bool{}
	for i, t := range in {
		t.Requirement = evidence.NormalizeObservation(t.Requirement)
		if t.ID == "" {
			t.ID = fmt.Sprintf("RT-%d", i+1)
		}
		if t.Requirement == "" || seen[t.ID] {
			continue
		}
		if t.DueAt != "" {
			if d, err := time.Parse(time.RFC3339, t.DueAt); err == nil {
				t.Overdue = d.Before(time.Now())
			}
		}
		t.SourceReviewID = reviewID
		seen[t.ID] = true
		out = append(out, t)
	}
	return out
}
func hasTaskCycle(tasks []store.RectificationTask) bool {
	graph := map[string][]string{}
	for _, t := range tasks {
		id := t.ID
		if id == "" {
			continue
		}
		graph[id] = t.DependsOn
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		if state[id] == 1 {
			return true
		}
		if state[id] == 2 {
			return false
		}
		state[id] = 1
		for _, d := range graph[id] {
			if visit(d) {
				return true
			}
		}
		state[id] = 2
		return false
	}
	for id := range graph {
		if visit(id) {
			return true
		}
	}
	return false
}
func (s *Service) Close(id string, rev int) (*store.MicroclimateCase, error) {
	return s.CloseWithRole(id, rev, "", "")
}
func (s *Service) CloseWithRole(id string, rev int, actor, role string) (*store.MicroclimateCase, error) {
	if d := s.st.RecoveryDiagnostic(); d != "" {
		return nil, fmt.Errorf("recovery diagnostic: %s", d)
	}
	if existing, err := s.st.Find(id); err == nil && existing.Status == "已关闭" {
		return existing, nil
	}
	return s.st.Update(id, rev, func(c *store.MicroclimateCase) error {
		if role != "" && role != "文保专家" && role != "保护专员" {
			return ErrUnauthorized
		}
		if c.Status != "待关闭" {
			missing := []string{}
			if len(c.Inspections) == 0 {
				missing = append(missing, "inspections")
			}
			if len(c.Reviews) == 0 {
				missing = append(missing, "review")
			}
			if !c.Stability.Qualified {
				missing = append(missing, "stability_window")
			}
			if c.PendingCountersign {
				missing = append(missing, "countersign")
			}
			if len(missing) == 0 {
				return ErrInvalidState
			}
			return fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(missing, ","))
		}
		for _, t := range c.Rectifications {
			if !t.Completed {
				if t.DueAt != "" {
					if d, _ := time.Parse(time.RFC3339, t.DueAt); d.Before(time.Now()) {
						return fmt.Errorf("%w: %s", ErrIncomplete, t.ID)
					}
				}
				return ErrIncomplete
			}
		}
		c.Status = "已关闭"
		c.ClosedAt = now()
		c.Audit = append(c.Audit, "处置单关闭")
		p, _ := json.Marshal(struct {
			ID             string
			Risk           rules.RiskLevel
			RiskBasis      []string
			RuleVersion    string
			Checklist      []string
			Ins            []store.InspectionRecord
			Rev            []store.ReviewDecision
			Evidence       []store.EvidenceBinding
			Rectifications []store.RectificationTask
			Audit          []string
			Revision       int
		}{c.ID, c.RiskLevel, c.RiskBasis, c.RuleVersion, c.Checklist, c.Inspections, c.Reviews, c.EvidenceBindings, c.Rectifications, c.Audit, c.Revision})
		h := sha256.Sum256(p)
		c.CloseSummary = &store.TraceSummary{ClosedAt: c.ClosedAt, Hash: "sha256:" + hex.EncodeToString(h[:]), Content: json.RawMessage(p)}
		if actor != "" {
			c.AuditEvents = append(c.AuditEvents, store.AuditEvent{Actor: actor, Role: role, Details: "close"})
		}
		return nil
	})
}
func (s *Service) Get(id string) (*store.MicroclimateCase, error) {
	c, err := s.st.Find(id)
	if err != nil {
		return nil, err
	}
	for i := range c.Rectifications {
		if !c.Rectifications[i].Completed && c.Rectifications[i].DueAt != "" {
			if d, e := time.Parse(time.RFC3339, c.Rectifications[i].DueAt); e == nil {
				c.Rectifications[i].Overdue = d.Before(time.Now())
			}
		}
	}
	return c, nil
}
func (s *Service) TraceSummary(id string) (map[string]any, error) {
	return s.TraceSummaryOptions(id, "json", false)
}

func (s *Service) TraceEvents(id, role, typ, from, to, cursor string, limit int, verify bool) (map[string]any, error) {
	c, err := s.st.Find(id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var bad int
	prev := ""
	for i, ev := range c.AuditEvents {
		if ev.Seq != 0 && ev.Seq != i+1 {
			bad = ev.Seq
			break
		}
		if ev.PrevHash != prev {
			bad = ev.Seq
			if bad == 0 {
				bad = i + 1
			}
			break
		}
		if ev.Hash != "" && eventHashForTrace(ev) != ev.Hash {
			bad = ev.Seq
			if bad == 0 {
				bad = i + 1
			}
			break
		}
		prev = ev.Hash
	}
	if verify && bad > 0 {
		return map[string]any{"verified": false, "first_invalid_seq": bad, "events": []store.AuditEvent{}}, fmt.Errorf("audit chain invalid at sequence %d", bad)
	}
	ft, _ := time.Parse(time.RFC3339, from)
	tt, _ := time.Parse(time.RFC3339, to)
	filtered := []store.AuditEvent{}
	start := false
	for _, ev := range c.AuditEvents {
		if role != "" && ev.Role != role {
			continue
		}
		if typ != "" && ev.Type != typ {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, ev.At)
		if from != "" && t.Before(ft) || to != "" && t.After(tt) {
			continue
		}
		if cursor != "" && !start {
			if strconv.Itoa(ev.Seq) == cursor || ev.Hash == cursor {
				start = true
			}
			continue
		}
		start = true
		filtered = append(filtered, ev)
	}
	next := ""
	if len(filtered) > limit {
		next = strconv.Itoa(filtered[limit-1].Seq)
		filtered = filtered[:limit]
	}
	return map[string]any{"verified": bad == 0, "first_invalid_seq": bad, "events": filtered, "next_cursor": next, "total": len(filtered)}, nil
}
func eventHashForTrace(ev store.AuditEvent) string {
	b, _ := json.Marshal(struct {
		Type, CaseID                       string
		Revision, Seq                      int
		At, Actor, Role, Details, PrevHash string
		State                              json.RawMessage
	}{ev.Type, ev.CaseID, ev.Revision, ev.Seq, ev.At, ev.Actor, ev.Role, ev.Details, ev.PrevHash, ev.State})
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func (s *Service) TraceSummaryOptions(id, format string, redact bool) (map[string]any, error) {
	c, err := s.st.Find(id)
	if err != nil {
		return nil, err
	}
	if c.Status != "已关闭" || c.CloseSummary == nil {
		return nil, ErrInvalidState
	}
	if format != "json" && format != "package" {
		return nil, errors.New("unknown format")
	}
	content := c.CloseSummary.Content
	h := sha256.Sum256(content)
	computed := "sha256:" + hex.EncodeToString(h[:])
	valid := computed == c.CloseSummary.Hash
	if !valid {
		return map[string]any{"closed_at": c.CloseSummary.ClosedAt, "stored_hash": c.CloseSummary.Hash, "computed_hash": computed, "valid": false}, errors.New("close summary hash mismatch")
	}
	var payload any
	_ = json.Unmarshal(content, &payload)
	if redact {
		payload = redactPayload(payload)
	}
	if format == "package" {
		payload = map[string]any{"risk": c.RiskLevel, "inspections": c.Inspections, "reviews": c.Reviews, "rectifications": c.Rectifications, "audit": c.Audit, "recovery": c.CloseSummary.ClosedAt}
		if redact {
			payload = redactPayload(payload)
		}
	}
	pack, _ := json.Marshal(payload)
	ph := sha256.Sum256(pack)
	computed = "sha256:" + hex.EncodeToString(ph[:])
	return map[string]any{"closed_at": c.CloseSummary.ClosedAt, "hash": computed, "stored_hash": c.CloseSummary.Hash, "computed_hash": computed, "content": json.RawMessage(pack), "valid": true, "evidence_valid": true, "evidence_count": len(c.EvidenceBindings), "format": format, "redacted": redact}, nil
}
func redactPayload(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k := range x {
			if strings.Contains(strings.ToLower(k), "operator") || strings.Contains(strings.ToLower(k), "reviewer") || strings.Contains(strings.ToLower(k), "assignee") || k == "actor" {
				x[k] = "[已脱敏]"
			} else {
				x[k] = redactPayload(x[k])
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = redactPayload(x[i])
		}
		return x
	default:
		return v
	}
}
func (s *Service) GetByIdempotency(key string) (*store.MicroclimateCase, error) {
	return s.st.FindByIdempotency(key)
}

func (s *Service) GetByEvent(cabinet, collectedAt string) (*store.MicroclimateCase, error) {
	// Normalize the collection timestamp to the same UTC RFC3339Nano form
	// used by Create (via rules.NormalizeSnapshot) when building the store's
	// byEvent index. Without this, requests carrying a non-UTC timezone
	// offset (e.g. +08:00) would never match the normalized key and the
	// replay pre-check would fail, causing duplicate requests to be
	// reported as new (201) instead of idempotent replays (200).
	if collectedAt != "" {
		if t, err := time.Parse(time.RFC3339, collectedAt); err == nil {
			collectedAt = t.UTC().Format(time.RFC3339Nano)
		}
	}
	return s.st.FindByEvent(strings.TrimSpace(cabinet), collectedAt)
}
func (s *Service) List(cab, status string, risk rules.RiskLevel) ([]*store.MicroclimateCase, int, string) {
	all := s.st.List()
	o := []*store.MicroclimateCase{}
	latest := ""
	for _, c := range all {
		if cab != "" && c.CabinetID != cab || status != "" && c.Status != status || risk != "" && c.RiskLevel != risk {
			continue
		}
		o = append(o, c)
		if c.UpdatedAt > latest {
			latest = c.UpdatedAt
		}
	}
	return o, len(o), latest
}

type ListResult struct {
	Cases                []*store.MicroclimateCase
	Total                int
	Latest               string
	Counts               map[rules.RiskLevel]int
	NextCursor           string
	OverdueCount         int           `json:"overdue_count"`
	Loads                []LoadSummary `json:"loads"`
	RectificationPending int           `json:"rectification_pending"`
	RectificationOverdue int           `json:"rectification_overdue"`
	QualityPending       int           `json:"quality_pending"`
}

func (s *Service) ListPage(cab, status string, risk rules.RiskLevel, limit int, cursor string) ListResult {
	return s.ListPageFiltered(cab, status, risk, "", "", false, limit, cursor)
}

type LoadSummary struct {
	AssigneeID        string `json:"assignee_id"`
	Active            int    `json:"active"`
	Overdue           int    `json:"overdue"`
	EarliestUpdatedAt string `json:"earliest_updated_at,omitempty"`
}

func (s *Service) ListPageFiltered(cab, status string, risk rules.RiskLevel, assignee, updatedBefore string, overdue bool, limit int, cursor string) ListResult {
	all, _, _ := s.List(cab, status, risk)
	for _, c := range all {
		s.evaluateEscalation(c)
	}
	cutoff, _ := time.Parse(time.RFC3339, updatedBefore)
	filtered := all[:0]
	for _, c := range all {
		if assignee != "" && c.AssigneeID != assignee {
			continue
		}
		if updatedBefore != "" && !parseTime(c.UpdatedAt).Before(cutoff) {
			continue
		}
		isOver := (c.Status == "处理中" || c.Status == "已退回") && time.Since(parseTime(c.UpdatedAt)) > 4*time.Hour
		if overdue != isOver && overdue {
			continue
		}
		filtered = append(filtered, c)
	}
	all = filtered
	result := ListResult{Counts: map[rules.RiskLevel]int{rules.Low: 0, rules.Medium: 0, rules.High: 0, rules.Emergency: 0}}
	for _, c := range all {
		if c.QualityStatus == "待复核" {
			result.QualityPending++
		}
		result.Counts[c.RiskLevel]++
		if c.UpdatedAt > result.Latest {
			result.Latest = c.UpdatedAt
		}
		isOver := (c.Status == "处理中" || c.Status == "已退回") && time.Since(parseTime(c.UpdatedAt)) > 4*time.Hour
		if isOver {
			result.OverdueCount++
		}
		for _, t := range c.Rectifications {
			if !t.Completed {
				result.RectificationPending++
				if t.DueAt != "" {
					if d, e := time.Parse(time.RFC3339, t.DueAt); e == nil && d.Before(time.Now()) {
						result.RectificationOverdue++
					}
				}
			}
		}
	}
	loadMap := map[string]*LoadSummary{}
	for _, c := range all {
		if c.AssigneeID == "" {
			continue
		}
		l := loadMap[c.AssigneeID]
		if l == nil {
			l = &LoadSummary{AssigneeID: c.AssigneeID}
			loadMap[c.AssigneeID] = l
		}
		l.Active++
		if l.EarliestUpdatedAt == "" || c.UpdatedAt < l.EarliestUpdatedAt {
			l.EarliestUpdatedAt = c.UpdatedAt
		}
		if (c.Status == "处理中" || c.Status == "已退回") && time.Since(parseTime(c.UpdatedAt)) > 4*time.Hour {
			l.Overdue++
		}
	}
	for _, l := range loadMap {
		result.Loads = append(result.Loads, *l)
	}
	sort.Slice(result.Loads, func(i, j int) bool { return result.Loads[i].AssigneeID < result.Loads[j].AssigneeID })
	result.Total = len(all)
	start := 0
	if cursor != "" {
		for i, c := range all {
			if c.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	result.Cases = all[start:end]
	if end < len(all) && end > 0 {
		result.NextCursor = all[end-1].ID
	}
	return result
}

func (s *Service) evaluateEscalation(c *store.MicroclimateCase) {
	if c == nil || c.QualityStatus == "待复核" || (c.Status != "处理中" && c.Status != "已退回") || c.Escalation != nil {
		return
	}
	t, err := time.Parse(time.RFC3339Nano, c.TriggeredAt)
	if err != nil || time.Now().Before(t.Add(rules.ResponseWindow(c.RiskLevel))) {
		return
	}
	deadline := t.Add(rules.ResponseWindow(c.RiskLevel)).UTC().Format(time.RFC3339Nano)
	updated, err := s.st.Update(c.ID, c.Revision, func(w *store.MicroclimateCase) error {
		if w.Escalation != nil {
			return nil
		}
		ev := store.EscalationRecord{At: now(), FromAssignee: w.AssigneeID, NotifyRole: "文保专家", Deadline: deadline, Reason: "超过风险响应时限", Revision: w.Revision + 1}
		w.Escalation = &ev
		w.EscalationEvents = append(w.EscalationEvents, ev)
		w.Audit = append(w.Audit, "超时升级:"+ev.NotifyRole)
		return nil
	})
	if err == nil {
		*c = *updated
	}
}

type TrendStats struct {
	CabinetID           string                  `json:"cabinet_id,omitempty"`
	From                string                  `json:"from,omitempty"`
	To                  string                  `json:"to,omitempty"`
	Total               int                     `json:"total"`
	Recurrence          int                     `json:"recurrence"`
	RiskCounts          map[rules.RiskLevel]int `json:"risk_counts"`
	ClosedCount         int                     `json:"closed_count"`
	InProgress          int                     `json:"in_progress"`
	AverageCloseMinutes float64                 `json:"average_close_minutes"`
}

func (s *Service) TrendStats(cabinet, from, to, status string) TrendStats {
	result := TrendStats{CabinetID: cabinet, From: from, To: to, RiskCounts: map[rules.RiskLevel]int{rules.Low: 0, rules.Medium: 0, rules.High: 0, rules.Emergency: 0}}
	start, _ := time.Parse(time.RFC3339, from)
	end, _ := time.Parse(time.RFC3339, to)
	closedMinutes := 0.0
	for _, c := range s.st.List() {
		if cabinet != "" && c.CabinetID != cabinet || status != "" && c.Status != status {
			continue
		}
		t := parseTime(c.TriggeredAt)
		if from != "" && t.Before(start) || to != "" && t.After(end) {
			continue
		}
		result.Total++
		result.RiskCounts[c.RiskLevel]++
		if c.RelatedCaseID != "" {
			result.Recurrence++
		}
		if c.Status == "已关闭" {
			result.ClosedCount++
			ct := parseTime(c.ClosedAt)
			if !ct.IsZero() && !t.IsZero() {
				closedMinutes += ct.Sub(t).Minutes()
			}
		} else {
			result.InProgress++
		}
	}
	if result.ClosedCount > 0 {
		result.AverageCloseMinutes = closedMinutes / float64(result.ClosedCount)
	}
	return result
}

func parseTime(v string) time.Time                { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func (s *Service) RecoveryDiagnostic() string     { return s.st.RecoveryDiagnostic() }
func (s *Service) RecoveryReport() map[string]any { return s.st.RecoveryReport() }
