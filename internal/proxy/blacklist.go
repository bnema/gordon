package proxy

import (
	"net"
	"os"
	"path/filepath"
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

	// Check if file hasn't been modified
	if fileInfo.ModTime() == b.LastMod {
		return nil // File hasn't changed
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

	log.Info("Loaded blacklist",
		"ips", len(b.IPs),
		"ranges", len(b.Ranges),
		"last_modified", b.LastMod.Format(time.RFC3339))

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
