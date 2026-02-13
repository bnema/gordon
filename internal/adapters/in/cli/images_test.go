package cli

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

type fakeImagesClient struct {
	listImagesResp []dto.Image
	listImagesErr  error
	pruneResp      *dto.ImagePruneResponse
	pruneErr       error

	listImagesCalls int
	pruneCalls      int
	lastPruneKeep   int
}

func (f *fakeImagesClient) ListImages(_ context.Context) ([]dto.Image, error) {
	f.listImagesCalls++
	if f.listImagesErr != nil {
		return nil, f.listImagesErr
	}
	return f.listImagesResp, nil
}

func (f *fakeImagesClient) PruneImages(_ context.Context, keepLast int) (*dto.ImagePruneResponse, error) {
	f.pruneCalls++
	f.lastPruneKeep = keepLast
	if f.pruneErr != nil {
		return nil, f.pruneErr
	}
	return f.pruneResp, nil
}

func TestRunImagesList_PrintsRowsAndSummary(t *testing.T) {
	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{
			{Repository: "registry.example.com/app", Tag: "latest", Size: 12_000_000, Created: createdAt, ID: "sha256:1111", Dangling: false},
			{Repository: "<none>", Tag: "<none>", Size: 512_000, Created: createdAt, ID: "sha256:2222", Dangling: true},
		},
	}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "REPOSITORY")
	assert.Contains(t, text, "registry.example.com/app")
	assert.Contains(t, text, "latest")
	assert.Contains(t, text, "Total images: 2 (dangling: 1)")
	assert.Equal(t, 1, client.listImagesCalls)
}

func TestRunImagesList_HeaderOrderAndSummaryLine(t *testing.T) {
	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{
			{Repository: "registry.example.com/app", Tag: "latest", Size: 12_000_000, Created: createdAt, ID: "sha256:1111", Dangling: false},
		},
	}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := stripANSI(out.String())
	header := findHeaderLine(text)
	assert.NotEmpty(t, header)
	assert.Less(t, strings.Index(header, "REPOSITORY"), strings.Index(header, "TAG"))
	assert.Less(t, strings.Index(header, "TAG"), strings.Index(header, "SIZE"))
	assert.Less(t, strings.Index(header, "SIZE"), strings.Index(header, "CREATED"))
	assert.Less(t, strings.Index(header, "CREATED"), strings.Index(header, "IMAGE_ID"))
	assert.Less(t, strings.Index(header, "IMAGE_ID"), strings.Index(header, "DANGLING"))
	assert.Contains(t, text, "Total images: 1 (dangling: 0)")
}

func TestRunImagesList_TruncatesLongValuesAndKeepsBoundedRowShape(t *testing.T) {
	createdAt := time.Date(2026, 2, 8, 12, 0, 0, 0, time.UTC)
	longRepository := "registry.example.com/team/this-is-a-very-long-service-name-with-extra-segments/and/more/segments/than/anyone/should/reasonably/use"
	longImageID := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-extra-suffix-for-overflow"
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{
			{Repository: longRepository, Tag: "latest", Size: 12_000_000, Created: createdAt, ID: longImageID, Dangling: false},
		},
	}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := stripANSI(out.String())
	header := findHeaderLine(text)
	assert.NotEmpty(t, header)
	assert.Less(t, strings.Index(header, "REPOSITORY"), strings.Index(header, "TAG"))
	assert.Less(t, strings.Index(header, "TAG"), strings.Index(header, "SIZE"))
	assert.Less(t, strings.Index(header, "SIZE"), strings.Index(header, "CREATED"))
	assert.Less(t, strings.Index(header, "CREATED"), strings.Index(header, "IMAGE_ID"))
	assert.Less(t, strings.Index(header, "IMAGE_ID"), strings.Index(header, "DANGLING"))

	row := findImageListRowLine(text, "latest")
	assert.NotEmpty(t, row)

	cols := splitTableColumns(row)
	require.Len(t, cols, 6)

	repositoryField := cols[0]
	imageIDField := cols[4]

	assert.NotEqual(t, longRepository, repositoryField)
	assert.NotEqual(t, longImageID, imageIDField)
	assert.True(t, strings.HasSuffix(repositoryField, "..."), "repository should be truncated with ellipsis")
	assert.True(t, strings.HasSuffix(imageIDField, "..."), "image id should be truncated with ellipsis")
	assert.True(t, strings.HasPrefix(longRepository, strings.TrimSuffix(repositoryField, "...")))
	assert.True(t, strings.HasPrefix(longImageID, strings.TrimSuffix(imageIDField, "...")))

	assert.Contains(t, text, "Total images: 1 (dangling: 0)")
}

