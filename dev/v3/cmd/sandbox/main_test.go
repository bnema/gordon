package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	for _, value := range []string{"hello", "a'b", "a b", "$(touch /tmp/no)", "&&", "A=B"} {
		s := sandbox{ctx: context.Background()}
		got, err := s.out("sh", "-c", "printf '%s' "+quote(value))
		if err != nil || got != value {
			t.Fatalf("%q: got %q, %v", value, got, err)
		}
	}
}
func TestImageDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	if !validHash(path, want) {
		t.Fatal("correct image rejected")
	}
	if validHash(path, imageSHA) {
		t.Fatal("changed image accepted")
	}
}
func TestOwnershipRefusesForeignResources(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$3\" in\nlist|net-list|pool-list) printf 'gordon-v3-sandbox\\n';;\n*) printf 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\\n';;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	s := sandbox{ctx: context.Background(), state: dir}
	if err := s.owned(); err == nil {
		t.Fatal("missing ownership record accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "uuid"), []byte("11111111-2222-3333-4444-555555555555"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.owned(); err == nil || !strings.Contains(err.Error(), "foreign") {
		t.Fatalf("foreign UUID accepted: %v", err)
	}
}
func TestSSHIsPinnedAndNonInteractive(t *testing.T) {
	s := sandbox{state: "/private"}
	args := strings.Join(s.sshArgs(), " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "ForwardAgent=no", "BatchMode=yes", "-F /dev/null", "127.0.0.1"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
func TestTemplatesDoNotPublishSSHToLAN(t *testing.T) {
	data, err := os.ReadFile("../../cloud-init/domain.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`address="127.0.0.1"`, `start="2222" to="22"`, `<domain type="kvm">`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing %s", want)
		}
	}
	data, err = os.ReadFile("../../cloud-init/network.xml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "<forward") {
		t.Fatal("test network must not forward to LAN")
	}
}

func TestVolumeDeletionAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name, table string
		valid       bool
	}{
		{"owned", " Name Path\n------\n disk.qcow2 /var/lib/libvirt/images/gordon-v3-sandbox/disk.qcow2\n seed.iso /var/lib/libvirt/images/gordon-v3-sandbox/seed.iso", true},
		{"empty", "Name Path\n------", true},
		{"foreign", "Name Path\n------\n important.qcow2 /foreign/important.qcow2", false},
		{"wrong path", "Name Path\n------\n disk.qcow2 /foreign/disk.qcow2", false},
		{"malformed", "unexpected output", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := volumeNames(tt.table)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v err=%v", tt.valid, err)
			}
		})
	}
}
