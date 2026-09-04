package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuote(t *testing.T) {
	for _, value := range []string{"hello", "a'b", "a b", "$(touch /tmp/no)", "&&", "A=B"} {
		s := sandbox{ctx: context.Background()}
		got, err := s.out("sh", "-c", "printf '%s' "+quote(value))
		require.NoError(t, err, "%q", value)
		assert.Equal(t, value, got, "%q", value)
	}
}
func TestImageDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image")
	require.NoError(t, os.WriteFile(path, []byte("image"), 0600))
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("image")))
	assert.True(t, validHash(path, want), "correct image rejected")
	assert.False(t, validHash(path, imageSHA), "changed image accepted")
}
func TestOwnershipRefusesForeignResources(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$3\" in\nlist|net-list|pool-list) printf 'gordon-v3-sandbox\\n';;\n*) printf 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\\n';;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "virsh"), []byte(script), 0700))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	s := sandbox{ctx: context.Background(), state: dir}
	require.Error(t, s.owned(), "missing ownership record accepted")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uuid"), []byte("11111111-2222-3333-4444-555555555555"), 0600))
	err := s.owned()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "foreign", "foreign UUID accepted: %v", err)
}
func TestSSHIsPinnedAndNonInteractive(t *testing.T) {
	s := sandbox{state: "/private"}
	args := strings.Join(s.sshArgs(), " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "ForwardAgent=no", "BatchMode=yes", "-F /dev/null", "127.0.0.1"} {
		assert.Contains(t, args, want, "missing %s", want)
	}
}
func TestTemplatesDoNotPublishSSHToLAN(t *testing.T) {
	data, err := os.ReadFile("../../cloud-init/domain.xml")
	require.NoError(t, err)
	for _, want := range []string{`address="127.0.0.1"`, `start="2222" to="22"`, `<domain type="kvm">`} {
		assert.Contains(t, string(data), want, "missing %s", want)
	}
	data, err = os.ReadFile("../../cloud-init/network.xml")
	require.NoError(t, err)
	assert.NotContains(t, string(data), "<forward", "test network must not forward to LAN")
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
			assert.Equal(t, tt.valid, err == nil, "valid=%v err=%v", tt.valid, err)
		})
	}
}
