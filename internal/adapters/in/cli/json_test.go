package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

func TestSecretsList_JSONFlag_Accepted(t *testing.T) {
	cmd := newSecretsListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestImagesList_JSONFlag_Accepted(t *testing.T) {
	cmd := newImagesListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestBackupList_JSONFlag_Accepted(t *testing.T) {
	cmd := newBackupListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestTokenList_JSONFlag_Accepted(t *testing.T) {
	cmd := newTokenListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestRemotesList_JSONFlag_Accepted(t *testing.T) {
	cmd := newRemotesListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestImagesList_JSONShape_RoundTripsDTO(t *testing.T) {
	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	images := []dto.Image{{Repository: "registry.example.com/app", Tag: "latest", Size: 12_000_000, Created: createdAt, ID: "sha256:1111", Dangling: false}}

	payload, err := json.Marshal(images)
	require.NoError(t, err)

	var got []dto.Image
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got, 1)
	assert.Equal(t, images[0].Repository, got[0].Repository)
	assert.Equal(t, images[0].Tag, got[0].Tag)
	assert.Equal(t, images[0].ID, got[0].ID)
}

func TestPinList_JSONShape_RoundTripsTags(t *testing.T) {
	tags := []string{"v1.2.0", "v1.1.0"}
	payload, err := json.Marshal(tags)
	require.NoError(t, err)

	var got []string
	require.NoError(t, json.Unmarshal(payload, &got))
	assert.Equal(t, tags, got)
}

func TestBackupList_JSONShape_RoundTripsJobs(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	jobs := []domain.BackupJob{{ID: "b1", App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted, StartedAt: startedAt}}

	payload, err := json.Marshal(jobs)
	require.NoError(t, err)

	var got []domain.BackupJob
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "b1", got[0].ID)
	assert.Equal(t, "shop", got[0].App)
	assert.Equal(t, "api", got[0].Service)
	assert.Equal(t, "orders", got[0].DBName)
}

func TestTokenList_JSONShape_RoundTripsTokens(t *testing.T) {
	tokens := []domain.Token{{
		ID:        "tok-1",
		Subject:   "deploy",
		Scopes:    []string{"admin:*:*"},
		IssuedAt:  time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}}

	payload, err := json.Marshal(tokens)
	require.NoError(t, err)

	var got []domain.Token
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got, 1)
	assert.Equal(t, tokens[0].ID, got[0].ID)
	assert.Equal(t, tokens[0].Subject, got[0].Subject)
	assert.Equal(t, tokens[0].Scopes, got[0].Scopes)
}

func TestRemotesList_JSONShape_RoundTripsRemoteObjects(t *testing.T) {
	items := []struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Active      bool   `json:"active"`
		InsecureTLS bool   `json:"insecure_tls"`
	}{
		{Name: "prod", URL: "https://prod.example.com", Active: true, InsecureTLS: true},
	}

	payload, err := json.Marshal(items)
	require.NoError(t, err)

	var got []struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Active      bool   `json:"active"`
		InsecureTLS bool   `json:"insecure_tls"`
	}
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "prod", got[0].Name)
	assert.Equal(t, "https://prod.example.com", got[0].URL)
	assert.True(t, got[0].Active)
	assert.True(t, got[0].InsecureTLS)
}

func TestSecretsList_JSONShape_RoundTripsPayload(t *testing.T) {
	payload := remote.SecretsListResult{
		Domain: "app.example.com",
		Keys:   []string{"API_KEY"},
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var got remote.SecretsListResult
	require.NoError(t, json.Unmarshal(encoded, &got))
	assert.Equal(t, payload.Domain, got.Domain)
	assert.Equal(t, payload.Keys, got.Keys)
}

func TestWriteJSON_ProducesValidIndentedJSON(t *testing.T) {
	var out bytes.Buffer
	err := writeJSON(&out, map[string]any{"ok": true})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	assert.Equal(t, true, got["ok"])
	assert.Contains(t, out.String(), "\n")
}
