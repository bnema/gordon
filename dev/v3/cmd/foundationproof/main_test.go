package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := setupCanaries(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPositiveControlsRoundTrip(t *testing.T) {
	dir := setup(t)
	if err := positiveControls(dir); err != nil {
		t.Fatal(err)
	}
}

func TestPositiveControlsMissing(t *testing.T) {
	if err := positiveControls(t.TempDir()); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestScenarioOpenContext(t *testing.T) {
	dir := setup(t)
	if err := run([]string{"run-scenario", "--dir", dir, "--bind", "127.0.0.1", "--expect", "open"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"report", "--dir", dir}); err != nil {
		t.Fatal(err)
	}
}

func TestScenarioConfinedExpectationFailsWhenOpen(t *testing.T) {
	dir := setup(t)
	if err := run([]string{"run-scenario", "--dir", dir, "--bind", "127.0.0.1", "--expect", "confined"}); err == nil {
		t.Fatal("expected mismatch error in an open context")
	}
	// Report reflects the recorded mismatch.
	if err := run([]string{"report", "--dir", dir}); err == nil {
		t.Fatal("expected report to flag failing checks")
	}
}

func TestScenarioDeniedCanary(t *testing.T) {
	dir := setup(t)
	secret := filepath.Join(dir, "secrets", "app-secret")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(secret, 0o600) })
	results := scenario(dir, "127.0.0.1")
	seen := map[string]string{}
	for _, r := range results {
		seen[r.Check] = r.Result
	}
	if seen["F1"] != "denied" {
		t.Fatalf("F1 = %q, want denied", seen["F1"])
	}
	if seen["F6"] != "denied" {
		t.Fatalf("F6 = %q, want denied", seen["F6"])
	}
	if seen["F2"] != "allowed" {
		t.Fatalf("F2 = %q, want allowed", seen["F2"])
	}
}

func TestUsageErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"bogus"},
		{"setup-canaries"},
		{"positive-controls"},
		{"run-scenario", "--dir", "x"},
		{"run-scenario", "--dir", "x", "--bind", "127.0.0.1", "--expect", "maybe"},
		{"report"},
		{"report", "--dir"},
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Fatalf("expected error for %q", args)
		}
	}
}

func TestVersionsRuns(t *testing.T) {
	if err := run([]string{"versions"}); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRead(t *testing.T) {
	dir := setup(t)
	if err := run([]string{"probe-read", dir + "/secrets/app-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"probe-read", dir + "/does-not-exist"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"probe-read"}); err == nil {
		t.Fatal("expected error without path")
	}
}
