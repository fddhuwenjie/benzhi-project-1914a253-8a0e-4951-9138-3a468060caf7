package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"microclimate/internal/rules"
	"microclimate/internal/store"
	"regexp"
	"strings"
	"time"
)

func NormalizeObservation(v string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(v), " "))
}
func AttachmentDigest(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}
func ValidateRecord(r store.InspectionRecord) error {
	if strings.TrimSpace(r.Observations) == "" {
		return errors.New("observations required")
	}
	if len(r.EvidenceRefs) == 0 {
		return errors.New("evidence reference required")
	}
	if !rules.ValidSnapshot(r.Readings) {
		return errors.New("readings out of range")
	}
	seen := map[string]bool{}
	for _, ref := range r.EvidenceRefs {
		if seen[ref] {
			return errors.New("duplicate evidence reference")
		}
		seen[ref] = true
	}
	return nil
}
func ValidateStrict(r store.InspectionRecord) error {
	if err := ValidateRecord(r); err != nil {
		return err
	}
	if strings.TrimSpace(r.MitigationActions) == "" {
		return errors.New("mitigation actions required")
	}
	for _, ref := range r.EvidenceRefs {
		if !regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`).MatchString(ref) {
			return errors.New("invalid evidence digest")
		}
	}
	return nil
}
func NormalizeRecord(r store.InspectionRecord) store.InspectionRecord {
	r.Observations = NormalizeObservation(r.Observations)
	r.MitigationActions = NormalizeObservation(r.MitigationActions)
	for i, ref := range r.EvidenceRefs {
		r.EvidenceRefs[i] = strings.ToLower(strings.TrimSpace(ref))
	}
	if r.Readings.TemperatureUnit == "" {
		r.Readings.TemperatureUnit = "C"
	}
	if r.Readings.HumidityUnit == "" {
		r.Readings.HumidityUnit = "%RH"
	}
	if r.Readings.CollectedAt == "" {
		r.Readings.CollectedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if r.ObservedAt == "" {
		r.ObservedAt = r.Readings.CollectedAt
	}
	if r.RelatedInspectionID != "" {
		r.RelatedInspectionID = strings.TrimSpace(r.RelatedInspectionID)
	}
	return r
}

func NormalizeReceipts(in []store.ChecklistReceipt, checklist []string) ([]store.ChecklistReceipt, error) {
	allowed := map[string]bool{}
	for _, item := range checklist {
		allowed[item] = true
	}
	seen := map[string]bool{}
	out := make([]store.ChecklistReceipt, 0, len(in))
	for _, v := range in {
		v.Item = NormalizeObservation(v.Item)
		v.Status = strings.TrimSpace(v.Status)
		v.Operator = strings.TrimSpace(v.Operator)
		if v.Item == "" || !allowed[v.Item] {
			return nil, errors.New("unknown checklist item")
		}
		if seen[v.Item] {
			return nil, errors.New("duplicate checklist receipt")
		}
		if v.Status == "" {
			v.Status = "completed"
		}
		if v.Status != "completed" && v.Status != "skipped" {
			return nil, errors.New("invalid checklist receipt status")
		}
		if v.Operator == "" {
			return nil, errors.New("checklist operator required")
		}
		if v.CompletedAt == "" {
			v.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if _, err := time.Parse(time.RFC3339, v.CompletedAt); err != nil {
			return nil, errors.New("invalid checklist completed_at")
		}
		seen[v.Item] = true
		out = append(out, v)
	}
	return out, nil
}

func ValidateEvidenceBindings(bindings []store.EvidenceBinding, inspection store.InspectionRecord) error {
	seen := map[string]bool{}
	refs := map[string]bool{}
	for _, ref := range inspection.EvidenceRefs {
		refs[strings.ToLower(strings.TrimSpace(ref))] = true
	}
	for _, b := range bindings {
		digest := strings.ToLower(strings.TrimSpace(b.Digest))
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
			return errors.New("invalid evidence digest")
		}
		if seen[digest] {
			return errors.New("duplicate evidence digest")
		}
		if len(refs) > 0 && !refs[digest] {
			return errors.New("evidence digest not referenced by inspection")
		}
		seen[digest] = true
		if b.SourceInspectionID != "" && b.SourceInspectionID != inspection.ID {
			return errors.New("evidence source mismatch")
		}
		if strings.TrimSpace(b.Observation) == "" || strings.TrimSpace(b.Mitigation) == "" {
			return errors.New("evidence binding excerpts required")
		}
	}
	return nil
}

func RequiredChecklist(level rules.RiskLevel) int {
	if level == rules.Emergency {
		return 5
	}
	if level == rules.High {
		return 3
	}
	return 1
}
func Missing(c *store.MicroclimateCase) []string {
	var m []string
	if len(c.Inspections) == 0 {
		m = append(m, "现场检查")
	}
	if len(c.Inspections) > 0 {
		last := c.Inspections[len(c.Inspections)-1]
		if len(last.EvidenceRefs) == 0 {
			m = append(m, "evidence_refs")
		}
		if strings.TrimSpace(last.MitigationActions) == "" {
			m = append(m, "mitigation_actions")
		}
	}
	return m
}
func ReviewComplete(r store.ReviewDecision) bool {
	return r.ReviewerID != "" && (r.Decision == "通过" || r.Decision == "退回") && strings.TrimSpace(r.Findings) != ""
}
