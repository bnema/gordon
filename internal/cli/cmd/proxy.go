package cmd

import (
	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/server"
	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"
)

// NewProxyCommand creates a new proxy command
func NewProxyCommand(a *server.App) *cobra.Command {
	var (
		domain        string
		containerID   string
		containerIP   string
		containerPort string
		protocol      string
		path          string
		remove        bool
	)

	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the reverse proxy routes",
		Long:  "Add, remove, or list proxy routes for domains to containers",
		Run: func(cmd *cobra.Command, args []string) {
			// Initialize the database if not already initialized
			_, err := server.InitializeDB(a)
			if err != nil {
				log.Fatal("Failed to initialize database:", err)
			}

			// Create a new proxy instance
			p, err := proxy.NewProxy(a)
			if err != nil {
				log.Fatal("Failed to create proxy:", err)
			}

			// If remove flag is set, remove the route
			if remove {
				if domain == "" {
					log.Fatal("Domain is required for removing a route")
				}

				if err := p.RemoveRoute(domain); err != nil {
					log.Fatal("Failed to remove route:", err)
				}

				log.Info("Route removed successfully", "domain", domain)
				return
			}

			// If domain is set, add a new route
			if domain != "" {
				// Validate required fields
				if containerID == "" || containerIP == "" || containerPort == "" {
					log.Fatal("Container ID, IP, and port are required for adding a route")
				}

				// Set defaults for optional fields
				if protocol == "" {
					protocol = "http"
				}

				if path == "" {
					path = "/"
				}

				// Add the route
				if err := p.AddRoute(domain, containerID, containerIP, containerPort, protocol, path); err != nil {
					log.Fatal("Failed to add route:", err)
				}

				log.Info("Route added successfully",
					"domain", domain,
					"containerIP", containerIP,
					"containerPort", containerPort,
				)
				return
			}

			// If no flags are set, list all routes
			if err := p.Reload(); err != nil {
				log.Fatal("Failed to load routes:", err)
			}

			// List routes from the database
			rows, err := a.DB.Query(`
				SELECT d.name, pr.container_id, pr.container_ip, pr.container_port, pr.active
				FROM proxy_route pr
				JOIN domain d ON pr.domain_id = d.id
			`)
			if err != nil {
				log.Fatal("Failed to query routes:", err)
			}
			defer rows.Close()

			var count int
			for rows.Next() {
				var domain, containerID, containerIP, containerPort string
				var active bool
				if err := rows.Scan(&domain, &containerID, &containerIP, &containerPort, &active); err != nil {
					log.Fatal("Failed to scan row:", err)
				}

				log.Info("Route",
					"domain", domain,
					"containerIP", containerIP,
					"containerPort", containerPort,
					"active", active,
				)
				count++
			}

			log.Info("Proxy routes loaded", "count", count)
		},
	}

	// Add flags
	proxyCmd.Flags().StringVarP(&domain, "domain", "d", "", "Domain name for the route")
	proxyCmd.Flags().StringVarP(&containerID, "container", "c", "", "Container ID")
	proxyCmd.Flags().StringVarP(&containerIP, "ip", "i", "", "Container IP address")
	proxyCmd.Flags().StringVarP(&containerPort, "port", "p", "", "Container port")
	proxyCmd.Flags().StringVarP(&protocol, "protocol", "t", "http", "Protocol (http or https)")
	proxyCmd.Flags().StringVarP(&path, "path", "a", "/", "Path prefix to route")
	proxyCmd.Flags().BoolVarP(&remove, "remove", "r", false, "Remove the route for the specified domain")

	return proxyCmd
}
