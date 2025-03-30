package common

import (
	"os"
	"strconv"
	"strings"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
)

// loadConfigFromEnv loads configuration from environment variables
func loadConfigFromEnv(config *Config, printLogs bool) {
	// General Configuration
	if val := os.Getenv("GORDON_STORAGE_DIR"); val != "" {
		config.General.StorageDir = val
		if printLogs {
			logger.Info("Using environment variable GORDON_STORAGE_DIR", "value", val)
		}
	}
	// Removed GORDON_TOKEN handling
	if val := os.Getenv("GORDON_LOG_LEVEL"); val != "" {
		config.General.LogLevel = val
		if printLogs {
			logger.Info("Using environment variable GORDON_LOG_LEVEL", "value", val)
		}
	}

	// HTTP Configuration
	if val := os.Getenv("GORDON_HTTP_PORT"); val != "" {
		config.Http.Port = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_PORT", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_DOMAIN"); val != "" {
		config.Http.Domain = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_DOMAIN", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_SUBDOMAIN"); val != "" {
		config.Http.SubDomain = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_SUBDOMAIN", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_BACKEND_URL"); val != "" {
		config.Http.BackendURL = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_BACKEND_URL", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_HTTPS"); val != "" {
		config.Http.Https = strings.ToLower(val) == "true"
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_HTTPS", "value", config.Http.Https)
		}
	}

	// Admin Configuration
	if val := os.Getenv("GORDON_ADMIN_PATH"); val != "" {
		config.Admin.Path = val
		if printLogs {
			logger.Info("Using environment variable GORDON_ADMIN_PATH", "value", val)
		}
	}

	// Container Engine Configuration
	if val := os.Getenv("GORDON_CONTAINER_SOCK"); val != "" {
		config.ContainerEngine.Sock = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_SOCK", "value", val)
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMANSOCK"); val != "" {
		config.ContainerEngine.PodmanSock = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_PODMANSOCK", "value", val)
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMAN"); val != "" {
		config.ContainerEngine.Podman = strings.ToLower(val) == "true"
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_PODMAN", "value", config.ContainerEngine.Podman)
		}
	} else {
		// Auto-detect Podman if not specified in environment
		isPodman, podmanSocket := docker.DetectPodman()
		if isPodman {
			config.ContainerEngine.Podman = true
			config.ContainerEngine.PodmanSock = podmanSocket
			if printLogs {
				logger.Info("Automatically detected Podman installation",
					"using_podman", true,
					"socket", podmanSocket)
			}
		}

		// If we're in a container, check for special socket naming patterns
		if docker.IsRunningInContainer() && fileExists(config.ContainerEngine.Sock) {
			// Check if socket path contains "podman" which indicates it might be a podman socket
			if strings.Contains(config.ContainerEngine.Sock, "podman") {
				config.ContainerEngine.Podman = true
				if printLogs {
					logger.Info("Socket path contains 'podman', enabling Podman mode", "path", config.ContainerEngine.Sock)
				}
			}
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_NETWORK"); val != "" {
		config.ContainerEngine.Network = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_NETWORK", "value", val)
		}
	}

	// Use Podman socket if Podman is enabled and no Docker socket is specified
	if config.ContainerEngine.Podman && config.ContainerEngine.Sock == "" && config.ContainerEngine.PodmanSock != "" {
		config.ContainerEngine.Sock = config.ContainerEngine.PodmanSock
		if printLogs {
			logger.Info("Setting ContainerEngine.Sock to PodmanSock value", "value", config.ContainerEngine.Sock)
		}
	}

	// Additional debug info for container sockets
	if docker.IsRunningInContainer() {
		logger.Debug("Container socket configuration",
			"sock", config.ContainerEngine.Sock,
			"podmansock", config.ContainerEngine.PodmanSock,
			"podman", config.ContainerEngine.Podman,
			"sock_exists", fileExists(config.ContainerEngine.Sock),
			"podmansock_exists", fileExists(config.ContainerEngine.PodmanSock))

		// Special handling for container environment to ensure sockets are properly set
		// If no socket is configured but default socket path exists, use it
		if config.ContainerEngine.Sock == "" && fileExists(sock) {
			config.ContainerEngine.Sock = sock
			logger.Info("Found default Docker socket in container, using it", "path", sock)
		}

		// If podman socket exists but podman mode is not enabled, check if we should enable it
		if fileExists(podmansock) && !config.ContainerEngine.Podman {
			// If the main socket path contains "podman", enable podman mode
			if strings.Contains(config.ContainerEngine.Sock, "podman") {
				config.ContainerEngine.Podman = true
				logger.Info("Socket path suggests Podman usage, enabling Podman mode", "path", config.ContainerEngine.Sock)
			}
		}
	}

	// Reverse Proxy Configuration
	if val := os.Getenv("GORDON_PROXY_PORT"); val != "" {
		config.ReverseProxy.Port = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_PORT", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_HTTP_PORT"); val != "" {
		config.ReverseProxy.HttpPort = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_HTTP_PORT", "value", val)
		}
	}

	// New environment variables for reverse proxy port control
	if val := os.Getenv("GORDON_USE_FALLBACK_BINDING"); val != "" {
		// This is handled at runtime in the proxy server but we log it here
		if printLogs {
			logger.Info("Found environment variable GORDON_USE_FALLBACK_BINDING", "value", val)
		}
	}

	// Handle GORDON_PROXY_ENABLED environment variable
	if val := os.Getenv("GORDON_PROXY_ENABLED"); val != "" {
		enabled, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.Enabled = enabled
		}
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_ENABLED", "value", config.ReverseProxy.Enabled)
		}
	}
	if val := os.Getenv("GORDON_PROXY_CERT_DIR"); val != "" {
		config.ReverseProxy.CertDir = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_CERT_DIR", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_RENEW_BEFORE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.RenewBefore = i
			if printLogs {
				logger.Info("Using environment variable GORDON_PROXY_RENEW_BEFORE", "value", i)
			}
		}
	}
	if val := os.Getenv("GORDON_PROXY_LETSENCRYPT_MODE"); val != "" {
		config.ReverseProxy.LetsEncryptMode = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_LETSENCRYPT_MODE", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_EMAIL"); val != "" {
		config.ReverseProxy.Email = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_EMAIL", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_GRACE_PERIOD"); val != "" {
		i, err := strconv.Atoi(val)
		if err == nil {
			config.ReverseProxy.GracePeriod = i
		}
		logger.Info("Using environment variable GORDON_PROXY_GRACE_PERIOD", "value", i)
	}

	// Handle GORDON_PROXY_ENABLE_HTTP_LOGS environment variable
	if val := os.Getenv("GORDON_PROXY_ENABLE_HTTP_LOGS"); val != "" {
		enableLogs, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.EnableHttpLogs = enableLogs
		}
		logger.Info("Using environment variable GORDON_PROXY_ENABLE_HTTP_LOGS", "value", config.ReverseProxy.EnableHttpLogs)
	} else if val := os.Getenv("GORDON_PROXY_ENABLE_LOGS"); val != "" {
		// For backward compatibility
		enableLogs, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.EnableHttpLogs = enableLogs
		}
		logger.Info("Using environment variable GORDON_PROXY_ENABLE_LOGS (deprecated, use GORDON_PROXY_ENABLE_HTTP_LOGS)", "value", config.ReverseProxy.EnableHttpLogs)
	}

	// Handle GORDON_PROXY_ENABLE_RATE_LIMIT environment variable
	if val := os.Getenv("GORDON_PROXY_ENABLE_RATE_LIMIT"); val != "" {
		enableRateLimit, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.EnableRateLimit = enableRateLimit
		}
		logger.Info("Using environment variable GORDON_PROXY_ENABLE_RATE_LIMIT", "value", config.ReverseProxy.EnableRateLimit)
	} else if val := os.Getenv("GORDON_PROXY_DISABLE_RATE_LIMIT"); val != "" {
		// For backward compatibility
		disableRateLimit, err := strconv.ParseBool(val)
		if err == nil {
			// Invert the logic
			config.ReverseProxy.EnableRateLimit = !disableRateLimit
		}
		logger.Info("Using environment variable GORDON_PROXY_DISABLE_RATE_LIMIT (deprecated, use GORDON_PROXY_ENABLE_RATE_LIMIT)", "value", !config.ReverseProxy.EnableRateLimit)
	}

	// Handle GORDON_PROXY_DETECT_UPSTREAM environment variable
	if val := os.Getenv("GORDON_PROXY_DETECT_UPSTREAM"); val != "" {
		detectUpstream, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.DetectUpstreamProxy = detectUpstream
		}
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_DETECT_UPSTREAM", "value", config.ReverseProxy.DetectUpstreamProxy)
		}
	}

	// Handle GORDON_PROXY_SKIP_CERTIFICATES environment variable
	if val := os.Getenv("GORDON_PROXY_SKIP_CERTIFICATES"); val != "" {
		skipCerts, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.SkipCertificates = skipCerts
		}
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_SKIP_CERTIFICATES", "value", config.ReverseProxy.SkipCertificates)
		}
	}

	// Handle GORDON_PROXY_BLOCK_DIRECT_IP environment variable
	if val := os.Getenv("GORDON_PROXY_BLOCK_DIRECT_IP"); val != "" {
		blockDirectIP, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.BlockDirectIP = blockDirectIP
		}
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_BLOCK_DIRECT_IP", "value", config.ReverseProxy.BlockDirectIP)
		}
	}
}
