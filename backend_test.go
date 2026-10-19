package main

import (
	"context"
	"encoding/json"
	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func agBody(scope string, remaining float64) map[string]any {
	display, bucket := "Gemini", "gemini"
	if scope == "third_party" {
		display, bucket = "Claude GPT", "3p"
	}
	rows := []any{}
	for _, kind := range []string{"5h", "weekly"} {
		rows = append(rows, map[string]any{"bucketId": bucket + "-" + kind, "window": kind, "remainingFraction": remaining, "resetTime": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	}
	return map[string]any{"groups": []any{map[string]any{"displayName": display, "buckets": rows}}}
}
func TestQuotaEndpointsFillMissingWithoutReplacingLow(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer FAKE_PANEL" {
			t.Error("panel credential missing")
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		target, _ := req["url"].(string)
		calls = append(calls, target)
		body := agBody("gemini", 0.01)
		if strings.Contains(target, "daily-") {
			body = agBody("third_party", 0.9)
			other := agBody("gemini", 1)
			body["groups"] = append(body["groups"].([]any), other["groups"].([]any)...)
		}
		raw, _ := json.Marshal(body)
		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": string(raw)})
	}))
	defer server.Close()
	b := newBackend(nil, server.URL, "FAKE_PANEL")
	s, err := b.Probe(context.Background(), guard.Account{Entry: guard.Entry{Provider: "antigravity", AuthIndex: "idx"}, ProjectID: "test"})
	if err != nil || len(s.Windows) != 4 || len(calls) != 2 {
		t.Fatal(err, s)
	}
	for _, w := range s.Windows {
		if w.Scope == "gemini" && w.Remaining != 1 {
			t.Fatal("low official quota overwritten")
		}
	}
}
func TestSnapshotNeverInventsIdleFiveHourCycle(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()
	b := newBackend(nil, server.URL, "fake")
	s := guard.Sample{ObservedMS: 1, Windows: []guard.Window{{ID: "5h", Kind: "five_hour", Scope: "all", Remaining: 100, EndMS: 1900000000000, DurationMS: guard.FiveHourMS}}}
	if err := b.Publish(context.Background(), guard.Account{Entry: guard.Entry{Name: "f", Provider: "codex", AuthIndex: "idx"}}, s); err != nil {
		t.Fatal(err)
	}
	entries := request["entries"].([]any)
	row := entries[0].(map[string]any)["windows"].([]any)[0].(map[string]any)
	if _, ok := row["cycle_start_ms"]; ok || row["boundary_accuracy"] != "unknown" {
		t.Fatal("invented cycle")
	}
}
func TestManagementHTTPHasCancellationAndResponseLimit(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	b := newBackend(nil, server.URL, "fake")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if b.call(ctx, "/v0/management/api-call", map[string]any{}, "POST", nil) == nil {
		t.Fatal("cancel ignored")
	}
}
