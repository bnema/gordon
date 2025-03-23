// the serve command is used to start the gordon server
// Path: internal/cli/cmd/serve.go
package cmd

import (
	"fmt"
	"os"

	"github.com/bnema/gordon/internal/cli/handler"
	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/spf13/cobra"
)

// NewServeCommand creates a new serve command
func NewServeCommand(a *server.App) *cobra.Command {
	defaultport := "1323"
	var port string
	var proxyEnabled bool
	var proxyPort string
	var proxyHttpPort string
	var skipCertificates bool
	var proxyDebug bool
	var useFallbackBinding bool
	var detectUpstreamProxy bool

	// if the server is running in a docker container, the default port is 8080
	// (changed from 80 to avoid conflict with the reverse proxy)
	if docker.IsRunningInContainer() {
		defaultport = "8080"
	}

	// if no flags -p or --port are specified, the default port is used
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start a new Gordon server instance",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			// Initialize Docker client here, before starting the server
			if err := common.DockerInit(&a.Config.ContainerEngine); err != nil {
				fmt.Printf("Warning: %s\n", err)
				fmt.Println("Continuing with Docker/Podman functionality disabled")
			}

			// Only use the port from flag if it was explicitly set by the user
			// otherwise keep the one from configuration (which has env vars applied)
			if cmd.Flags().Changed("port") {
				logger.Info("Using port from command line flag", "port", port)
				a.Config.Http.Port = port
			} else {
				// Use the port from the config (which would have env vars applied)
				port = a.Config.Http.Port
				logger.Info("Using port from configuration", "port", port)
			}

			// Apply proxy flags if set
			if cmd.Flags().Changed("proxy-enabled") {
				logger.Info("Setting proxy enabled flag from command line", "enabled", proxyEnabled)
				a.Config.ReverseProxy.Enabled = proxyEnabled
			}

			if cmd.Flags().Changed("proxy-port") {
				logger.Info("Setting proxy port from command line", "port", proxyPort)
				a.Config.ReverseProxy.Port = proxyPort
			}

			if cmd.Flags().Changed("proxy-http-port") {
				logger.Info("Setting proxy HTTP port from command line", "port", proxyHttpPort)
				a.Config.ReverseProxy.HttpPort = proxyHttpPort
			}

			if cmd.Flags().Changed("skip-certificates") {
				logger.Info("Setting skip certificates flag from command line", "skip", skipCertificates)
				a.Config.ReverseProxy.SkipCertificates = skipCertificates
			}

			if cmd.Flags().Changed("proxy-debug") && proxyDebug {
				logger.Info("Enabling proxy debug mode")
				// Set log level to debug for proxy-related components
				logger.GetLogger().SetLogLevel("debug")
			}

			if cmd.Flags().Changed("use-fallback-binding") {
				logger.Info("Setting use fallback binding flag", "enabled", useFallbackBinding)
				if useFallbackBinding {
					os.Setenv("GORDON_USE_FALLBACK_BINDING", "true")
				}
			}

			if cmd.Flags().Changed("detect-upstream-proxy") {
				logger.Info("Setting detect upstream proxy flag", "enabled", detectUpstreamProxy)
				a.Config.ReverseProxy.DetectUpstreamProxy = detectUpstreamProxy
			}

			err := handler.StartServer(a, port)
			if err != nil {
				logger.Error("Server error", "error", err)
				// Ensure database is closed on error
				if err := a.Shutdown(); err != nil {
					logger.Error("Error during shutdown after server error", "error", err)
				}
				panic(err)
			}
		},
	}

	// Attach the flag to serveCmd and store its value in the variable port
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultport, "port to listen on")

	// Add proxy-related flags
	serveCmd.Flags().BoolVar(&proxyEnabled, "proxy-enabled", true, "enable or disable the reverse proxy")
	serveCmd.Flags().StringVar(&proxyPort, "proxy-port", "443", "port for the reverse proxy HTTPS server")
	serveCmd.Flags().StringVar(&proxyHttpPort, "proxy-http-port", "80", "port for the reverse proxy HTTP server")
	serveCmd.Flags().BoolVar(&skipCertificates, "skip-certificates", false, "skip Let's Encrypt certificate acquisition")
	serveCmd.Flags().BoolVar(&proxyDebug, "proxy-debug", false, "enable detailed debug logging for the proxy")
	serveCmd.Flags().BoolVar(&useFallbackBinding, "use-fallback-binding", false, "use fallback binding method for HTTPS ports")
	serveCmd.Flags().BoolVar(&detectUpstreamProxy, "detect-upstream-proxy", false, "detect and handle upstream TLS termination proxies")

	return serveCmd
}
