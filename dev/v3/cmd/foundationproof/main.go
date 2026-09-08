// foundationproof is an explicitly test-only A1A.1 harness. It sets up
// canary files, proves positive controls, and runs allow/deny scenarios in
// the current process context. Confinement comes from the surrounding
// systemd unit profile under test, never from this binary.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const usage = `usage:
  foundationproof versions
  foundationproof setup-canaries <dir>
  foundationproof positive-controls <dir>
  foundationproof run-scenario --dir <dir> --bind <ip> --expect <open|confined>
  foundationproof report --dir <dir>
  foundationproof probe-read <path>
  foundationproof landlock-demo <dir>`

type checkResult struct {
	Check    string `json:"check"`
	Action   string `json:"action"`
	Target   string `json:"target"`
	Result   string `json:"result"`
	Expected string `json:"expected"`
	Pass     bool   `json:"pass"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "foundationproof:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "versions":
		return versions()
	case "setup-canaries":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return setupCanaries(args[1])
	case "positive-controls":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return positiveControls(args[1])
	case "run-scenario":
		return runScenario(args[1:])
	case "report":
		if len(args) != 3 || args[1] != "--dir" {
			return errors.New(usage)
		}
		return report(args[2])
	case "probe-read":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return probeRead(args[1])
	case "landlock-demo":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return landlockDemo(args[1])
	default:
		return errors.New(usage)
	}
}

// canaryFiles maps check IDs to paths relative to the canary dir.
func canaryFiles() map[string]string {
	return map[string]string{
		"F1":  "secrets/app-secret",
		"F2":  "podman/podman.sock",
		"F3":  "control/control-private",
		"F7a": "sockets/ingress-admin.sock",
		"F7b": "sockets/runtime-control.sock",
	}
}

