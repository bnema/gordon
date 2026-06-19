package s3

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bnema/gordon/internal/domain"
)

const volumeBackupTimestampLayout = "20060102T150405Z"

type volumeS3Client interface {
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadObject(ctx context.Context, params *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	ListObjectsV2(ctx context.Context, params *awss3.ListObjectsV2Input, optFns ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	DeleteObject(ctx context.Context, params *awss3.DeleteObjectInput, optFns ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
}

type volumeUploader interface {
	UploadObject(ctx context.Context, input *transfermanager.UploadObjectInput, opts ...func(*transfermanager.Options)) (*transfermanager.UploadObjectOutput, error)
}

// VolumeBackupStorage stores volume archive backups in S3.
type VolumeBackupStorage struct {
	bucket       string
	prefix       string
	sseAlgorithm string
	sseKMSKeyID  string
	client       volumeS3Client
	uploader     volumeUploader
}

// NewVolumeBackupStorage creates an S3-backed volume backup storage adapter.
func NewVolumeBackupStorage(ctx context.Context, cfg domain.VolumeBackupConfig) (*VolumeBackupStorage, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.S3Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
		o.UsePathStyle = cfg.S3PathStyle
	})
	uploader := transfermanager.New(client, func(o *transfermanager.Options) {
		o.Concurrency = 2
		o.PartSizeBytes = 16 * 1024 * 1024
		o.MultipartUploadThreshold = 16 * 1024 * 1024
		o.FailTimeout = time.Minute
	})

	return NewVolumeBackupStorageWithClients(cfg, client, uploader), nil
}

// NewVolumeBackupStorageWithClients creates storage with injected clients for tests.
func NewVolumeBackupStorageWithClients(cfg domain.VolumeBackupConfig, client volumeS3Client, uploader volumeUploader) *VolumeBackupStorage {
	return &VolumeBackupStorage{
		bucket:       strings.TrimSpace(cfg.S3Bucket),
		prefix:       normalizeS3Prefix(cfg.S3Prefix),
		sseAlgorithm: strings.TrimSpace(cfg.S3SSEAlgorithm),
		sseKMSKeyID:  strings.TrimSpace(cfg.S3SSEKMSKeyID),
		client:       client,
		uploader:     uploader,
	}
}

// StoreVolumeArchive uploads a volume archive stream to S3 and returns its artifact reference.
func (s *VolumeBackupStorage) StoreVolumeArchive(ctx context.Context, job domain.VolumeBackupJob, data io.Reader) (string, error) {
	if s.bucket == "" {
		return "", fmt.Errorf("s3 bucket is required")
	}
	if job.Domain == "" {
		return "", fmt.Errorf("backup domain is required")
	}
	if job.VolumeName == "" {
		return "", fmt.Errorf("backup volume name is required")
	}
	if data == nil {
		return "", fmt.Errorf("backup data is required")
	}

	key := s.objectKey(job)
	compression := ""
	if job.Metadata != nil {
		compression = job.Metadata["compression"]
	}
	input := &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        data,
		ContentType: aws.String(contentTypeForCompression(compression)),
		Metadata: map[string]string{
			"gordon-domain":      job.Domain,
			"gordon-volume":      job.VolumeName,
			"gordon-container":   job.ContainerName,
			"gordon-mount-path":  job.MountPath,
			"gordon-compression": compression,
		},
	}
	if s.sseAlgorithm != "" {
		input.ServerSideEncryption = tmtypes.ServerSideEncryption(s.sseAlgorithm)
	}
	if s.sseKMSKeyID != "" {
		input.SSEKMSKeyID = aws.String(s.sseKMSKeyID)
	}

	if _, err := s.uploader.UploadObject(ctx, input); err != nil {
		return "", fmt.Errorf("failed to upload volume archive: %w", err)
	}

	return s.artifactRef(key), nil
}

