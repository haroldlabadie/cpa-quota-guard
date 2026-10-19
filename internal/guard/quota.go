package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func Number(v any) (float64, error) {
	var f float64
	var err error
	switch n := v.(type) {
	case json.Number:
		f, err = n.Float64()
	case float64:
		f = n
	case string:
		f, err = strconv.ParseFloat(n, 64)
	default:
		return 0, errors.New("missing/non-numeric quota")
	}
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("non-finite quota")
	}
	return f, nil
}
func ParseCodex(body map[string]any) ([]Window, error) {
	main, ok := body["rate_limit"].(map[string]any)
	if !ok {
		return nil, errors.New("missing Codex rate_limit")
	}
	type group struct {
		label, scope string
		limit        map[string]any
	}
	groups := []group{{"codex", "all", main}}
	if additional, exists := body["additional_rate_limits"]; exists && additional != nil {
		items, ok := additional.([]any)
		if !ok {
			return nil, errors.New("invalid additional rate limits")
		}
		for _, r := range items {
			m, ok := r.(map[string]any)
			if !ok {
				return nil, errors.New("invalid additional rate limit")
			}
			label, _ := m["limit_name"].(string)
			if label == "" {
				label, _ = m["metered_feature"].(string)
			}
			if label == "" {
				label = "additional"
			}
			l, ok := m["rate_limit"].(map[string]any)
			if !ok {
				l = m
			}
			groups = append(groups, group{label, label, l})
		}
	}
	out := []Window{}
	for _, g := range groups {
		for _, role := range []string{"primary_window", "secondary_window"} {
			raw := g.limit[role]
			if raw == nil {
				continue
			}
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("invalid Codex window")
			}
			dur, e1 := Number(m["limit_window_seconds"])
			used, e2 := Number(m["used_percent"])
			reset, e3 := Number(m["reset_at"])
			if e1 != nil || e2 != nil || e3 != nil || used < 0 || used > 100 || dur <= 0 || reset <= 0 || dur > 1e10 || reset > 1e13 {
				return nil, errors.New("invalid Codex quota")
			}
			duration := int64(dur) * 1000
			kind, id := "other", role
			switch duration {
			case FiveHourMS:
				kind, id = "five_hour", "five-hour"
			case WeekMS:
				kind, id = "weekly", "weekly"
			case 2592000000:
				id = "monthly"
			}
			if g.label != "codex" {
				id = g.label + ":" + id
			}
			out = append(out, Window{id, kind, g.scope, 100 - used, int64(reset) * 1000, duration})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty Codex windows")
	}
	return out, nil
}

var groupSlug = regexp.MustCompile(`[^a-z0-9]+`)

func first(m map[string]any, a, b string) any {
	if v := m[a]; v != nil {
		return v
	}
	return m[b]
}
func text(v any) string { s, _ := v.(string); return s }
func ParseAntigravity(body map[string]any, partial bool) ([]Window, error) {
	groups, ok := body["groups"].([]any)
	if !ok {
		return nil, errors.New("missing Antigravity groups")
	}
	out := []Window{}
	invalid := map[string]bool{}
	for _, r := range groups {
		g, ok := r.(map[string]any)
		if !ok {
			return nil, errors.New("invalid quota group")
		}
		label := strings.ToLower(text(first(g, "displayName", "display_name")))
		scope := ""
		if strings.Contains(label, "gemini") {
			scope = "gemini"
		} else if strings.Contains(label, "claude") || strings.Contains(label, "gpt") {
			scope = "third_party"
		}
		buckets, ok := g["buckets"].([]any)
		if g["buckets"] == nil {
			buckets = []any{}
			ok = true
		}
		if !ok {
			if partial && scope != "" {
				invalid[scope] = true
				continue
			}
			return nil, errors.New("invalid quota buckets")
		}
		for _, r := range buckets {
			b, ok := r.(map[string]any)
			if !ok {
				if partial && scope != "" {
					invalid[scope] = true
					continue
				}
				return nil, errors.New("invalid quota bucket")
			}
			id := text(first(b, "bucketId", "bucket_id"))
			bs := scope
			if strings.Contains(label+strings.ToLower(id), "gemini") {
				bs = "gemini"
			} else if strings.HasPrefix(id, "3p-") || strings.Contains(label, "claude") {
				bs = "third_party"
			}
			if bs == "" {
				continue
			}
			kind := ""
			switch strings.ToLower(text(b["window"])) {
			case "5h", "five_hour", "five-hour":
				kind = "five_hour"
			case "weekly", "week":
				kind = "weekly"
			}
			end, e1 := time.Parse(time.RFC3339Nano, text(first(b, "resetTime", "reset_time")))
			fraction := first(b, "remainingFraction", "remaining_fraction")
			if fraction == nil {
				fraction = float64(0)
			}
			f, e2 := Number(fraction)
			if kind == "" || id == "" || e1 != nil || e2 != nil || f < 0 || f > 1 {
				if partial {
					invalid[bs] = true
					continue
				}
				return nil, errors.New("invalid Antigravity window")
			}
			duration := FiveHourMS
			if kind == "weekly" {
				duration = WeekMS
			}
			gid := strings.Trim(groupSlug.ReplaceAllString(label, "-"), "-")
			out = append(out, Window{gid + ":" + id, kind, bs, f * 100, end.UnixMilli(), duration})
		}
	}
	complete := map[string]map[string]bool{}
	for _, w := range out {
		if complete[w.Scope] == nil {
			complete[w.Scope] = map[string]bool{}
		}
		complete[w.Scope][w.Kind] = true
	}
	for _, scope := range []string{"gemini", "third_party"} {
		if invalid[scope] || !complete[scope]["five_hour"] || !complete[scope]["weekly"] {
			if !partial && (scope == "gemini" || complete[scope] != nil) {
				return nil, fmt.Errorf("incomplete Antigravity windows: %s", scope)
			}
			filtered := out[:0]
			for _, w := range out {
				if w.Scope != scope {
					filtered = append(filtered, w)
				}
			}
			out = filtered
		}
	}
	if len(out) == 0 {
		return nil, errors.New("empty Antigravity windows")
	}
	return out, nil
}
func Reasons(sample *Sample, provider, scope string, rule Rule, now int64) []string {
	if sample == nil || sample.ObservedMS > now+1000 || now-sample.ObservedMS > 90000 {
		return []string{"unknown/stale quota"}
	}
	kinds := map[string]bool{}
	windows := []Window{}
	for _, w := range sample.Windows {
		if (provider == "codex" || w.Scope == scope) && (w.Kind == "five_hour" || w.Kind == "weekly") {
			windows = append(windows, w)
			if provider != "codex" || w.Scope == "all" {
				kinds[w.Kind] = true
			}
		}
	}
	if !kinds["five_hour"] || !kinds["weekly"] {
		return []string{"unknown/incomplete quota: " + scope}
	}
	reasons := []string{}
	for _, w := range windows {
		threshold := rule.ReserveFiveHour
		if w.Kind == "weekly" {
			threshold = rule.ReserveWeekly
		}
		if math.IsNaN(w.Remaining) || math.IsInf(w.Remaining, 0) || w.Remaining < 0 || w.Remaining > 100 || w.EndMS <= 0 {
			return []string{"unknown/invalid quota"}
		}
		if w.Remaining <= threshold+1e-6 {
			reasons = append(reasons, fmt.Sprintf("%s/%s remaining=%.2f%%", w.Scope, w.Kind, w.Remaining))
		} else if w.EndMS <= now {
			return []string{"unknown/expired quota evidence"}
		}
	}
	return reasons
}
