package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedPrivValidatorStateCreatesEmptyState(t *testing.T) {
	home := t.TempDir()
	if err := seedPrivValidatorState(home); err != nil {
		t.Fatalf("seedPrivValidatorState: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "data", "priv_validator_state.json"))
	if err != nil {
		t.Fatalf("read seeded state: %v", err)
	}
	var state struct {
		Height string `json:"height"`
		Round  int    `json:"round"`
		Step   int    `json:"step"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("seeded state is not valid json: %v", err)
	}
	if state.Height != "0" || state.Round != 0 || state.Step != 0 {
		t.Fatalf("seeded state = %+v, want a zeroed state", state)
	}
}

// Overwriting the signing state is how a validator double-signs, so an existing
// file must survive untouched.
func TestSeedPrivValidatorStateNeverOverwrites(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "data", "priv_validator_state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"height":"22460000","round":0,"step":3}`)
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := seedPrivValidatorState(home); err != nil {
		t.Fatalf("seedPrivValidatorState: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(existing) {
		t.Fatalf("state was modified:\n got %s\nwant %s", got, existing)
	}
}
