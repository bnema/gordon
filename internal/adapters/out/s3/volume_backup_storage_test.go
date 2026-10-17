package s3

import (
	"bytes"
	"context"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

type fakeVolumeS3Client struct {
	objects  map[string][]byte
	metadata map[string]map[string]string
	deleted  []string
	heads    int
}

func newFakeVolumeS3Client() *fakeVolumeS3Client {
	return &fakeVolumeS3Client{objects: make(map[string][]byte), metadata: make(map[string]map[string]string)}
}

func (f *fakeVolumeS3Client) GetObject(_ context.Context, params *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	data := f.objects[aws.ToString(params.Key)]
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (f *fakeVolumeS3Client) HeadObject(_ context.Context, params *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	f.heads++
	return &awss3.HeadObjectOutput{Metadata: f.metadata[aws.ToString(params.Key)]}, nil
}

func (f *fakeVolumeS3Client) ListObjectsV2(_ context.Context, params *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	prefix := aws.ToString(params.Prefix)
	keys := make([]string, 0)
	for key := range f.objects {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	contents := make([]awss3types.Object, 0, len(keys))
	for _, key := range keys {
		size := int64(len(f.objects[key]))
		contents = append(contents, awss3types.Object{Key: aws.String(key), Size: aws.Int64(size)})
	}
	return &awss3.ListObjectsV2Output{Contents: contents, IsTruncated: aws.Bool(false)}, nil
}

func (f *fakeVolumeS3Client) DeleteObject(_ context.Context, params *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	key := aws.ToString(params.Key)
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return &awss3.DeleteObjectOutput{}, nil
}

type fakeVolumeUploader struct {
	client *fakeVolumeS3Client
	last   *transfermanager.UploadObjectInput
}

func (f *fakeVolumeUploader) UploadObject(_ context.Context, input *transfermanager.UploadObjectInput, _ ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error) {
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	key := aws.ToString(input.Key)
	f.client.objects[key] = data
	f.client.metadata[key] = input.Metadata
	f.last = input
	return &transfermanager.UploadObjectOutput{Key: input.Key}, nil
}

func TestVolumeBackupStorageStoreListGet(t *testing.T) {
	client := newFakeVolumeS3Client()
	uploader := &fakeVolumeUploader{client: client}
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{
		S3Bucket: "gordon-backups",
		S3Prefix: "/prod/gordon/",
	}, client, uploader)
	started := time.Date(2026, 6, 19, 2, 0, 0, 0, time.UTC)

	artifact, err := storage.StoreVolumeArchive(context.Background(), domain.VolumeBackupJob{
		ID:            "job/1",
		App:           "app.example.com",
		Service:       "api",
		ContainerName: "app",
		VolumeName:    "gordon-app-data",
		MountPath:     "/data",
		StartedAt:     started,
		Metadata:      map[string]string{"compression": string(domain.VolumeBackupCompressionZstd)},
	}, bytes.NewReader([]byte("archive")))
	require.NoError(t, err)

	assert.Equal(t, "s3://gordon-backups/prod/gordon/apps/app.example.com/volumes/gordon-app-data/20260619T020000Z-job_1.tar.zst", artifact)
	assert.Equal(t, "application/zstd", aws.ToString(uploader.last.ContentType))
	assert.Empty(t, uploader.last.StorageClass, "no storage class keeps the bucket default")

	jobs, err := storage.ListVolumeArchives(context.Background(), "app.example.com")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.BackupStatusCompleted, jobs[0].Status)
	assert.Equal(t, "api", jobs[0].Service)
	assert.Equal(t, "app", jobs[0].ContainerName)
	assert.Equal(t, "/data", jobs[0].MountPath)
	assert.Equal(t, string(domain.VolumeBackupCompressionZstd), jobs[0].Metadata["compression"])
	assert.Equal(t, int64(len("archive")), jobs[0].SizeBytes)
	assert.Equal(t, artifact, jobs[0].ArtifactRef)

	rc, err := storage.GetVolumeArchive(context.Background(), artifact)
	require.NoError(t, err)
	defer rc.Close()
	data, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, []byte("archive"), data)
}

func TestVolumeBackupStorageSendsConfiguredStorageClass(t *testing.T) {
	client := newFakeVolumeS3Client()
	uploader := &fakeVolumeUploader{client: client}
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{
		S3Bucket:       "bucket",
		S3StorageClass: "ONEZONE_IA",
	}, client, uploader)

	_, err := storage.StoreVolumeArchive(context.Background(), domain.VolumeBackupJob{
		ID:         "job",
		App:        "app",
		VolumeName: "data",
		StartedAt:  time.Date(2026, 6, 19, 2, 0, 0, 0, time.UTC),
	}, bytes.NewReader([]byte("archive")))
	require.NoError(t, err)

	assert.Equal(t, "ONEZONE_IA", string(uploader.last.StorageClass))
}

