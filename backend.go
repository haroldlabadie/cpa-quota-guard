package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
)

type hostCaller func(string, any, any) error

var accountInventory = func(b *backend, ctx context.Context) ([]guard.AccountOption, error) { return b.Accounts(ctx) }

type backend struct {
	host      hostCaller
	base, key string
	client    *http.Client
}

func newBackend(host hostCaller, base, key string) *backend {
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 8 * time.Second}).DialContext, ResponseHeaderTimeout: 12 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &backend{host: host, base: strings.TrimRight(base, "/"), key: key, client: &http.Client{Timeout: 18 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (b *backend) List(ctx context.Context) ([]guard.Entry, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var out struct {
		Files []guard.Entry `json:"files"`
	}
	if err := b.host("host.auth.list", map[string]any{}, &out); err != nil {
		return nil, err
	}
	if out.Files == nil {
		return nil, errors.New("missing inventory")
	}
	rows := []guard.Entry{}
	seen := map[string]bool{}
	for _, e := range out.Files {
		if e.Provider == "" {
			e.Provider = e.Type
		}
		if e.Provider != "codex" && e.Provider != "antigravity" {
			continue
		}
		if e.RuntimeOnly || e.Source != "file" {
			continue
		}
		if e.Name == "" || strings.ContainsAny(e.Name, "/\\") || e.AuthIndex == "" || seen[e.Name] {
			return nil, errors.New("ambiguous credential inventory")
		}
		seen[e.Name] = true
		rows = append(rows, e)
	}
	return rows, nil
}

// Accounts is a read-only UI projection of the host credential inventory.
// It never reads raw credential JSON and therefore cannot expose tokens.
func (b *backend) Accounts(ctx context.Context) ([]guard.AccountOption, error) {
	entries, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]guard.AccountOption, 0, len(entries))
	for _, entry := range entries {
		out = append(out, guard.AccountOption{Name: entry.Name, Provider: entry.Provider, Email: entry.Email, Disabled: entry.Disabled})
	}
	return out, nil
}
func (b *backend) Read(ctx context.Context, e guard.Entry) (guard.Account, error) {
	var out struct {
		AuthIndex string          `json:"auth_index"`
		Name      string          `json:"name"`
		Path      string          `json:"path"`
		JSON      json.RawMessage `json:"json"`
	}
	if ctx.Err() != nil {
		return guard.Account{}, ctx.Err()
	}
	if err := b.host("host.auth.get", map[string]string{"auth_index": e.AuthIndex}, &out); err != nil {
		return guard.Account{}, err
	}
	if out.AuthIndex != e.AuthIndex || out.Name != e.Name || out.Path == "" {
		return guard.Account{}, errors.New("credential binding mismatch")
	}
	fi, err := os.Stat(out.Path)
	if err != nil {
		return guard.Account{}, errors.New("credential stat failed")
	}
	// Bind the parsed JSON to a stable filesystem revision during token refresh.
	path := out.Path
	if err = b.host("host.auth.get", map[string]string{"auth_index": e.AuthIndex}, &out); err != nil {
		return guard.Account{}, err
	}
	afterStat, err := os.Stat(path)
	if err != nil || out.Path != path || out.Name != e.Name || out.AuthIndex != e.AuthIndex || afterStat.ModTime() != fi.ModTime() || afterStat.Size() != fi.Size() {
		return guard.Account{}, errors.New("credential changed during read")
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(out.JSON))
	decoder.UseNumber()
	if err = decoder.Decode(&raw); err != nil {
		return guard.Account{}, errors.New("invalid credential JSON")
	}
	out.JSON = nil
	var runtime struct {
		Auth guard.Entry `json:"auth"`
	}
	if err = b.host("host.auth.get_runtime", map[string]string{"auth_index": e.AuthIndex}, &runtime); err != nil {
		return guard.Account{}, err
	}
	if runtime.Auth.AuthIndex != e.AuthIndex || runtime.Auth.Name != e.Name || runtime.Auth.Provider != e.Provider {
		return guard.Account{}, errors.New("credential runtime identity mismatch")
	}
	e.Disabled = runtime.Auth.Disabled
	e.Email = runtime.Auth.Email
	a, err := guard.MakeAccount(e, raw, fi.ModTime().UnixNano())
	raw = nil
	if err != nil {
		return a, err
	}
	if e.Disabled != a.Snapshot.Protected.Disabled {
		return a, errors.New("credential runtime/disk disabled mismatch")
	}
	return a, nil
}
func (b *backend) call(ctx context.Context, path string, payload any, method string, out any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return errors.New("encode HTTP request")
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid HTTP target")
	}
	req.Header.Set("Authorization", "Bearer "+b.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cpa-quota-guard/"+pluginVersion)
	response, err := b.client.Do(req)
	if err != nil {
		return errors.New("management transport failure")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("management HTTP %d", response.StatusCode)
	}
	bytes, err := io.ReadAll(io.LimitReader(response.Body, guard.MaxFile+1))
	if err != nil || len(bytes) > guard.MaxFile {
		return errors.New("management response size/read failure")
	}
	if out == nil {
		return nil
	}
	d := json.NewDecoder(strings.NewReader(string(bytes)))
	d.UseNumber()
	if err = d.Decode(out); err != nil {
		return errors.New("management JSON failure")
	}
	return nil
}
func (b *backend) Patch(ctx context.Context, a guard.Account, fields map[string]any) error {
	body := map[string]any{"name": a.Entry.Name}
	for k, v := range fields {
		body[k] = v
	}
	return b.call(ctx, "/v0/management/auth-files/fields", body, http.MethodPatch, nil)
}
func (b *backend) upstream(ctx context.Context, a guard.Account, target, method string, data any) (map[string]any, error) {
	headers := map[string]string{"Authorization": "Bearer $TOKEN$", "Content-Type": "application/json"}
	if a.Entry.Provider == "codex" {
		if a.AccountID == "" {
			return nil, errors.New("missing account_id")
		}
		headers["Chatgpt-Account-Id"] = a.AccountID
		headers["User-Agent"] = "codex_cli_rs/0.116.0"
	} else {
		headers["User-Agent"] = "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)"
	}
	request := map[string]any{"auth_index": a.Entry.AuthIndex, "method": method, "url": target, "header": headers}
	if data != nil {
		r, _ := json.Marshal(data)
		request["data"] = string(r)
	}
	var out struct {
		Status int    `json:"status_code"`
		Body   string `json:"body"`
	}
	if err := b.call(ctx, "/v0/management/api-call", request, http.MethodPost, &out); err != nil {
		return nil, err
	}
	if out.Status != 200 {
		return nil, fmt.Errorf("quota HTTP %d", out.Status)
	}
	if len(out.Body) > guard.MaxFile {
		return nil, errors.New("quota response too large")
	}
	var body map[string]any
	d := json.NewDecoder(strings.NewReader(out.Body))
	d.UseNumber()
	if err := d.Decode(&body); err != nil {
		return nil, errors.New("quota JSON failure")
	}
	return body, nil
}
func (b *backend) Probe(ctx context.Context, a guard.Account) (guard.Sample, error) {
	s := guard.Sample{Windows: []guard.Window{}, Sources: map[string]string{}}
	if a.Entry.Provider == "codex" {
		body, err := b.upstream(ctx, a, "https://chatgpt.com/backend-api/wham/usage", "GET", nil)
		if err != nil {
			return s, err
		}
		s.Windows, err = guard.ParseCodex(body)
		s.Plan, _ = body["plan_type"].(string)
		s.ObservedMS = time.Now().UnixMilli()
		return s, err
	}
	if a.ProjectID == "" {
		return s, errors.New("missing project_id")
	}
	found := map[string]bool{}
	for _, target := range []string{"https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"} {
		body, err := b.upstream(ctx, a, target, "POST", map[string]string{"project": a.ProjectID})
		if err != nil {
			continue
		}
		windows, err := guard.ParseAntigravity(body, true)
		if err != nil {
			continue
		}
		for _, scope := range []string{"gemini", "third_party"} {
			if found[scope] {
				continue
			}
			rows := []guard.Window{}
			for _, w := range windows {
				if w.Scope == scope {
					rows = append(rows, w)
				}
			}
			if len(rows) > 0 {
				s.Windows = append(s.Windows, rows...)
				s.Sources[scope] = target
				found[scope] = true
			}
		}
		if len(found) == 2 {
			break
		}
	}
	if len(s.Windows) == 0 {
		return s, errors.New("quota unavailable")
	}
	s.ObservedMS = time.Now().UnixMilli()
	return s, nil
}
func validateBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("invalid CPAMP base URL")
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	if ip != nil {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return errors.New("CPAMP must use loopback/private network")
		}
	} else if strings.Contains(h, ".") && !strings.HasSuffix(h, ".internal") {
		return errors.New("CPAMP must use Compose DNS or internal network")
	}
	return nil
}
func (b *backend) Publish(ctx context.Context, a guard.Account, s guard.Sample) error {
	windows := []map[string]any{}
	for _, w := range s.Windows {
		scope := w.Scope
		if scope == "all" {
			scope = "codex_main"
		} else if scope == "third_party" {
			scope = "claude_gpt"
		}
		row := map[string]any{"provider_window_id": w.ID, "window_kind": w.Kind, "window_mode": "fixed", "model_scope_kind": "family", "model_scope_key": scope, "source": "api_query", "source_observation_id": fmt.Sprintf("guard-plugin:%s:%d", a.Entry.AuthIndex, s.ObservedMS), "observed_at_ms": s.ObservedMS, "boundary_accuracy": "derived", "cycle_end_ms": w.EndMS, "duration_seconds": w.DurationMS / 1000, "used_percent": 100 - w.Remaining, "remaining_percent": w.Remaining}
		if w.Kind != "five_hour" || w.Remaining < 100 {
			row["cycle_start_ms"] = w.EndMS - w.DurationMS
		} else {
			row["boundary_accuracy"] = "unknown"
		}
		if s.Plan != "" {
			row["plan_type"] = s.Plan
		}
		windows = append(windows, row)
	}
	account := map[string]any{"auth_file_snapshot": a.Entry.Name, "auth_provider_snapshot": a.Entry.Provider, "account_snapshot": a.Entry.Email, "auth_index": a.Entry.AuthIndex, "auth_account_id_snapshot": a.AccountID, "auth_project_id_snapshot": a.ProjectID}
	inventoryScope := "antigravity:quota-windows"
	if a.Entry.Provider == "codex" {
		inventoryScope = "codex:rate-limits"
	}
	body := map[string]any{"entries": []any{map[string]any{"row_key": a.Entry.Name, "provider": a.Entry.Provider, "account": account, "observation": map[string]any{"source": "api_query", "source_observation_id": fmt.Sprintf("guard-plugin:%s:%d", a.Entry.AuthIndex, s.ObservedMS), "observed_at_ms": s.ObservedMS, "inventory_scope_key": inventoryScope, "inventory_mode": "partial"}, "windows": windows}}}
	return b.call(ctx, "/v0/management/quota-snapshots", body, http.MethodPost, nil)
}