// GetVolumeArchive retrieves a volume archive by artifact reference.
func (s *VolumeBackupStorage) GetVolumeArchive(ctx context.Context, artifactRef string) (io.ReadCloser, error) {
	key, err := s.keyFromArtifactRef(artifactRef)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get volume archive: %w", err)
	}
	return out.Body, nil
}

// ListVolumeArchives lists completed volume archive backups for a domain.
func (s *VolumeBackupStorage) ListVolumeArchives(ctx context.Context, domainName string) ([]domain.VolumeBackupJob, error) {
	listPrefix := s.domainPrefix(domainName)
	jobs := make([]domain.VolumeBackupJob, 0)
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(listPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list volume archives: %w", err)
		}
		for _, obj := range out.Contents {
			if obj.Key == nil {
				continue
			}
			job, ok := s.jobFromKey(domainName, *obj.Key)
			if !ok {
				continue
			}
			if obj.Size != nil {
				job.SizeBytes = *obj.Size
			}
			s.hydrateVolumeBackupJobMetadata(ctx, &job, *obj.Key)
			jobs = append(jobs, job)
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		token = out.NextContinuationToken
	}

	sortVolumeBackupJobs(jobs)
	return jobs, nil
}

// DeleteVolumeArchive deletes a volume archive by artifact reference.
func (s *VolumeBackupStorage) hydrateVolumeBackupJobMetadata(ctx context.Context, job *domain.VolumeBackupJob, key string) {
	out, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil || out == nil || out.Metadata == nil {
		return
	}
	job.ContainerName = out.Metadata["gordon-container"]
	job.MountPath = out.Metadata["gordon-mount-path"]
	if compression := out.Metadata["gordon-compression"]; compression != "" {
		if job.Metadata == nil {
			job.Metadata = make(map[string]string)
		}
		job.Metadata["compression"] = compression
	}
}

func (s *VolumeBackupStorage) DeleteVolumeArchive(ctx context.Context, artifactRef string) error {
	key, err := s.keyFromArtifactRef(artifactRef)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete volume archive: %w", err)
	}
	return nil
}