// TestVolumeBackupStorageRetentionMixesLongAndShortIDs proves archives
// written with the former timestamped job IDs stay listed and age out
// under retention next to archives with short IDs.
func TestVolumeBackupStorageRetentionMixesLongAndShortIDs(t *testing.T) {
	client := newFakeVolumeS3Client()
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{S3Bucket: "bucket", S3Prefix: "gordon"}, client, &fakeVolumeUploader{client: client})
	longID := "gordon/apps/forgejo/volumes/forgejo-data/20261017T052537Z-2026-10-17T05_25_37.725487298Z-7cddcd8f.tar.gz"
	short1 := "gordon/apps/forgejo/volumes/forgejo-data/20261018T013000Z-1a2b3c4d.tar.gz"
	short2 := "gordon/apps/forgejo/volumes/forgejo-data/20261019T013000Z-5e6f7a8b.tar.gz"
	for _, key := range []string{longID, short1, short2} {
		client.objects[key] = []byte("x")
	}

	jobs, err := storage.ListVolumeArchives(context.Background(), "forgejo")
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	assert.Equal(t, "5e6f7a8b", jobs[0].ID)
	assert.Equal(t, "2026-10-17T05_25_37.725487298Z-7cddcd8f", jobs[2].ID)

	deleted, err := storage.ApplyVolumeRetention(context.Background(), "forgejo", domain.VolumeBackupRetentionPolicy{Keep: 2})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.Equal(t, []string{longID}, client.deleted)
}

// TestVolumeBackupStorageLatestVolumeArchives proves the newest archive of
// each app volume is selected from the listing alone, with no per-object
// metadata request.
func TestVolumeBackupStorageLatestVolumeArchives(t *testing.T) {
	client := newFakeVolumeS3Client()
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{S3Bucket: "bucket", S3Prefix: "gordon"}, client, nil)
	for key, data := range map[string]string{
		"gordon/apps/forgejo/volumes/forgejo-data/20261017T013000Z-1a2b3c4d.tar.gz":  "old",
		"gordon/apps/forgejo/volumes/forgejo-data/20261018T013000Z-5e6f7a8b.tar.gz":  "newest",
		"gordon/apps/forgejo/volumes/postgres-data/20261016T013000Z-0a0b0c0d.tar.gz": "pg",
		"gordon/apps/ermite/volumes/forgejo-data/20261015T013000Z-9a9b9c9d.tar.gz":   "other-app",
	} {
		client.objects[key] = []byte(data)
	}

	jobs, err := storage.LatestVolumeArchives(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, client.heads)

	got := map[string]domain.VolumeBackupJob{}
	for _, job := range jobs {
		got[job.App+"/"+job.VolumeName] = job
	}
	require.Len(t, got, 3)
	newest := got["forgejo/forgejo-data"]
	assert.Equal(t, "5e6f7a8b", newest.ID)
	assert.Equal(t, int64(len("newest")), newest.SizeBytes)
	assert.Equal(t, time.Date(2026, 10, 18, 1, 30, 0, 0, time.UTC), newest.StartedAt)
	assert.Contains(t, got, "forgejo/postgres-data")
	assert.Contains(t, got, "ermite/forgejo-data")
}

func TestVolumeBackupStorageRetentionUsesParsedKeyTimestamp(t *testing.T) {
	client := newFakeVolumeS3Client()
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{S3Bucket: "bucket"}, client, &fakeVolumeUploader{client: client})
	keys := []string{
		"apps/app.example.com/volumes/gordon-data/20260619T010000Z-a.tar.zst",
		"apps/app.example.com/volumes/gordon-data/20260619T020000Z-b.tar.zst",
		"apps/app.example.com/volumes/gordon-data/20260619T030000Z-c.tar.zst",
		"apps/app.example.com/volumes/gordon-data/not-a-backup.tar.zst",
	}
	for _, key := range keys {
		client.objects[key] = []byte("x")
	}

	deleted, err := storage.ApplyVolumeRetention(context.Background(), "app.example.com", domain.VolumeBackupRetentionPolicy{Keep: 2})
	require.NoError(t, err)

	assert.Equal(t, 1, deleted)
	assert.Equal(t, []string{"apps/app.example.com/volumes/gordon-data/20260619T010000Z-a.tar.zst"}, client.deleted)
	assert.Contains(t, client.objects, "apps/app.example.com/volumes/gordon-data/not-a-backup.tar.zst")
}

func TestVolumeBackupStorageRejectsWrongBucketArtifact(t *testing.T) {
	storage := NewVolumeBackupStorageWithClients(domain.VolumeBackupConfig{S3Bucket: "expected"}, newFakeVolumeS3Client(), nil)

	_, err := storage.GetVolumeArchive(context.Background(), "s3://other/key")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}
