package main

import (
	"context"
	"encoding/json"
	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

const managementBase = "/v0/management/plugins/cpa-quota-guard"

type managementRequest struct {
	Method  string      `json:"Method"`
	Path    string      `json:"Path"`
	Headers http.Header `json:"Headers"`
	Query   url.Values  `json:"Query"`
	Body    []byte      `json:"Body"`
}
type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}

func managementRegistration() any {
	return map[string]any{"routes": []map[string]string{{"Method": "GET", "Path": managementBase + "/status"}, {"Method": "GET", "Path": managementBase + "/settings"}, {"Method": "PUT", "Path": managementBase + "/settings"}, {"Method": "GET", "Path": managementBase + "/observations"}, {"Method": "GET", "Path": managementBase + "/accounts"}}, "resources": []map[string]string{{"Path": "/settings", "Menu": "额度保护", "Description": "独立额度保护与人工所有权状态"}}}
}
func managementJSON(code int, v any, etag string) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	h := http.Header{"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}
	if etag != "" {
		h.Set("ETag", etag)
	}
	return okEnvelope(managementResponse{code, h, b})
}
func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if json.Unmarshal(raw, &req) != nil {
		return managementJSON(400, map[string]string{"error": "invalid request"}, "")
	}
	if (req.Method == "GET" || req.Method == "HEAD") && req.Path == "/v0/resource/plugins/cpa-quota-guard/settings" {
		return settingsPage(req.Method == "HEAD")
	}
	i := currentInstance()
	if i == nil {
		return managementJSON(503, map[string]string{"error": "not configured"}, "")
	}
	switch {
	case req.Method == "GET" && req.Path == managementBase+"/status":
		state, tick, last, running := i.engine.View()
		i.mu.Lock()
		cfg, revision, stopped := i.cfg, i.revision, i.stopped
		i.mu.Unlock()
		return managementJSON(200, map[string]any{"plugin": pluginID, "version": pluginVersion, "schema": 1, "mode": cfg.Mode, "enabled": cfg.Enabled, "running": running, "quiesced": stopped, "last_tick_ms": tick, "last_error": last, "revision": revision, "account_count": len(state.Observations), "stop_behavior": "retains existing credential restrictions"}, "")
	case req.Method == "GET" && req.Path == managementBase+"/observations":
		state, _, _, _ := i.engine.View()
		return managementJSON(200, map[string]any{"schema": 1, "observations": state.Observations}, "")
	case req.Method == "GET" && req.Path == managementBase+"/accounts":
		entries, err := accountInventory(newBackend(hostCall, i.cfg.CPAMPURL, ""), context.Background())
		if err != nil {
			return managementJSON(503, map[string]string{"error": "credential inventory unavailable"}, "")
		}
		return managementJSON(200, map[string]any{"schema": 1, "accounts": entries}, "")
	case req.Method == "GET" && req.Path == managementBase+"/settings":
		i.mu.Lock()
		defer i.mu.Unlock()
		return managementJSON(200, map[string]any{"settings": i.settings, "revision": i.revision}, `"`+i.revision+`"`)
	case req.Method == "PUT" && req.Path == managementBase+"/settings":
		i.mu.Lock()
		defer i.mu.Unlock()
		if i.stopped {
			return managementJSON(409, map[string]string{"error": "plugin quiesced"}, "")
		}
		tag := strings.TrimSpace(req.Headers.Get("If-Match"))
		if tag == "" {
			return managementJSON(428, map[string]string{"error": "If-Match required"}, "")
		}
		if tag != `"`+i.revision+`"` {
			return managementJSON(412, map[string]string{"error": "settings changed; reload"}, "")
		}
		var s guard.Settings
		if guard.StrictJSON(req.Body, &s) != nil {
			return managementJSON(400, map[string]string{"error": "invalid settings JSON"}, "")
		}
		if err := guard.ValidateSettings(s); err != nil {
			return managementJSON(400, map[string]string{"error": err.Error()}, "")
		}
		if err := guard.AtomicJSON(filepath.Join(i.cfg.DataDir, "settings.json"), s); err != nil {
			return managementJSON(500, map[string]string{"error": "settings persistence failed"}, "")
		}
		i.settings = s
		i.revision = guard.Digest(s)
		i.engine.SetSettings(s)
		return managementJSON(200, map[string]any{"settings": s, "revision": i.revision}, `"`+i.revision+`"`)
	default:
		return managementJSON(404, map[string]string{"error": "route not found"}, "")
	}
}
