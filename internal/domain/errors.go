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

	// Config errors
	ErrConfigNotFound   = errors.New("configuration not found")
	ErrInvalidConfig    = errors.New("invalid configuration")
	ErrConfigLoadFailed = errors.New("failed to load configuration")

	// Environment errors
	ErrEnvFileNotFound  = errors.New("environment file not found")
	ErrSecretNotFound   = errors.New("secret not found")
	ErrProviderNotFound = errors.New("secret provider not found")
)
