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

	// In-memory cache of blocked IPs for quick lookups
	blockedCache map[string]bool `yaml:"-"`
}

// MarshalableConfig is a copy of BlacklistConfig without the mutex and other non-serializable fields
// Used to prevent copying mutex when marshaling to YAML
type MarshalableConfig struct {
	IPs    []string `yaml:"ips"`
	Ranges []string `yaml:"ranges"`
}

// NewBlacklist creates a new BlacklistConfig from a file
func NewBlacklist(path string) (*BlacklistConfig, error) {
	// Create the blacklist config
	blacklist := &BlacklistConfig{
		path:         path,
		blockedCache: make(map[string]bool),
	}

	// Create the blacklist file if it doesn't exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Create parent directory if it doesn't exist
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory for blacklist: %w", err)
			}
		}

		// Create the blacklist file with default empty structure
		data, err := yaml.Marshal(MarshalableConfig{
			IPs:    []string{},
			Ranges: []string{},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal default blacklist: %w", err)
		}

		if err := os.WriteFile(path, data, 0644); err != nil {
			return nil, fmt.Errorf("failed to create blacklist file: %w", err)
		}

		log.Info("Created new blacklist file", "path", path)
	}

	// Load the blacklist
	if err := blacklist.Load(); err != nil {
		return nil, fmt.Errorf("failed to load blacklist: %w", err)
	}

	log.Info("Blacklist loaded",
		"ips", len(blacklist.IPs),
		"ranges", len(blacklist.Ranges))

	return blacklist, nil
}

// Load reloads the blacklist from the file if it has changed
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
	var tempConfig MarshalableConfig
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

	// Rebuild the blocked cache
	b.rebuildBlockedCache()

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

// rebuildBlockedCache rebuilds the in-memory cache of blocked IPs for quick lookups
func (b *BlacklistConfig) rebuildBlockedCache() {
	// Create a new map to hold all blocked IPs
	newCache := make(map[string]bool, len(b.IPs))

	// Add direct IPs
	for _, ip := range b.IPs {
		newCache[ip] = true
	}

	// Replace the cache atomically
	b.blockedCache = newCache

	log.Debug("Rebuilt blocked IP cache", "direct_ips", len(b.blockedCache))
}

// IsBlocked checks if an IP is in the blacklist
func (b *BlacklistConfig) IsBlocked(ip string) bool {
	// Try to reload the blacklist first
	if err := b.Load(); err != nil {
		log.Error("Failed to reload blacklist", "error", err)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	// Fast path: Check direct IP match in cache first (fastest)
	if b.blockedCache[ip] {
		return true
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
			// Add to cache for faster lookups next time
			// This is safe because we have a read lock and the map is only replaced, not modified
			b.blockedCache[ip] = true
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
	var config MarshalableConfig
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

	// Add to in-memory cache immediately
	b.blockedCache[ip] = true
	log.Info("Added IP to blacklist", "ip", ip)

	// Force a reload
	return b.Load()
}

// AddRange adds an IP range to the blacklist immediately
func (b *BlacklistConfig) AddRange(cidr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Validate the CIDR
	if !strings.Contains(cidr, "/") {
		cidr = cidr + "/32" // Add /32 for single IPs
	}

	_, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR format: %w", err)
	}

	// Read the current file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Parse the current data
	var config MarshalableConfig
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

	log.Info("Added CIDR range to blacklist", "cidr", cidr)

	// Force a reload to update Networks and the cache
	return b.Load()
}

// RemoveIP removes an IP from the blacklist
func (b *BlacklistConfig) RemoveIP(ip string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Read the current file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Parse the current data
	var config MarshalableConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// Check if the IP is in the list
	found := false
	newIPs := make([]string, 0, len(config.IPs))
	for _, blockedIP := range config.IPs {
		if blockedIP == ip {
			found = true
			continue // Skip this IP
		}
		newIPs = append(newIPs, blockedIP)
	}

	if !found {
		return fmt.Errorf("IP not found in blacklist: %s", ip)
	}

	// Update the list
	config.IPs = newIPs

	// Write the updated config back to the file
	newData, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(b.path, newData, 0644); err != nil {
		return err
	}

	// Remove from cache
	delete(b.blockedCache, ip)
	log.Info("Removed IP from blacklist", "ip", ip)

	// Force a reload
	return b.Load()
}

// RemoveRange removes an IP range from the blacklist
func (b *BlacklistConfig) RemoveRange(cidr string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Normalize CIDR
	if !strings.Contains(cidr, "/") {
		cidr = cidr + "/32" // Add /32 for single IPs
	}

	// Read the current file
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}

	// Parse the current data
	var config MarshalableConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return err
	}

	// Check if the range is in the list
	found := false
	newRanges := make([]string, 0, len(config.Ranges))
	for _, blockedRange := range config.Ranges {
		if blockedRange == cidr {
			found = true
			continue // Skip this range
		}
		newRanges = append(newRanges, blockedRange)
	}

	if !found {
		return fmt.Errorf("range not found in blacklist: %s", cidr)
	}

	// Update the list
	config.Ranges = newRanges

	// Write the updated config back to the file
	newData, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(b.path, newData, 0644); err != nil {
		return err
	}

	log.Info("Removed CIDR range from blacklist", "cidr", cidr)

	// Force a reload to rebuild Networks and the cache
	return b.Load()
}