// ApplyVolumeRetention deletes old completed volume archives according to the keep count.
func (s *VolumeBackupStorage) ApplyVolumeRetention(ctx context.Context, domainName string, policy domain.VolumeBackupRetentionPolicy) (int, error) {
	if policy.Keep < 0 {
		return 0, fmt.Errorf("volume backup retention keep cannot be negative")
	}
	if policy.Keep == 0 {
		return 0, nil
	}

	jobs, err := s.ListVolumeArchives(ctx, domainName)
	if err != nil {
		return 0, err
	}

	byVolume := make(map[string][]domain.VolumeBackupJob)
	for _, job := range jobs {
		byVolume[job.VolumeName] = append(byVolume[job.VolumeName], job)
	}

	deleted := 0
	for _, group := range byVolume {
		sortVolumeBackupJobs(group)
		for i := policy.Keep; i < len(group); i++ {
			if err := s.DeleteVolumeArchive(ctx, group[i].ArtifactRef); err != nil {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, nil
}

func (s *VolumeBackupStorage) objectKey(job domain.VolumeBackupJob) string {
	started := job.StartedAt.UTC()
	if started.IsZero() {
		started = time.Now().UTC()
	}
	id := job.ID
	if id == "" {
		id = randomObjectSuffix()
	}

	ext := "tar.zst"
	if job.Metadata != nil && job.Metadata["compression"] == string(domain.VolumeBackupCompressionGzip) {
		ext = "tar.gz"
	}
	fileName := fmt.Sprintf("%s-%s.%s", started.Format(volumeBackupTimestampLayout), sanitizeS3KeyComponent(id), ext)
	parts := []string{s.prefix, "domains", sanitizeS3KeyComponent(job.Domain), "volumes", sanitizeS3KeyComponent(job.VolumeName), fileName}
	return joinS3Key(parts...)
}

func (s *VolumeBackupStorage) domainPrefix(domainName string) string {
	if strings.TrimSpace(domainName) == "" {
		return joinS3Key(s.prefix, "domains") + "/"
	}
	return joinS3Key(s.prefix, "domains", sanitizeS3KeyComponent(domainName), "volumes") + "/"
}

func (s *VolumeBackupStorage) artifactRef(key string) string {
	return "s3://" + s.bucket + "/" + key
}

func (s *VolumeBackupStorage) keyFromArtifactRef(artifactRef string) (string, error) {
	if strings.HasPrefix(artifactRef, "s3://") {
		u, err := url.Parse(artifactRef)
		if err != nil {
			return "", fmt.Errorf("invalid s3 artifact ref: %w", err)
		}
		if u.Host != s.bucket {
			return "", fmt.Errorf("artifact bucket %q does not match configured bucket", u.Host)
		}
		return strings.TrimPrefix(u.Path, "/"), nil
	}
	if strings.TrimSpace(artifactRef) == "" {
		return "", fmt.Errorf("artifact ref is required")
	}
	return strings.TrimPrefix(artifactRef, "/"), nil
}

func (s *VolumeBackupStorage) jobFromKey(domainName, key string) (domain.VolumeBackupJob, bool) {
	var rel string
	if strings.TrimSpace(domainName) == "" {
		rel = strings.TrimPrefix(key, joinS3Key(s.prefix, "domains")+"/")
		parts := strings.Split(rel, "/")
		if len(parts) != 4 || parts[1] != "volumes" {
			return domain.VolumeBackupJob{}, false
		}
		domainName = parts[0]
		rel = strings.Join(parts[2:], "/")
	} else {
		rel = strings.TrimPrefix(key, joinS3Key(s.prefix, "domains", sanitizeS3KeyComponent(domainName), "volumes")+"/")
	}
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		return domain.VolumeBackupJob{}, false
	}
	volumeName := parts[0]
	fileName := parts[1]
	if volumeName == "" || fileName == "" {
		return domain.VolumeBackupJob{}, false
	}
	started, id, ok := parseVolumeBackupFileName(fileName)
	if !ok {
		return domain.VolumeBackupJob{}, false
	}
	return domain.VolumeBackupJob{
		ID:          id,
		Domain:      domainName,
		VolumeName:  volumeName,
		Type:        domain.BackupTypeVolumeArchive,
		Status:      domain.BackupStatusCompleted,
		StartedAt:   started,
		CompletedAt: started,
		ArtifactRef: s.artifactRef(key),
	}, true
}

func parseVolumeBackupFileName(fileName string) (time.Time, string, bool) {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(fileName, ".tar.zst"), ".tar.gz")
	if trimmed == fileName {
		return time.Time{}, "", false
	}
	timestampPart, id, ok := strings.Cut(trimmed, "-")
	if !ok {
		return time.Time{}, "", false
	}
	started, err := time.Parse(volumeBackupTimestampLayout, timestampPart)
	if err != nil {
		return time.Time{}, "", false
	}
	return started.UTC(), id, true
}

func sortVolumeBackupJobs(jobs []domain.VolumeBackupJob) {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.After(jobs[j].StartedAt)
		}
		return jobs[i].ArtifactRef < jobs[j].ArtifactRef
	})
}

func normalizeS3Prefix(prefix string) string {
	return strings.Trim(strings.TrimSpace(prefix), "/")
}

func joinS3Key(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part != "" {
			clean = append(clean, part)
		}
	}
	return path.Join(clean...)
}

func sanitizeS3KeyComponent(input string) string {
	clean := strings.TrimSpace(input)
	if clean == "" || strings.Trim(clean, ".") == "" {
		return "unknown"
	}
	clean = strings.Trim(clean, ".")
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(clean)
}

func contentTypeForCompression(compression string) string {
	switch compression {
	case string(domain.VolumeBackupCompressionGzip):
		return "application/gzip"
	case string(domain.VolumeBackupCompressionZstd):
		return "application/zstd"
	default:
		return "application/octet-stream"
	}
}

func randomObjectSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
