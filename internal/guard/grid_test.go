package guard

import (
	"context"
	"errors"
	"testing"
)

type listFailBackend struct{ durableBackend }

func (f *listFailBackend) List(context.Context) ([]Entry, error) { return nil, errors.New("down") }

func TestObservationsKeepDegradedCoverage(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(99)}
	e, _ := durableEngine(t, f)
	if err := e.Tick(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	// Remove the account from inventory on the next tick.
	empty := &emptyBackend{}
	e.backend = empty
	before, _, _, _ := e.View()
	if len(before.Observations["test.json"].Reasons) != 0 {
		t.Fatal("healthy baseline missing")
	}
	if err := e.Tick(context.Background(), now+1000, true); err != nil {
		t.Fatal(err)
	}
	s, _, _, _ := e.View()
	obs, ok := s.Observations["test.json"]
	t.Logf("degraded=%#v", obs)
	if !ok || !obs.Blocked || obs.Action != "inventory-degraded; no credential change" {
		t.Fatalf("missing degraded observation: %#v", s.Observations)
	}
}

type emptyBackend struct{}

func (emptyBackend) List(context.Context) ([]Entry, error) { return []Entry{}, nil }
func (emptyBackend) Read(context.Context, Entry) (Account, error) {
	return Account{}, errors.New("missing")
}
func (emptyBackend) Patch(context.Context, Account, map[string]any) error { return nil }
func (emptyBackend) Probe(context.Context, Account) (Sample, error) {
	return Sample{}, errors.New("missing")
}

func TestReadFailureIsVisibleForWhitelistedEntry(t *testing.T) {
	f := &durableBackend{raw: rawAuth("codex"), mtime: 10, sample: quota(99)}
	e, _ := durableEngine(t, f)
	f.failReads = map[int]bool{1: true}
	if err := e.Tick(context.Background(), now, true); err == nil {
		t.Fatal("read failure hidden")
	}
	s, _, _, _ := e.View()
	obs := s.Observations["test.json"]
	if !obs.Blocked || obs.Action != "read-degraded; no credential change" || obs.Reasons[0] != "credential metadata unavailable" {
		t.Fatalf("degraded read not visible: %#v", obs)
	}
}
