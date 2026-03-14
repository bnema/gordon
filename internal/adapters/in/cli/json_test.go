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

func TestRoutesList_JSONFlag_Accepted(t *testing.T) {
	cmd := newRoutesListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

func TestAttachmentsList_JSONFlag_Accepted(t *testing.T) {
	cmd := newAttachmentsListCmd()
	f := cmd.Flags().Lookup("json")
	assert.NotNil(t, f)
	if f != nil {
		assert.Equal(t, "false", f.DefValue)
	}
}

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

func TestRollbackList_JSONFlag_Accepted(t *testing.T) {
	cmd := newRollbackListCmd()
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

func TestRollbackList_JSONShape_RoundTripsTags(t *testing.T) {
	tags := []string{"v1.2.0", "v1.1.0"}
	payload, err := json.Marshal(tags)
	require.NoError(t, err)

	var got []string
	require.NoError(t, json.Unmarshal(payload, &got))
	assert.Equal(t, tags, got)
}

func TestBackupList_JSONShape_RoundTripsJobs(t *testing.T) {
	startedAt := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	jobs := []domain.BackupJob{{ID: "b1", Domain: "app.example.com", DBName: "postgres", Status: domain.BackupStatusCompleted, StartedAt: startedAt}}

	payload, err := json.Marshal(jobs)
	require.NoError(t, err)

	var got []domain.BackupJob
	require.NoError(t, json.Unmarshal(payload, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "b1", got[0].ID)
	assert.Equal(t, "app.example.com", got[0].Domain)
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

func TestAttachmentsList_JSONShape_RoundTripsTargetPayload(t *testing.T) {
	payload := struct {
		Target string   `json:"target"`
		Images []string `json:"images"`
	}{
		Target: "app.example.com",
		Images: []string{"postgres:16"},
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var got struct {
		Target string   `json:"target"`
		Images []string `json:"images"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))
	assert.Equal(t, payload.Target, got.Target)
	assert.Equal(t, payload.Images, got.Images)
}

func TestSecretsList_JSONShape_RoundTripsPayload(t *testing.T) {
	payload := struct {
		Domain      string                     `json:"domain"`
		Keys        []string                   `json:"keys"`
		Attachments []remote.AttachmentSecrets `json:"attachments"`
	}{
		Domain: "app.example.com",
		Keys:   []string{"API_KEY"},
		Attachments: []remote.AttachmentSecrets{{
			Service: "postgres",
			Keys:    []string{"POSTGRES_PASSWORD"},
		}},
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var got struct {
		Domain      string                     `json:"domain"`
		Keys        []string                   `json:"keys"`
		Attachments []remote.AttachmentSecrets `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))
	assert.Equal(t, payload.Domain, got.Domain)
	assert.Equal(t, payload.Keys, got.Keys)
	assert.Equal(t, payload.Attachments, got.Attachments)
}

func TestSecretsList_JSONShape_NormalizesNilAttachmentsToEmptySlice(t *testing.T) {
	payload := struct {
		Domain      string                     `json:"domain"`
		Keys        []string                   `json:"keys"`
		Attachments []remote.AttachmentSecrets `json:"attachments"`
	}{
		Domain:      "app.example.com",
		Keys:        []string{"API_KEY"},
		Attachments: []remote.AttachmentSecrets{},
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"attachments":[]`)

	var got struct {
		Domain      string                     `json:"domain"`
		Keys        []string                   `json:"keys"`
		Attachments []remote.AttachmentSecrets `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))
	assert.NotNil(t, got.Attachments)
	assert.Empty(t, got.Attachments)
}

func TestRoutesList_JSONShape_RoundTripsLocalPayload(t *testing.T) {
	payload := []struct {
		Domain string `json:"domain"`
		Image  string `json:"image"`
	}{
		{Domain: "app.example.com", Image: "app:latest"},
	}

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	var got []struct {
		Domain string `json:"domain"`
		Image  string `json:"image"`
	}
	require.NoError(t, json.Unmarshal(encoded, &got))
	require.Len(t, got, 1)
	assert.Equal(t, payload[0], got[0])
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
