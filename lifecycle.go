package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
	"gopkg.in/yaml.v3"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const pluginVersion = "0.2.0"
const pluginID = "cpa-quota-guard"

type bootstrap struct {
	Version      string `yaml:"version,omitempty"`
	Priority     int    `yaml:"priority"`
	Store        any    `yaml:"store,omitempty"`
	Enabled      bool   `yaml:"enabled"`
	Mode         string `yaml:"mode"`
	DataDir      string `yaml:"data_dir"`
	SecretFile   string `yaml:"secret_file"`
	CPAMPURL     string `yaml:"cpamp_url"`
	CheckSeconds int    `yaml:"check_seconds"`
	ProbeSeconds int    `yaml:"probe_seconds"`
}
type instance struct {
	cfg      bootstrap
	engine   *guard.Engine
	cancel   context.CancelFunc
	done     chan struct{}
	lock     *os.File
	settings guard.Settings
	revision string
	mu       sync.Mutex
	stopped  bool
}

var current struct {
	sync.RWMutex
	inst *instance
}
var lifecycleMu sync.Mutex

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if err := configure(raw); err != nil {
			return nil, err
		}
		return okEnvelope(map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "额度保护", "Version": pluginVersion, "Author": "Harold Labadie", "GitHubRepository": "https://github.com/haroldlabadie/cpa-quota-guard", "ConfigFields": []map[string]string{{"Name": "enabled", "Type": "boolean", "Description": "Enable quota automation"}, {"Name": "mode", "Type": "string", "Description": "observe or apply"}, {"Name": "data_dir", "Type": "string", "Description": "Private persistent data directory"}, {"Name": "secret_file", "Type": "string", "Description": "CPAMP management credential file"}, {"Name": "cpamp_url", "Type": "string", "Description": "Private CPAMP base URL"}}}, "capabilities": map[string]bool{"management_api": true}})
	case "plugin.quiesce":
		stopCurrent()
		return okEnvelope(map[string]any{})
	case "management.register":
		return okEnvelope(managementRegistration())
	case "management.handle":
		return handleManagement(raw)
	default:
		return nil, errors.New("unsupported plugin method")
	}
}
func configure(raw []byte) error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	var req struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.SchemaVersion < 6 {
		return errors.New("CPA RPC schema 6 required")
	}
	cfg := bootstrap{Mode: "observe", CheckSeconds: 20, ProbeSeconds: 20}
	d := yaml.NewDecoder(bytes.NewReader(req.ConfigYAML))
	d.KnownFields(true)
	if err := d.Decode(&cfg); err != nil {
		return errors.New("invalid bootstrap YAML")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("multiple YAML documents")
	}
	if cfg.Mode != "observe" && cfg.Mode != "apply" {
		return errors.New("mode must be observe or apply")
	}
	if !filepath.IsAbs(cfg.DataDir) || !filepath.IsAbs(cfg.SecretFile) || cfg.CheckSeconds < 5 || cfg.CheckSeconds > 300 || cfg.ProbeSeconds < cfg.CheckSeconds || cfg.ProbeSeconds > 90 {
		return errors.New("invalid bootstrap paths/intervals")
	}
	if err := validateBase(cfg.CPAMPURL); err != nil {
		return err
	}
	fi, err := os.Lstat(cfg.SecretFile)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return errors.New("secret_file must be a private regular 0600 file")
	}
	keyBytes, err := os.ReadFile(cfg.SecretFile)
	if err != nil || len(keyBytes) > 65536 || len(strings.TrimSpace(string(keyBytes))) == 0 {
		return errors.New("secret_file unavailable")
	}
	sig, err := os.Stat(cfg.SecretFile)
	if err != nil || sig.Size() != fi.Size() || sig.ModTime() != fi.ModTime() {
		return errors.New("secret_file changed during read")
	}
	if err = os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return errors.New("data directory unavailable")
	}
	fi, err = os.Lstat(cfg.DataDir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&0077 != 0 {
		return errors.New("data directory must be private 0700")
	}
	settings := guard.Settings{Schema: 1, Rules: []guard.Rule{}}
	settingsPath := filepath.Join(cfg.DataDir, "settings.json")
	sraw, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		if cfg.Mode == "apply" {
			return errors.New("apply mode requires an explicit settings.json whitelist")
		}
		if err = guard.AtomicJSON(settingsPath, settings); err != nil {
			return errors.New("settings initialization failed")
		}
	} else if err != nil || guard.StrictJSON(sraw, &settings) != nil {
		return errors.New("settings invalid")
	}
	if err = guard.ValidateSettings(settings); err != nil {
		return err
	}
	// Decode/validate first; then join the old instance before acquiring this lock.
	stopCurrentLocked()
	lock, err := lockFile(filepath.Join(cfg.DataDir, "worker.lock"))
	if err != nil {
		return errors.New("another guard instance owns worker.lock")
	}
	statePath := filepath.Join(cfg.DataDir, "state.json")
	if cfg.Mode == "apply" {
		if fi, statErr := os.Lstat(statePath); statErr != nil || !fi.Mode().IsRegular() {
			unlockFile(lock)
			return errors.New("apply mode requires an explicitly initialized or migrated state.json")
		}
	}
	state, upgrade, err := guard.LoadStateUpgrade(statePath)
	if err != nil {
		unlockFile(lock)
		return errors.New("state invalid; automation stopped")
	}
	if upgrade && cfg.Mode == "apply" {
		// Loading schema 1 already validates and upgrades the in-memory model.
		// Observe stays read-only; apply owns worker.lock and can atomically
		// persist the explicit schema upgrade before starting the worker.
		if err = guard.AtomicJSON(statePath, state); err != nil {
			unlockFile(lock)
			return errors.New("state upgrade persistence failed; automation stopped")
		}
	}
	engine := guard.NewEngine(newBackend(hostCall, cfg.CPAMPURL, strings.TrimSpace(string(keyBytes))), state, settings, filepath.Join(cfg.DataDir, "state.json"), cfg.Mode == "apply")
	keyBytes = nil
	ctx, cancel := context.WithCancel(context.Background())
	inst := &instance{cfg: cfg, engine: engine, cancel: cancel, done: make(chan struct{}), lock: lock, settings: settings, revision: guard.Digest(settings)}
	current.Lock()
	current.inst = inst
	current.Unlock()
	go func() {
		defer close(inst.done)
		if cfg.Enabled {
			engine.Run(ctx, time.Duration(cfg.CheckSeconds)*time.Second, time.Duration(cfg.ProbeSeconds)*time.Second)
		}
	}()
	return nil
}
func stopCurrent() { lifecycleMu.Lock(); defer lifecycleMu.Unlock(); stopCurrentLocked() }
func stopCurrentLocked() {
	current.RLock()
	i := current.inst
	current.RUnlock()
	if i == nil {
		return
	}
	i.cancel()
	<-i.done
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.stopped {
		unlockFile(i.lock)
		i.lock = nil
		i.stopped = true
	}
}
func currentInstance() *instance { current.RLock(); defer current.RUnlock(); return current.inst }

// An ABI callback may be reentrant while the worker is awaiting host RPC. Never
// join that worker synchronously in a panic boundary; cancel first and join out
// of the callback so the host can unwind and release its own locks.
func panicStop() {
	if i := currentInstance(); i != nil {
		i.cancel()
	}
	go stopCurrent()
}
