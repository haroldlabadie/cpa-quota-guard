// guard-state exchanges only sanitized ownership proofs. Raw OAuth data is read
// from stdin and is never echoed, logged or accepted as a command-line argument.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/haroldlabadie/cpa-quota-guard/internal/guard"
	"io"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "guard-state: invalid input or proof")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "fingerprint" && os.Args[1] != "verify" && os.Args[1] != "validate-state" && os.Args[1] != "initialize-state") {
		return fmt.Errorf("command")
	}
	if os.Args[1] == "initialize-state" {
		s := guard.NewState()
		s.Initialization = &guard.Initialization{Kind: "fresh-install", CreatedMS: time.Now().UnixMilli()}
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	var req struct {
		Auth      map[string]any  `json:"auth"`
		MtimeNS   int64           `json:"mtime_ns"`
		Name      string          `json:"name"`
		AuthIndex string          `json:"auth_index"`
		Provider  string          `json:"provider"`
		Email     string          `json:"email"`
		Proof     *guard.Snapshot `json:"proof,omitempty"`
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, guard.MaxFile+1))
	if err != nil {
		return err
	}
	if os.Args[1] == "validate-state" {
		if _, err = guard.ParseState(b); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]bool{"valid": true})
	}
	if err = guard.StrictJSON(b, &req); err != nil {
		return err
	}
	if req.Auth == nil || req.Name == "" || req.AuthIndex == "" || req.MtimeNS <= 0 || (req.Provider != "codex" && req.Provider != "antigravity") {
		return fmt.Errorf("input")
	}
	a, err := guard.MakeAccount(guard.Entry{Name: req.Name, AuthIndex: req.AuthIndex, Provider: req.Provider, Email: req.Email}, req.Auth, req.MtimeNS)
	if err != nil {
		return err
	}
	var out any = a.Snapshot
	if os.Args[1] == "verify" {
		out = map[string]any{"valid": guard.Proven(req.Proof, a.Snapshot), "snapshot": a.Snapshot}
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
