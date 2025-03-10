package kv

import (
	"encoding/json"
	"time"

	"github.com/charmbracelet/log"
	"github.com/starskey-io/starskey"
)

type RateLimitInfo struct {
	Attempts  int       `json:"attempts"`
	ResetTime time.Time `json:"reset_time"`
	Jailed    bool      `json:"jailed"`
}

type StarskeyRateLimiterStore struct {
	db          *starskey.Starskey
	rate        float64
	burst       int
	expiresIn   time.Duration
	stopReports chan struct{} // Channel to signal stopping the report goroutine
}

// NewStarskeyRateLimiterStore creates a new rate limiter store backed by Starskey
func NewStarskeyRateLimiterStore(dbPath string, rate float64, burst int, expiresIn time.Duration) (*StarskeyRateLimiterStore, error) {
	// Configure and open Starskey
	db, err := starskey.Open(&starskey.Config{
		Permission:        0755,
		Directory:         dbPath,
		FlushThreshold:    64 * 1024 * 1024, // 64MB - adjust based on expected traffic
		MaxLevel:          3,
		SizeFactor:        10,
		BloomFilter:       true,  // Enable for faster lookups
		SuRF:              false, // No need for range queries in rate limiting
		Logging:           true,
		Compression:       true,
		CompressionOption: starskey.SnappyCompression,
	})

	if err != nil {
		return nil, err
	}

	log.Info("Initialized rate limiter with Starskey backend",
		"path", dbPath,
		"rate", rate,
		"burst", burst,
		"expiration", expiresIn)

	store := &StarskeyRateLimiterStore{
		db:          db,
		rate:        rate,
		burst:       burst,
		expiresIn:   expiresIn,
		stopReports: make(chan struct{}),
	}

	// Start the jailed IPs reporting
	go store.startJailReporting()

	return store, nil
}

// Implement Echo's RateLimiterStore interface
func (s *StarskeyRateLimiterStore) Allow(identifier string) (bool, error) {
	var allowed bool

	err := s.db.Update(func(txn *starskey.Txn) error {
		now := time.Now()
		key := []byte(identifier)

		// Default state for new entries
		info := RateLimitInfo{
			Attempts:  0,
			ResetTime: now,
			Jailed:    false,
		}

		// Get existing record if any
		value, err := txn.Get(key)
		if err == nil && value != nil {
			if err := json.Unmarshal(value, &info); err != nil {
				// Handle corrupted data
				info = RateLimitInfo{Attempts: 0, ResetTime: now}
			}

			// Check if jailed
			if info.Jailed && now.Before(info.ResetTime) {
				allowed = false
				return nil
			}

			// If IP was jailed but now should be freed (reset time passed)
			if info.Jailed && !now.Before(info.ResetTime) {
				log.Info("IP address released from jail", "ip", identifier)
				info.Jailed = false
			}

			// Calculate time-based allowance
			timePassed := now.Sub(info.ResetTime).Seconds()
			tokensToAdd := timePassed * s.rate

			// Reset if needed
			if now.After(info.ResetTime.Add(s.expiresIn)) {
				info.Attempts = 0
				info.ResetTime = now
			} else {
				info.Attempts = max(0, info.Attempts-int(tokensToAdd))
			}
		}

		// Check if under rate limit
		if info.Attempts < s.burst {
			info.Attempts++
			allowed = true

			// Jail if needed (e.g., if burst exceeded)
			if info.Attempts >= s.burst {
				info.Jailed = true
				info.ResetTime = now.Add(time.Second) // 1 second penalty
				log.Debug("IP jailed (burst exceeded)", "ip", identifier, "reset_at", info.ResetTime)
				// Add info level log when an IP is jailed
				log.Info("IP address jailed due to rate limit violation", "ip", identifier, "reset_at", info.ResetTime)
			} else {
				log.Debug("Request allowed", "ip", identifier, "attempts", info.Attempts)
			}

			// Save updated info
			data, err := json.Marshal(info)
			if err != nil {
				log.Debug("Failed to marshal rate limit info", "ip", identifier, "error", err)
				return err
			}

			// Fix: txn.Put doesn't return a value in Starskey
			// The errors are handled at the transaction level
			txn.Put(key, data)
			log.Debug("Rate limit info saved", "ip", identifier)
			return nil
		}

		log.Debug("Request blocked (rate limited)", "ip", identifier, "attempts", info.Attempts)
		allowed = false
		return nil
	})

	return allowed, err
}

func (s *StarskeyRateLimiterStore) Reset(identifier string) (bool, error) {
	// Check if the IP was jailed before resetting
	var wasJailed bool

	// Access data before deletion to check jail status
	value, err := s.db.Get([]byte(identifier))
	if err == nil && value != nil {
		var info RateLimitInfo
		if err := json.Unmarshal(value, &info); err == nil {
			wasJailed = info.Jailed
		}
	}

	err = s.db.Delete([]byte(identifier))

	// Log if we freed a jailed IP
	if wasJailed {
		log.Info("IP address manually reset and released from jail", "ip", identifier)
	}

	return err == nil, err
}

// GetJailedIPs returns all currently jailed IPs
func (s *StarskeyRateLimiterStore) GetJailedIPs() (map[string]time.Time, error) {
	jailed := make(map[string]time.Time)
	now := time.Now()

	// Use the FilterKeys method since Starskey doesn't have a direct iterator
	compareFunc := func(key []byte) bool {
		return true // We'll examine all keys and filter ourselves
	}

	results, err := s.db.FilterKeys(compareFunc)
	if err != nil {
		return nil, err
	}

	// Process results - each pair of entries is key, value
	for i := 0; i < len(results); i += 2 {
		if i+1 >= len(results) {
			break // Safety check
		}

		key := string(results[i])
		value := results[i+1]

		var info RateLimitInfo
		if err := json.Unmarshal(value, &info); err != nil {
			log.Debug("Failed to unmarshal rate limit info", "key", key, "error", err)
			continue // Skip invalid entries
		}

		// Add jailed IPs to the result map
		if info.Jailed && now.Before(info.ResetTime) {
			jailed[key] = info.ResetTime
		}
	}

	return jailed, nil
}

// startJailReporting starts a goroutine that logs jailed IPs count on startup and every 5 minutes
func (s *StarskeyRateLimiterStore) startJailReporting() {
	// Initial report on startup
	s.reportJailedIPs()

	// Set up ticker for periodic reporting (every 5 minutes)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.reportJailedIPs()
		case <-s.stopReports:
			log.Debug("Stopping jailed IPs reporting")
			return
		}
	}
}

// reportJailedIPs gets and logs the current jailed IPs count
func (s *StarskeyRateLimiterStore) reportJailedIPs() {
	jailed, err := s.GetJailedIPs()
	if err != nil {
		log.Error("Failed to get jailed IPs", "error", err)
		return
	}

	count := len(jailed)
	if count > 0 {
		log.Info("Currently jailed IPs", "count", count)
	} else {
		log.Debug("No IPs currently in jail")
	}
}

// Close closes the underlying Starskey database and stops the reporting goroutine
func (s *StarskeyRateLimiterStore) Close() error {
	log.Debug("Closing rate limiter store")
	// Signal the reporting goroutine to stop
	close(s.stopReports)
	return s.db.Close()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