func TestRunImagesList_LongDigestLikeValuesAndEmptyFormattedFields(t *testing.T) {
	longRepository := "registry.example.com/very/long/repository/path/with/deep/nesting/and-digest-like-ref@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef-extra"
	longTag := "release-2026-02-12-build-metadata-with-a-very-very-long-tag-name"
	longImageID := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{
			{Repository: longRepository, Tag: longTag, Size: 0, Created: time.Time{}, ID: longImageID, Dangling: true},
		},
	}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := stripANSI(out.String())
	rows := findImageListRows(text)
	require.Len(t, rows, 1, "expected a single, no-wrap data row")

	cols := rows[0]
	require.Len(t, cols, 6)

	repositoryField := cols[0]
	tagField := cols[1]
	sizeField := cols[2]
	createdField := cols[3]
	imageIDField := cols[4]

	assert.True(t, strings.HasSuffix(repositoryField, "..."), "repository should be truncated with ellipsis")
	assert.True(t, strings.HasSuffix(tagField, "..."), "tag should be truncated with ellipsis")
	assert.True(t, strings.HasSuffix(imageIDField, "..."), "image id should be truncated with ellipsis")
	assert.True(t, strings.HasPrefix(longRepository, strings.TrimSuffix(repositoryField, "...")))
	assert.True(t, strings.HasPrefix(longTag, strings.TrimSuffix(tagField, "...")))
	assert.True(t, strings.HasPrefix(longImageID, strings.TrimSuffix(imageIDField, "...")))

	assert.Equal(t, "-", sizeField)
	assert.Equal(t, "-", createdField)
	assert.Contains(t, text, "Total images: 1 (dangling: 1)")
}

func TestRunImagesList_EmptyOutput(t *testing.T) {
	client := &fakeImagesClient{listImagesResp: []dto.Image{}}

	var out bytes.Buffer
	err := runImagesList(context.Background(), client, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "No images found")
	assert.Contains(t, text, "Total images: 0")
	assert.Equal(t, 1, client.listImagesCalls)
}

func TestRunImagesPrune_DryRunCallsListOnly(t *testing.T) {
	client := &fakeImagesClient{
		listImagesResp: []dto.Image{{Dangling: true}, {Dangling: false}, {Dangling: true}},
	}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{DryRun: true, KeepLast: 3}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "Dry run")
	assert.Contains(t, text, "would prune 2 dangling runtime images")
	assert.Contains(t, text, "would keep latest + 3 previous tags")
	assert.Equal(t, 1, client.listImagesCalls)
	assert.Equal(t, 0, client.pruneCalls)
}

func TestRunImagesPrune_DryRunKeepZeroSkipsRegistry(t *testing.T) {
	client := &fakeImagesClient{listImagesResp: []dto.Image{{Dangling: true}}}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{DryRun: true, KeepLast: 0}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Contains(t, text, "Registry cleanup skipped (--keep=0)")
	assert.Equal(t, 1, client.listImagesCalls)
	assert.Equal(t, 0, client.pruneCalls)
}

func TestRunImagesPrune_RuntimeOnlyForcesKeepZero(t *testing.T) {
	client := &fakeImagesClient{pruneResp: &dto.ImagePruneResponse{}}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 9, RuntimeOnly: true}, &out)
	require.NoError(t, err)

	assert.Equal(t, 1, client.pruneCalls)
	assert.Equal(t, 0, client.lastPruneKeep)
	assert.Contains(t, out.String(), "Registry cleanup skipped")
}

func TestRunImagesPrune_UsesKeepFlagWhenNotRuntimeOnly(t *testing.T) {
	client := &fakeImagesClient{
		pruneResp: &dto.ImagePruneResponse{
			Runtime:  dto.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096},
			Registry: dto.RegistryPruneResult{TagsRemoved: 3, BlobsRemoved: 1, SpaceReclaimed: 8192},
		},
	}

	var out bytes.Buffer
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 5}, &out)
	require.NoError(t, err)

	text := out.String()
	assert.Equal(t, 1, client.pruneCalls)
	assert.Equal(t, 5, client.lastPruneKeep)
	assert.Contains(t, text, "Runtime: deleted=2")
	assert.Contains(t, text, "Registry: tags_removed=3")
}

func TestRunImagesPrune_ReturnsRemoteErrors(t *testing.T) {
	client := &fakeImagesClient{pruneErr: errors.New("request failed")}

	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 2}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to prune images")
}

func TestRunImagesPrune_RejectsNegativeKeep(t *testing.T) {
	err := runImagesPrune(context.Background(), &fakeImagesClient{}, imagesPruneOptions{KeepLast: -1}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--keep must be >= 0")
}

func TestRunImagesPrune_RejectsEmptyResponse(t *testing.T) {
	client := &fakeImagesClient{pruneResp: nil}
	err := runImagesPrune(context.Background(), client, imagesPruneOptions{KeepLast: 1}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func stripANSI(value string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	return ansiRegex.ReplaceAllString(value, "")
}

func findHeaderLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "REPOSITORY") && strings.Contains(line, "DANGLING") {
			return line
		}
	}

	return ""
}

func findImageListRowLine(text string, tag string) string {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Images") || strings.HasPrefix(trimmed, "Total images:") {
			continue
		}

		cols := splitTableColumns(line)
		if len(cols) == 6 && cols[1] == tag {
			return line
		}
	}

	return ""
}

func findImageListRows(text string) [][]string {
	rows := make([][]string, 0)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "Images") || strings.HasPrefix(trimmed, "Total images:") {
			continue
		}

		cols := splitTableColumns(line)
		if len(cols) == 6 && cols[0] != "REPOSITORY" {
			rows = append(rows, cols)
		}
	}

	return rows
}

func splitTableColumns(line string) []string {
	if strings.Contains(line, "│") {
		parts := strings.Split(line, "│")
		cols := make([]string, 0, len(parts))
		for _, part := range parts {
			cell := strings.TrimSpace(part)
			if cell != "" {
				cols = append(cols, cell)
			}
		}
		return cols
	}

	return regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(line), -1)
}
