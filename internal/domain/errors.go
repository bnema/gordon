package domain

import "errors"

// Domain errors represent business-level errors that can occur in the system.
// These errors are used across layers to communicate specific failure conditions.
var (
	// Container errors
	ErrContainerNotFound   = errors.New("container not found")
	ErrContainerExists     = errors.New("container already exists")
	ErrContainerNotRunning = errors.New("container is not running")
	ErrContainerRunning    = errors.New("container is already running")

	// Image errors
	ErrImageNotFound      = errors.New("image not found")
	ErrImagePullFailed    = errors.New("failed to pull image")
	ErrInvalidImageFormat = errors.New("invalid image format")

	// Route errors
	ErrRouteNotFound     = errors.New("route not found")
	ErrRouteExists       = errors.New("route already exists")
	ErrInvalidRoute      = errors.New("invalid route configuration")
	ErrRouteDomainEmpty  = errors.New("route domain cannot be empty")
	ErrRouteImageEmpty   = errors.New("route image cannot be empty")
	ErrNoTargetAvailable = errors.New("no target available for route")

	// Registry errors
	ErrManifestNotFound = errors.New("manifest not found")
	ErrBlobNotFound     = errors.New("blob not found")
	ErrUploadNotFound   = errors.New("upload not found")
	ErrInvalidDigest    = errors.New("invalid digest")
	ErrDigestMismatch   = errors.New("digest mismatch")
	ErrUnauthorized     = errors.New("unauthorized")

	// Network errors
	ErrNetworkNotFound = errors.New("network not found")
	ErrNetworkExists   = errors.New("network already exists")

	// Volume errors
	ErrVolumeNotFound = errors.New("volume not found")
	ErrVolumeExists   = errors.New("volume already exists")

	// Preview errors
	ErrPreviewNotFound = errors.New("preview not found")

	// Config errors
	ErrConfigNotFound       = errors.New("configuration not found")
	ErrInvalidConfig        = errors.New("invalid configuration")
	ErrConfigLoadFailed     = errors.New("failed to load configuration")
	ErrInvalidDomainPattern = errors.New("invalid domain pattern")

	// Environment errors
	ErrEnvFileNotFound      = errors.New("environment file not found")
	ErrSecretNotFound       = errors.New("secret not found")
	ErrProviderNotFound     = errors.New("secret provider not found")
	ErrInvalidContainerName = errors.New("invalid container name")

	// Authentication errors
	ErrInvalidToken       = errors.New("invalid token")
	ErrExpiredToken       = errors.New("token has expired")
	ErrRevokedToken       = errors.New("token has been revoked")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrTokenNotFound      = errors.New("token not found")

	// Security errors
	ErrPathTraversal     = errors.New("path traversal not allowed")
	ErrInvalidPath       = errors.New("invalid path format")
	ErrInsufficientScope = errors.New("insufficient scope for operation")
	ErrInvalidScope      = errors.New("invalid scope format")
	ErrSSRFBlocked       = errors.New("request to internal/blocked network not allowed")
	ErrInvalidEnvKey     = errors.New("invalid environment variable key")
	ErrLongLivedToken    = errors.New("long-lived tokens must be exchanged via /auth/token")

	// Remote errors
	ErrEmptyRemoteName       = errors.New("remote name must not be empty")
	ErrInvalidRemoteNameChar = errors.New("remote name contains invalid characters")
	ErrConsecutiveDots       = errors.New("remote name must not contain consecutive dots")
	ErrPassUnavailable       = errors.New("pass is not available")
	ErrRemoteNotFound        = errors.New("remote not found")

	// Attachment errors
	ErrAttachmentNotFound    = errors.New("attachment not found")
	ErrAttachmentExists      = errors.New("attachment already exists")
	ErrAttachmentImageEmpty  = errors.New("attachment image cannot be empty")
	ErrAttachmentTargetEmpty = errors.New("attachment target (domain or group) cannot be empty")
	ErrAttachmentNotDeployed = errors.New("configured attachment is not deployed; run gordon deploy to create it")
)
