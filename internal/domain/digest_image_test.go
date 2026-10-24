package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDigestImage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	hosts := []string{"gordon.example.com", "ci.example.com", "old.example.com"}
	valid := map[string]string{
		"web@" + digest:                      "web",
		"gordon.example.com/web@" + digest:   "web",
		"CI.example.com/team/web@" + digest:  "team/web",
		"old.example.com:443/web@" + digest:  "web",
		"gordon.example.com/a.b/c@" + digest: "a.b/c",
	}
	for ref, repo := range valid {
		got, err := ParseDigestImage(ref, hosts)
		require.NoError(t, err, ref)
		assert.Equal(t, DigestImage{Repository: repo, Digest: digest}, got, ref)
		assert.Equal(t, "gordon.example.com/"+repo+"@"+digest, got.Canonical("gordon.example.com"))
	}
	invalid := []string{
		"",
		"web:1.0",
		"gordon.example.com/web:latest",
		"web@sha512:" + strings.Repeat("a", 128),
		"web@sha256:" + strings.Repeat("A", 64),
		"web@sha256:abc",
		"docker.io/web@" + digest,
		"evil.example.com/web@" + digest,
		"localhost:5000/web@" + digest,
		"web:1@" + digest,
		"Web@" + digest,
		" web@" + digest,
		"web@" + digest + "@" + digest,
		"gordon.example.com/../web@" + digest,
	}
	for _, ref := range invalid {
		_, err := ParseDigestImage(ref, hosts)
		require.ErrorIs(t, err, ErrInvalidImageReference, ref)
	}
}
