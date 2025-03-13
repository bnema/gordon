package queries

import (
	"database/sql"
	"github.com/bnema/gordon/internal/db"
)

// ProviderQueries contains all SQL queries for provider operations
type ProviderQueries struct {
	InsertProvider            string
	PopulateProviderFromDB    string
	CheckProviderExists       string
	GetProviderByAccountLogin string
}

// NewProviderQueries returns a new instance of ProviderQueries
func NewProviderQueries() *ProviderQueries {
	return &ProviderQueries{
		InsertProvider:            "INSERT INTO provider (id, account_id, name, login, avatar_url, profile_url, email) VALUES (?, ?, ?, ?, ?, ?, ?)",
		PopulateProviderFromDB:    "SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider WHERE account_id = ?",
		CheckProviderExists:       "SELECT EXISTS(SELECT 1 FROM provider WHERE login = ? AND email = ?)",
		GetProviderByAccountLogin: "SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider WHERE login = ? AND email = ?",
	}
}

// CreateDBGitHubProvider creates a new GitHub provider in the database.
func CreateDBGitHubProvider(database *sql.DB, accountID string, userInfo *db.GithubUserInfo) error {
	providerID := generateUUID()
	
	_, err := database.Exec(
		NewProviderQueries().InsertProvider,
		providerID,
		accountID,
		"github",
		userInfo.Login,
		userInfo.AvatarURL,
		userInfo.ProfileURL,
		userInfo.Emails[0],
	)
	
	return err
}

// PopulateDBProviderFromDB populates provider information from the database.
func PopulateDBProviderFromDB(database *sql.DB, accountID string) (*db.Provider, error) {
	provider := &db.Provider{}
	
	err := database.QueryRow(
		NewProviderQueries().PopulateProviderFromDB,
		accountID,
	).Scan(
		&provider.ID,
		&provider.AccountID,
		&provider.Name,
		&provider.Login,
		&provider.AvatarURL,
		&provider.ProfileURL,
		&provider.Email,
	)
	
	if err != nil {
		return nil, err
	}
	
	return provider, nil
}

// CheckDBProviderExists checks if a provider exists in the database.
func CheckDBProviderExists(database *sql.DB, login string, email string) (bool, error) {
	var exists bool
	
	err := database.QueryRow(
		NewProviderQueries().CheckProviderExists,
		login,
		email,
	).Scan(&exists)
	
	if err != nil {
		return false, err
	}
	
	return exists, nil
}

// GetProviderByLoginAndEmail returns a provider from the database by login and email.
func GetProviderByLoginAndEmail(database *sql.DB, login string, email string) (*db.Provider, error) {
	provider := &db.Provider{}
	
	err := database.QueryRow(
		NewProviderQueries().GetProviderByAccountLogin,
		login,
		email,
	).Scan(
		&provider.ID,
		&provider.AccountID,
		&provider.Name,
		&provider.Login,
		&provider.AvatarURL,
		&provider.ProfileURL,
		&provider.Email,
	)
	
	if err != nil {
		return nil, err
	}
	
	return provider, nil
}