func setupCanaries(dir string) error {
	files := canaryFiles()
	dirs := []string{"secrets", "control", "podman", "sockets", "ipc"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	for check, rel := range files {
		p := filepath.Join(dir, rel)
		if err := os.WriteFile(p, []byte("canary for "+check+"\n"), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	link := filepath.Join(dir, "sockets", "link-to-secret")
	os.Remove(link)
	if err := os.Symlink("../secrets/app-secret", link); err != nil {
		return fmt.Errorf("symlink traversal canary: %w", err)
	}
	fmt.Println("canaries ready: " + dir)
	return nil
}

func positiveControls(dir string) error {
	for check, rel := range canaryFiles() {
		p := filepath.Join(dir, rel)
		if _, err := os.ReadFile(p); err != nil {
			return fmt.Errorf("positive control %s unreadable (%s): %w", check, rel, err)
		}
	}
	link := filepath.Join(dir, "sockets", "link-to-secret")
	if _, err := os.ReadFile(link); err != nil {
		return fmt.Errorf("positive control F6 traversal unreadable: %w", err)
	}
	fmt.Println("positive controls pass: canaries exist and are readable")
	return nil
}

func versions() error {
	info := map[string]string{
		"os":      osRelease(),
		"kernel":  kernelRelease(),
		"lsm":     readTrim("/sys/kernel/security/lsm"),
		"podman":  toolVersion("podman", "--version"),
		"systemd": toolVersion("systemctl", "--version"),
		"uid":     fmt.Sprint(os.Getuid()),
		"gid":     fmt.Sprint(os.Getgid()),
	}
	return json.NewEncoder(os.Stdout).Encode(info)
}

func osRelease() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "unknown"
	}
	for line := range strings.Lines(strings.TrimSpace(string(data))) {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return "unknown"
}

func kernelRelease() string {
	return readTrim("/proc/sys/kernel/osrelease")
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func toolVersion(name string, args ...string) string {
	//nolint:gosec // test harness only; call sites pass fixed binaries with fixed arguments.
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "unknown"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first
}

func parseScenarioArgs(args []string) (dir, bind, expect string, err error) {
	for i := 0; i < len(args); i++ {
		var value *string
		switch args[i] {
		case "--dir":
			value = &dir
		case "--bind":
			value = &bind
		case "--expect":
			value = &expect
		default:
			return "", "", "", errors.New(usage)
		}
		i++
		if i >= len(args) {
			return "", "", "", errors.New(usage)
		}
		*value = args[i]
	}
	if dir == "" || bind == "" || (expect != "open" && expect != "confined") {
		return "", "", "", errors.New(usage)
	}
	return dir, bind, expect, nil
}

func evaluateScenario(results []checkResult, wantDenied bool) bool {
	pass := true
	for i := range results {
		if results[i].Check == "F5" {
			continue // informational only; never gates the verdict
		}
		results[i].Expected = "allowed"
		if strings.HasPrefix(results[i].Check, "F") && wantDenied {
			results[i].Expected = "denied"
		}
		results[i].Pass = results[i].Result == results[i].Expected
		if !results[i].Pass {
			pass = false
		}
	}
	return pass
}

func runScenario(args []string) error {
	dir, bind, expect, err := parseScenarioArgs(args)
	if err != nil {
		return err
	}
	results := scenario(dir, bind)
	pass := evaluateScenario(results, expect == "confined")
	f, err := os.OpenFile(filepath.Join(dir, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open results: %w", err)
	}
	enc := json.NewEncoder(f)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return fmt.Errorf("write results: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close results: %w", err)
	}
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		fmt.Printf("%s %s %s: got %s, want %s\n", status, r.Check, r.Action, r.Result, r.Expected)
	}
	if !pass {
		return fmt.Errorf("scenario mismatch for expect=%s", expect)
	}
	return nil
}

func scenario(dir, bind string) []checkResult {
	var out []checkResult
	allow := func(check, action, target string, err error) {
		r := checkResult{Check: check, Action: action, Target: target, Result: "allowed"}
		if err != nil {
			r.Result = "denied (" + err.Error() + ")"
			if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
				r.Result = "denied"
			}
		}
		out = append(out, r)
	}
	// B1: legitimate binds stay usable.
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err == nil {
		ln.Close()
	}
	allow("B1", "bind-tcp", bind, err)
	pc, err := net.ListenPacket("udp", net.JoinHostPort(bind, "0"))
	if err == nil {
		pc.Close()
	}
	allow("B1", "bind-udp", bind, err)
	// B2: private IPC read/write.
	ipc := filepath.Join(dir, "ipc", "probe")
	err = os.WriteFile(ipc, []byte("ping\n"), 0o600)
	if err == nil {
		_, err = os.ReadFile(ipc)
		os.Remove(ipc)
	}
	allow("B2", "ipc-read-write", "ipc/", err)
	// F1-F3, F7: canary reads.
	for check, rel := range canaryFiles() {
		_, err := os.ReadFile(filepath.Join(dir, rel))
		allow(check, "read", rel, err)
	}
	// F6: traversal read.
	_, err = os.ReadFile(filepath.Join(dir, "sockets", "link-to-secret"))
	allow("F6", "read-traversal", "sockets/link-to-secret", err)
	// F4: write to a canary outside owned dirs.
	f, err := os.OpenFile(filepath.Join(dir, "secrets", "app-secret"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		_, err = f.WriteString("x")
		f.Close()
	}
	allow("F4", "write", "secrets/app-secret", err)
	// F5: same-account process access is informational only.
	_, err = os.ReadFile("/proc/1/cmdline")
	r := checkResult{Check: "F5", Action: "read-proc", Target: "/proc/1/cmdline", Expected: "informational"}
	if err != nil {
		r.Result = "denied"
	} else {
		r.Result = "allowed"
	}
	r.Pass = true
	out = append(out, r)
	return out
}

func probeRead(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("denied: " + err.Error())
		return nil
	}
	fmt.Printf("allowed: %d bytes\n", len(data))
	return nil
}

func report(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return fmt.Errorf("read results: %w", err)
	}
	var pass, fail int
	for line := range strings.Lines(strings.TrimSpace(string(data))) {
		if line == "" {
			continue
		}
		var r checkResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return fmt.Errorf("parse results: %w", err)
		}
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
			fail++
		} else {
			pass++
		}
		fmt.Printf("%s %s %s: got %s, want %s\n", status, r.Check, r.Action, r.Result, r.Expected)
	}
	fmt.Printf("total: %d pass, %d fail\n", pass, fail)
	if fail > 0 {
		return errors.New("failing checks present")
	}
	return nil
}
