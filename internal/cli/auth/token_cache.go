package auth

import (
	"sync"
	"time"

	"github.com/bnema/gordon/internal/db/queries"
	"github.com/charmbracelet/log"
)

type TokenCache struct {
	cache sync.Map
}

type cacheEntry struct {
	token      string
	expiration time.Time
	githubUser *queries.GithubUserInfo
}

var (
	defaultCache *TokenCache
	once         sync.Once
)

// GetTokenCache returns a singleton instance of TokenCache
func GetTokenCache() *TokenCache {
	once.Do(func() {
		defaultCache = &TokenCache{}
		log.Debug("initialized token cache singleton")
	})
	return defaultCache
}

// GetWithUser retrieves a token and GitHub user info from the cache
func (tc *TokenCache) GetWithUser(key string) (string, *queries.GithubUserInfo, bool) {
	if value, ok := tc.cache.Load(key); ok {
		entry := value.(cacheEntry)
		if time.Now().Before(entry.expiration) {
			log.Debug("cache hit", "key", key, "expires_in", time.Until(entry.expiration))
			return entry.token, entry.githubUser, true
		}
		log.Debug("cache entry expired", "key", key)
		tc.cache.Delete(key)
	}
	log.Debug("cache miss", "key", key)
	return "", nil, false
}

// Get retrieves just the token validation status
func (tc *TokenCache) Get(key string) (bool, bool) {
	if value, ok := tc.cache.Load(key); ok {
		entry := value.(cacheEntry)
		if time.Now().Before(entry.expiration) {
			log.Debug("cache hit (validation status)", "key", key, "expires_in", time.Until(entry.expiration))
			return true, true
		}
		log.Debug("cache entry expired (validation status)", "key", key)
		tc.cache.Delete(key)
	}
	log.Debug("cache miss (validation status)", "key", key)
	return false, false
}

// SetWithUser stores a token and GitHub user info in the cache
func (tc *TokenCache) SetWithUser(key string, token string, user *queries.GithubUserInfo, duration time.Duration) {
	tc.cache.Store(key, cacheEntry{
		token:      token,
		githubUser: user,
		expiration: time.Now().Add(duration),
	})
	log.Debug("stored token with user info in cache",
		"key", key,
		"duration", duration,
		"github_user", user.Login)
}

// Set stores just the token validation status
func (tc *TokenCache) Set(key string, duration time.Duration) {
	tc.cache.Store(key, cacheEntry{
		token:      key,
		expiration: time.Now().Add(duration),
	})
	log.Debug("stored token validation status in cache",
		"key", key,
		"duration", duration)
}
