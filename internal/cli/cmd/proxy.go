package cmd

import (
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/cli/handler"
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

			// Register the server instance for global signal handling
			handler.SetGlobalServer(a)

			// Create a new proxy instance
			p, err := proxy.NewProxy(a)
			if err != nil {
				log.Fatal("Failed to create proxy:", err)
			}

			// If update flag is set, update a container's IP address
			if update {
				if containerID == "" || containerIP == "" {
					log.Fatal("Container ID and IP address are required for update")
				}

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

				// Update the route
				err = p.ForceUpdateRouteIP(domain, containerIP)
				if err != nil {
					log.Fatal("Failed to update route IP:", err)
				}

				log.Info("Route IP updated successfully")

				// Close the database connection
				if err := a.Shutdown(); err != nil {
					log.Error("Error closing database", "error", err)
				}
				return
			}

			// If remove flag is set, remove a route
			if remove {
				if domain == "" {
					log.Fatal("Domain is required for removal")
				}
				if err := p.RemoveRoute(domain); err != nil {
					log.Fatal("Failed to remove route:", err)
				}
				log.Info("Route removed successfully")

				// Close the database connection
				if err := a.Shutdown(); err != nil {
					log.Error("Error closing database", "error", err)
				}
				return
			}

			// If domain and containerID are provided, add a route
			if domain != "" && containerID != "" {
				if containerPort == "" {
					log.Fatal("Container port is required")
				}
				if protocol == "" {
					protocol = "http"
				}
				if err := p.AddRoute(domain, containerID, containerIP, containerPort, protocol, path); err != nil {
					log.Fatal("Failed to add route:", err)
				}
				log.Info("Route added successfully")

				// Close the database connection
				if err := a.Shutdown(); err != nil {
					log.Error("Error closing database", "error", err)
				}
				return
			}

			// If no flags are set, list all routes
			if err := p.Reload(); err != nil {
				log.Fatal("Failed to load routes:", err)
			}

			// List routes from the database
			// Use the query from the instantiated proxy queries
			rows, err := a.DB.Query(p.Queries.GetAllRoutes)
			if err != nil {
				log.Fatal("Failed to query routes:", err)
			}
			defer rows.Close()

			fmt.Println("--- Proxy Routes ---")
			fmt.Printf("%-30s %-12s %-15s %-6s %-7s\n", "DOMAIN", "CONTAINER ID", "IP", "PORT", "ACTIVE")
			fmt.Println(strings.Repeat("-", 75))

			var count int
			for rows.Next() {
				// Adjust variables to match the columns selected by GetAllRoutes
				var id, domain, containerID, containerIP, containerPort, protocol, path string
				var active bool
				// Adjust scan targets
				if err := rows.Scan(&id, &domain, &containerID, &containerIP, &containerPort, &protocol, &path, &active); err != nil {
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

			// Close the database connection
			if err := a.Shutdown(); err != nil {
				log.Error("Error closing database", "error", err)
			}
		},
	}

	proxyCmd.Flags().StringVarP(&domain, "domain", "d", "", "Domain name")
	proxyCmd.Flags().StringVarP(&containerID, "container", "c", "", "Container ID")
	proxyCmd.Flags().StringVarP(&containerIP, "ip", "i", "", "Container IP address")
	proxyCmd.Flags().StringVarP(&containerPort, "port", "p", "", "Container port")
	proxyCmd.Flags().StringVarP(&protocol, "protocol", "t", "", "Protocol (http or https)")
	proxyCmd.Flags().StringVarP(&path, "path", "a", "", "Path prefix")
	proxyCmd.Flags().BoolVarP(&remove, "remove", "r", false, "Remove a route")
	proxyCmd.Flags().BoolVarP(&update, "update", "u", false, "Update a container's IP address")
	proxyCmd.Flags().BoolVarP(&force, "force", "f", false, "Force removal of a route")

	return proxyCmd
}
