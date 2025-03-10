package cmd

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/server"
	"github.com/bnema/gordon/pkg/docker"
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
		update        bool
		force         bool
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

			// If update flag is set, update a container's IP address
			if update {
				if domain == "" {
					log.Fatal("Domain is required for updating a route IP")
				}

				if containerIP == "" {
					log.Fatal("Container IP is required for updating a route IP")
				}

				// If force flag is not set, try to get the IP from the container ID
				if !force && containerID != "" {
					// Get container info to verify the IP
					containerInfo, err := docker.GetContainerInfo(containerID)
					if err != nil {
						log.Warn("Failed to get container info, using provided IP", "error", err)
					} else {
						// Look for the network specified in the config
						networkName := a.Config.ContainerEngine.Network
						if networkSettings, exists := containerInfo.NetworkSettings.Networks[networkName]; exists && networkSettings.IPAddress != "" {
							if networkSettings.IPAddress != containerIP {
								log.Warn("Provided IP doesn't match container's actual IP",
									"provided", containerIP,
									"actual", networkSettings.IPAddress)
								if !force {
									log.Info("Using actual container IP instead of provided IP",
										"container_id", containerID,
										"new_ip", networkSettings.IPAddress)
									containerIP = networkSettings.IPAddress
								} else {
									log.Warn("Force flag is set, using provided IP despite mismatch")
								}
							}
						}
					}
				}

				// Update the route
				err := p.ForceUpdateRouteIP(domain, containerIP)
				if err != nil {
					log.Fatal("Failed to update route IP:", err)
				}

				log.Info("Route IP updated successfully", "domain", domain, "ip", containerIP)
				return
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

			fmt.Println("--- Proxy Routes ---")
			fmt.Printf("%-30s %-12s %-15s %-6s %-7s\n", "DOMAIN", "CONTAINER ID", "IP", "PORT", "ACTIVE")
			fmt.Println(strings.Repeat("-", 75))

			var count int
			for rows.Next() {
				var domain, containerID, containerIP, containerPort string
				var active bool
				if err := rows.Scan(&domain, &containerID, &containerIP, &containerPort, &active); err != nil {
					log.Fatal("Failed to scan row:", err)
				}

				// Format container ID to show just first 12 chars
				shortID := containerID
				if len(containerID) > 12 {
					shortID = containerID[:12]
				}

				// Format active status
				activeStr := "✓"
				if !active {
					activeStr = "✗"
				}

				fmt.Printf("%-30s %-12s %-15s %-6s %-7s\n",
					domain, shortID, containerIP, containerPort, activeStr)
				count++
			}

			fmt.Println(strings.Repeat("-", 75))
			fmt.Printf("Total routes: %d\n", count)
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
	proxyCmd.Flags().BoolVarP(&update, "update", "u", false, "Update the IP address for an existing route")
	proxyCmd.Flags().BoolVarP(&force, "force", "f", false, "Force update with provided IP even if it doesn't match container's actual IP")

	return proxyCmd
}
