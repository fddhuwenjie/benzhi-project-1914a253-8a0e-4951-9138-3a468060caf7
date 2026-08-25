package rules

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const MaxDurationMinutes = 7 * 24 * 60

var ErrInvalidSnapshot = errors.New("invalid sensor snapshot")

type SensorSnapshot struct {
	Temperature     float64 `json:"temperature"`
	Humidity        float64 `json:"humidity"`
	DurationMinutes int     `json:"duration_minutes"`
	CollectedAt     string  `json:"collected_at,omitempty"`
	TemperatureUnit string  `json:"temperature_unit,omitempty"`
	HumidityUnit    string  `json:"humidity_unit,omitempty"`
}

type QualityAssessment struct {
	Score float64  `json:"score"`
	Flags []string `json:"quality_flags"`
}

// AssessQuality evaluates trustworthiness separately from environmental risk.
func AssessQuality(s SensorSnapshot, calibrationAt string, previous *SensorSnapshot) QualityAssessment {
	score := 100.0
	flags := []string{}
	if s.CollectedAt == "" {
		score -= 20
		flags = append(flags, "missing_collection_time")
	} else if t, err := time.Parse(time.RFC3339, s.CollectedAt); err != nil {
		score -= 25
		flags = append(flags, "invalid_collection_time")
	} else if time.Since(t) > 24*time.Hour {
		score -= 25
		flags = append(flags, "stale_reading")
	}
	if calibrationAt == "" {
		score -= 10
		flags = append(flags, "calibration_missing")
	} else if t, err := time.Parse(time.RFC3339, calibrationAt); err != nil {
		score -= 20
		flags = append(flags, "invalid_calibration_time")
	} else if time.Since(t) > 365*24*time.Hour {
		score -= 30
		flags = append(flags, "calibration_expired")
	}
	if previous != nil {
		if math.Abs(previous.Temperature-s.Temperature) > 10 || math.Abs(previous.Humidity-s.Humidity) > 30 {
			score -= 30
			flags = append(flags, "adjacent_jump")
		}
	}
	if score < 0 {
		score = 0
	}
	return QualityAssessment{Score: score, Flags: flags}
}

// TrendDirection returns improvement, deterioration, or unchanged risk direction.
func TrendDirection(from, to RiskLevel) string {
	if Rank(to) > Rank(from) {
		return "恶化"
	}
	if Rank(to) < Rank(from) {
		return "改善"
	}
	return "持平"
}

func Oscillation(recent []string) bool {
	reversals := 0
	last := ""
	for _, d := range recent {
		if d != "改善" && d != "恶化" {
			continue
		}
		if last != "" && d != last {
			reversals++
		}
		last = d
	}
	return reversals >= 2
}

func ValidSnapshot(s SensorSnapshot) bool {
	return !math.IsNaN(s.Temperature) && !math.IsInf(s.Temperature, 0) &&
		!math.IsNaN(s.Humidity) && !math.IsInf(s.Humidity, 0) &&
		s.Temperature >= -50 && s.Temperature <= 80 && s.Humidity >= 0 && s.Humidity <= 100 &&
		s.DurationMinutes >= 0 && s.DurationMinutes <= MaxDurationMinutes
}

// NormalizeSnapshot converts supported units and validates the collection window.
func NormalizeSnapshot(s SensorSnapshot, current time.Time, requireDuration bool) (SensorSnapshot, error) {
	tempUnit := strings.ToUpper(strings.TrimSpace(s.TemperatureUnit))
	switch tempUnit {
	case "", "C", "°C", "℃", "CELSIUS":
		s.TemperatureUnit = "C"
	case "F", "°F", "FAHRENHEIT":
		s.Temperature = (s.Temperature - 32) * 5 / 9
		s.TemperatureUnit = "C"
	default:
		return s, fmt.Errorf("%w: unsupported temperature_unit", ErrInvalidSnapshot)
	}
	humidityUnit := strings.ToUpper(strings.TrimSpace(s.HumidityUnit))
	switch humidityUnit {
	case "", "%", "%RH", "RH":
		s.HumidityUnit = "%RH"
	default:
		return s, fmt.Errorf("%w: unsupported humidity_unit", ErrInvalidSnapshot)
	}
	if s.CollectedAt == "" {
		s.CollectedAt = current.UTC().Format(time.RFC3339Nano)
	} else {
		collected, err := time.Parse(time.RFC3339, s.CollectedAt)
		if err != nil {
			return s, fmt.Errorf("%w: collected_at must be RFC3339", ErrInvalidSnapshot)
		}
		if collected.After(current.Add(5 * time.Minute)) {
			return s, fmt.Errorf("%w: collected_at is in the future", ErrInvalidSnapshot)
		}
		if collected.Before(current.Add(-30 * 24 * time.Hour)) {
			return s, fmt.Errorf("%w: collected_at is outside the 30 day window", ErrInvalidSnapshot)
		}
		s.CollectedAt = collected.UTC().Format(time.RFC3339Nano)
	}
	if !ValidSnapshot(s) || (requireDuration && s.DurationMinutes <= 0) {
		return s, fmt.Errorf("%w: readings or duration out of range", ErrInvalidSnapshot)
	}
	return s, nil
}
func Rank(v RiskLevel) int {
	switch v {
	case Low:
		return 1
	case Medium:
		return 2
	case High:
		return 3
	case Emergency:
		return 4
	}
	return 0
}

