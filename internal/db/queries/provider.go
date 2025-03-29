package queries

import (
	"database/sql"

	"github.com/bnema/gordon/internal/db"
	"github.com/google/uuid"
)

// ProviderQueries contains all SQL queries for provider operations
type ProviderQueries struct {
	InsertProvider                string
	PopulateProviderFromDB        string
	CheckProviderExists           string
	GetProviderByAccountLogin     string
	CountProvidersQuery           string
	InsertProviderForAccountQuery string
	GetUserByProviderLoginQuery   string
}

// NewProviderQueries returns a new instance of ProviderQueries
func NewProviderQueries() *ProviderQueries {
	return &ProviderQueries{
		InsertProvider:            "INSERT INTO provider (id, account_id, name, login, avatar_url, profile_url, email) VALUES (?, ?, ?, ?, ?, ?, ?)",
		PopulateProviderFromDB:    "SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider WHERE account_id = ?",
		CheckProviderExists:       "SELECT EXISTS(SELECT 1 FROM provider WHERE login = ? AND email = ?)",
		GetProviderByAccountLogin: "SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider WHERE login = ? AND email = ?",
		CountProvidersQuery:       "SELECT COUNT(*) FROM provider",
		InsertProviderForAccountQuery: `
			INSERT INTO provider (id, account_id, name, login, avatar_url, profile_url, email)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
		GetUserByProviderLoginQuery: `
			SELECT u.id, u.name, u.email, p.account_id
			FROM provider p
			JOIN account a ON p.account_id = a.id
			JOIN user u ON a.user_id = u.id
			WHERE p.name = ? AND p.login = ?`,
	}
}

// CountProviders returns the total number of provider records in the database.
func CountProviders(database *sql.DB) (int, error) {
	var count int
	err := database.QueryRow(NewProviderQueries().CountProvidersQuery).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Inserts a new provider record linked to an existing account.
func InsertProviderForAccount(database *sql.DB, accountID string, providerName string, userInfo *db.GithubUserInfo) error {
	providerID := uuid.NewString()
	primaryEmail := ""
	if len(userInfo.Emails) > 0 {
		primaryEmail = userInfo.Emails[0]
	}

	_, err := database.Exec(
		NewProviderQueries().InsertProviderForAccountQuery,
		providerID,
		accountID,
		providerName,
		userInfo.Login,
		userInfo.AvatarURL,
		userInfo.ProfileURL,
		primaryEmail,
	)
	return err
}

// Retrieves the User and Account ID associated with a specific provider login.
func GetUserByProviderLogin(database *sql.DB, providerName string, providerLogin string) (*db.User, string, error) {
	user := &db.User{}
	var accountID string

	err := database.QueryRow(
		NewProviderQueries().GetUserByProviderLoginQuery,
		providerName,
		providerLogin,
	).Scan(
		&user.ID,
		&user.Name,
		&user.Email,
		&accountID,
	)

	if err != nil {
		return nil, "", err
	}

	return user, accountID, nil
}

// CreateDBGitHubProvider creates a new GitHub provider in the database.
func CreateDBGitHubProvider(database *sql.DB, accountID string, userInfo *db.GithubUserInfo) error {
	providerID := uuid.NewString()

	primaryEmail := ""
	if len(userInfo.Emails) > 0 {
		primaryEmail = userInfo.Emails[0]
	}

	_, err := database.Exec(
		NewProviderQueries().InsertProvider,
		providerID,
		accountID,
		"github",
		userInfo.Login,
		userInfo.AvatarURL,
		userInfo.ProfileURL,
		primaryEmail,
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
		if err == sql.ErrNoRows {
			return false, nil
		}
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
