package proxy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"gopkg.in/yaml.v3"
)

// BlacklistConfig represents the structure of the blacklist YAML file (.yaml or .yml)
type BlacklistConfig struct {
	IPs      []string     `yaml:"ips"`
	Ranges   []string     `yaml:"ranges"`
	Networks []*net.IPNet `yaml:"-"` // Parsed IP networks
	LastMod  time.Time    `yaml:"-"` // Last modification time
	mu       sync.RWMutex `yaml:"-"` // Mutex for thread safety
	path     string       `yaml:"-"` // Path to blacklist file
}

// NewBlacklist creates a new IP blacklist
func NewBlacklist(path string) (*BlacklistConfig, error) {
	blacklist := &BlacklistConfig{
		path: path,
	}

	// Load initial blacklist
	if err := blacklist.Load(); err != nil {
		// If file doesn't exist, create an empty blacklist
		if os.IsNotExist(err) {
			log.Info("Blacklist file not found, creating empty blacklist", "path", path)
			blacklist.IPs = []string{}
			blacklist.Ranges = []string{}
			blacklist.Networks = []*net.IPNet{}

			// Create directory if needed
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Error("Failed to create directory for blacklist", "dir", dir, "error", err)
			}

			// Write empty blacklist file
			emptyConfig := &BlacklistConfig{
				IPs:    []string{},
				Ranges: []string{},
			}
			data, _ := yaml.Marshal(emptyConfig)
			if err := os.WriteFile(path, data, 0644); err != nil {
				log.Error("Failed to create empty blacklist file", "path", path, "error", err)
			}

			return blacklist, nil
		}
		return nil, err
	}

	return blacklist, nil
}

// Load reads the blacklist from disk
func (b *BlacklistConfig) Load() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Get file info for last modified time
	fileInfo, err := os.Stat(b.path)
	if err != nil {
		return err
	}

	// Check if file hasn't been modified in the last 10 seconds
	// This ensures we check more frequently, but not on every single request
	forceReload := time.Since(b.LastMod) > 10*time.Second
	if fileInfo.ModTime() == b.LastMod && !forceReload {
		return nil // File hasn't changed and we're within the refresh window
	}

	// Read the blacklist file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Create a temporary config to unmarshal into
	var tempConfig BlacklistConfig
	if err := yaml.Unmarshal(data, &tempConfig); err != nil {
		return err
	}

	// Parse IP ranges into networks
	networks := make([]*net.IPNet, 0, len(tempConfig.Ranges))
	for _, cidr := range tempConfig.Ranges {
		// Add /32 to plain IPs if not already a CIDR
		if !strings.Contains(cidr, "/") {
			cidr = cidr + "/32"
		}

		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Warn("Invalid CIDR in blacklist", "cidr", cidr, "error", err)
			continue
		}
		networks = append(networks, network)
	}

	// Update the blacklist
	b.IPs = tempConfig.IPs
	b.Ranges = tempConfig.Ranges
	b.Networks = networks
	b.LastMod = fileInfo.ModTime()

	// If the file has actually changed, log about it
	if fileInfo.ModTime() != b.LastMod {
		log.Info("Loaded blacklist",
			"ips", len(b.IPs),
			"ranges", len(b.Ranges),
			"last_modified", b.LastMod.Format(time.RFC3339))
	} else if forceReload {
		log.Debug("Reloaded blacklist (forced refresh)",
			"ips", len(b.IPs),
			"ranges", len(b.Ranges))
	}

	return nil
}

// IsBlocked checks if an IP is in the blacklist
func (b *BlacklistConfig) IsBlocked(ip string) bool {
	// Try to reload the blacklist first
	if err := b.Load(); err != nil {
		log.Error("Failed to reload blacklist", "error", err)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Check direct IP match first (fastest)
	for _, blockedIP := range b.IPs {
		if ip == blockedIP {
			return true
		}
	}

	// Parse the IP
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		log.Warn("Failed to parse IP", "ip", ip)
		return false
	}

	// Check if IP is in any blocked ranges
	for _, network := range b.Networks {
		if network.Contains(parsedIP) {
			return true
		}
	}

	return false
}

// AddIP adds an IP to the blacklist immediately
func (b *BlacklistConfig) AddIP(ip string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Read the current file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Parse the current data
	var config BlacklistConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// Check if the IP is already in the list
	for _, blockedIP := range config.IPs {
		if blockedIP == ip {
			return nil // Already in the list
		}
	}

	// Add the IP to the list
	config.IPs = append(config.IPs, ip)

	// Write the updated config back to the file
	newData, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(b.path, newData, 0644); err != nil {
		return err
	}

	// Force a reload
	return b.Load()
}

// AddRange adds an IP range to the blacklist immediately
func (b *BlacklistConfig) AddRange(cidr string) error {
	// Make sure the CIDR is valid
	_, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Read the current file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Parse the current data
	var config BlacklistConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// Check if the range is already in the list
	for _, blockedRange := range config.Ranges {
		if blockedRange == cidr {
			return nil // Already in the list
		}
	}

	// Add the range to the list
	config.Ranges = append(config.Ranges, cidr)

	// Write the updated config back to the file
	newData, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(b.path, newData, 0644); err != nil {
		return err
	}

	// Force a reload
	return b.Load()
}