type RiskLevel string

const (
	Low       RiskLevel = "低"
	Medium    RiskLevel = "中"
	High      RiskLevel = "高"
	Emergency RiskLevel = "紧急"
)

type Assessment struct {
	Level     RiskLevel
	Reasons   []string
	Checklist []string
	Basis     RuleBasis
}

type RuleBasis struct {
	Version           string  `json:"version"`
	TemperatureLow    float64 `json:"temperature_low"`
	TemperatureHigh   float64 `json:"temperature_high"`
	HumidityLow       float64 `json:"humidity_low"`
	HumidityHigh      float64 `json:"humidity_high"`
	DurationMedium    int     `json:"duration_medium"`
	DurationHigh      int     `json:"duration_high"`
	SensitivityFactor float64 `json:"sensitivity_factor"`
}

var ruleBases = map[string]RuleBasis{
	"v1": {Version: "v1", TemperatureLow: 16, TemperatureHigh: 26, HumidityLow: 40, HumidityHigh: 60, DurationMedium: 30, DurationHigh: 180, SensitivityFactor: 1},
	"v2": {Version: "v2", TemperatureLow: 17, TemperatureHigh: 25, HumidityLow: 42, HumidityHigh: 58, DurationMedium: 20, DurationHigh: 120, SensitivityFactor: 1.2},
}

func RuleVersions() []string { return []string{"v1", "v2"} }
func Basis(version string) (RuleBasis, bool) {
	if version == "" {
		version = "v1"
	}
	b, ok := ruleBases[version]
	return b, ok
}

func Assess(s SensorSnapshot, sensitivity string) Assessment {
	return AssessVersion(s, sensitivity, "v1")
}

func AssessVersion(s SensorSnapshot, sensitivity, version string) Assessment {
	b, ok := Basis(version)
	if !ok {
		return Assessment{}
	}
	deviation := 0.0
	if s.Temperature < b.TemperatureLow {
		deviation += b.TemperatureLow - s.Temperature
	} else if s.Temperature > b.TemperatureHigh {
		deviation += s.Temperature - b.TemperatureHigh
	}
	if s.Humidity < b.HumidityLow {
		deviation += (b.HumidityLow - s.Humidity) / 5
	} else if s.Humidity > b.HumidityHigh {
		deviation += (s.Humidity - b.HumidityHigh) / 5
	}
	level := Low
	if deviation >= 10 || s.DurationMinutes >= b.DurationHigh {
		level = High
	}
	if deviation >= 18 || s.DurationMinutes >= b.DurationHigh*2 {
		level = Emergency
	}
	if level == Low && (deviation >= 4 || s.DurationMinutes >= b.DurationMedium) {
		level = Medium
	}
	if sensitivity == "高" && level == Medium {
		level = High
	}
	if sensitivity == "高" && level == High && s.DurationMinutes >= 120 {
		level = Emergency
	}
	reasons := []string{fmt.Sprintf("温度 %.1f℃、湿度 %.1f%%RH、持续 %d 分钟", s.Temperature, s.Humidity, s.DurationMinutes)}
	reasons = append(reasons, "展品敏感度："+sensitivity)
	reasons = append(reasons, "偏离阈值评分："+fmt.Sprintf("%.1f", deviation))
	return Assessment{Level: level, Reasons: reasons, Checklist: Checklist(level, sensitivity), Basis: b}
}

func Checklist(level RiskLevel, sensitivity string) []string {
	items := []string{"确认传感器状态与校准有效性", "核对展柜门封、照明和空调运行情况", "记录现场温湿度并拍摄影像证据"}
	if level == Medium || level == High || level == Emergency {
		items = append(items, "检查展柜周边气流、渗漏和凝露迹象")
	}
	if level == High || level == Emergency {
		items = append(items, "实施临时调控并每30分钟复测")
	}
	if level == Emergency {
		items = append(items, "立即通知文保专家并准备展品转移预案")
	}
	if sensitivity == "高" {
		items = append(items, "核查展品材质敏感部位及既往病害")
	}
	return items
}

// StabilityTarget returns the number of consecutive qualified retests required.
func StabilityTarget(level RiskLevel) int {
	switch level {
	case Emergency:
		return 5
	case High:
		return 3
	default:
		return 1
	}
}

func QualifiedRetest(s SensorSnapshot, version string) bool {
	b, ok := Basis(version)
	if !ok {
		return false
	}
	return s.Temperature >= b.TemperatureLow && s.Temperature <= b.TemperatureHigh && s.Humidity >= b.HumidityLow && s.Humidity <= b.HumidityHigh
}

func ResponseWindow(level RiskLevel) time.Duration {
	switch level {
	case Emergency:
		return 30 * time.Minute
	case High:
		return time.Hour
	case Medium:
		return 4 * time.Hour
	default:
		return 8 * time.Hour
	}
}
