package queries

import (
	"database/sql"
	"fmt"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/google/uuid"
)

// UserQueries contains all SQL queries for user operations
type UserQueries struct {
	InsertUser                  string
	CheckUserExists             string
	GetUserByProvider           string
	GetAccountCount             string
	GetFirstUserAndAccountQuery string
	UpdateUserDetailsQuery      string
}

// NewUserQueries returns a new instance of UserQueries
func NewUserQueries() *UserQueries {
	return &UserQueries{
		InsertUser:        "INSERT INTO user (id, name, email) VALUES (?, ?, ?)",
		CheckUserExists:   "SELECT EXISTS(SELECT 1 FROM user LIMIT 1)",
		GetUserByProvider: "SELECT user.id, user.name, user.email FROM user INNER JOIN account ON user.id = account.user_id INNER JOIN provider ON account.id = provider.account_id WHERE provider.login = ? AND provider.email = ?",
		GetAccountCount:   "SELECT COUNT(*) FROM account",
		GetFirstUserAndAccountQuery: `
			SELECT u.id, u.name, u.email, a.id
			FROM user u
			JOIN account a ON u.id = a.user_id
			ORDER BY u.rowid ASC -- Rely on implicit rowid for 'first'
			LIMIT 1`,
		UpdateUserDetailsQuery: "UPDATE user SET name = ?, email = ? WHERE id = ?",
	}
}

// GetFirstUserAndAccount retrieves the first user and their associated account ID. Assumes this is the seeded admin.
func GetFirstUserAndAccount(database *sql.DB) (*db.User, string, error) {
	user := &db.User{}
	var accountID string
	err := database.QueryRow(NewUserQueries().GetFirstUserAndAccountQuery).Scan(
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

// UpdateUserDetails updates the name and email for a specific user ID.
func UpdateUserDetails(database *sql.DB, userID string, newName string, newEmail string) error {
	_, err := database.Exec(NewUserQueries().UpdateUserDetailsQuery, newName, newEmail, userID)
	return err
}

// CreateUser creates a new user along with the associated account, provider, and session.
func CreateUser(database *sql.DB, accessToken string, browserInfo string, userInfo *db.GithubUserInfo) (*db.User, string, *db.Sessions, error) {
	// Check if a user already exists. If so, return an error.
	logger.Debug("Starting CreateUser database function")
	if exists, err := CheckDBUserExists(database); err != nil || exists {
		logger.Warn("User already exists or error checking", "error", err, "exists", exists)
		return nil, "", nil, fmt.Errorf("error checking user or user already exists: %v", err)
	}

	logger.Debug("Creating database user", "login", userInfo.Login, "email", userInfo.Emails[0])
	user, err := CreateDBUser(database, userInfo)
	if err != nil {
		logger.Error("Failed to create database user", "error", err)
		return nil, "", nil, err
	}
	logger.Info("Database user created successfully", "user_id", user.ID)

	logger.Debug("Creating database account for user", "user_id", user.ID)
	account, err := CreateDBAccount(database, user.ID)
	if err != nil {
		logger.Error("Failed to create database account", "error", err)
		return nil, "", nil, err
	}
	logger.Info("Database account created successfully", "account_id", account.ID)

	logger.Debug("Creating GitHub provider for account", "account_id", account.ID)
	err = InsertProviderForAccount(database, account.ID, "github", userInfo)
	if err != nil {
		logger.Error("Failed to create GitHub provider", "error", err)
		return nil, "", nil, err
	}
	logger.Info("GitHub provider created successfully")

	logger.Debug("Creating session for account", "account_id", account.ID)
	session, err := CreateOrUpdateSession(database, account.ID, accessToken, browserInfo)
	if err != nil {
		logger.Error("Failed to create session", "error", err)
		return nil, "", nil, err
	}
	logger.Info("Session created successfully")

	return user, account.ID, session, nil
}

// CreateDBUser creates a new user in the database
func CreateDBUser(database *sql.DB, userInfo *db.GithubUserInfo) (*db.User, error) {
	primaryEmail := ""
	if len(userInfo.Emails) > 0 {
		primaryEmail = userInfo.Emails[0]
	}

	user := &db.User{
		ID:    uuid.NewString(),
		Name:  userInfo.Login,
		Email: primaryEmail,
	}

	_, err := database.Exec(
		NewUserQueries().InsertUser,
		user.ID, user.Name, user.Email,
	)
	return user, err
}

// CheckDBUserExists checks if any user exists in the database
func CheckDBUserExists(database *sql.DB) (bool, error) {
	var exists bool
	err := database.QueryRow(NewUserQueries().CheckUserExists).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// CheckDBUserIsGood checks if the user matches the provided GitHub info
func CheckDBUserIsGood(database *sql.DB, userInfo *db.GithubUserInfo) (*db.User, bool, error) {
	login := userInfo.Login
	primaryEmail := ""
	if len(userInfo.Emails) > 0 {
		primaryEmail = userInfo.Emails[0]
	}

	user := &db.User{}

	// Check if the user exists based on github login and email
	err := database.QueryRow(NewUserQueries().GetUserByProvider, login, primaryEmail).Scan(
		&user.ID, &user.Name, &user.Email,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}

	if user.ID != "" {
		return user, true, nil
	}

	return nil, false, nil
}

// UpdateUser updates the user session with new access token
func UpdateUser(database *sql.DB, accessToken string, browserInfo string, userInfo *db.GithubUserInfo) (*db.User, string, *db.Sessions, error) {
	// First, get the user info to get their account ID
	user, accountID, err := GetUserByProviderLogin(database, "github", userInfo.Login)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", nil, fmt.Errorf("user not found in database for provider login '%s'", userInfo.Login)
		}
		return nil, "", nil, fmt.Errorf("error checking user by provider login: %w", err)
	}

	// If user found, update/create their session
	session, err := CreateOrUpdateSession(database, accountID, accessToken, browserInfo)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error creating/updating session: %w", err)
	}

	logger.Debug("Session updated/created in database", "accountID", accountID, "sessionID", session.ID)

	return user, accountID, session, nil
}

// GetAccountCount returns the number of accounts in the database
func GetAccountCount(database *sql.DB) (int, error) {
	var count int
	err := database.QueryRow(NewUserQueries().GetAccountCount).Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}
