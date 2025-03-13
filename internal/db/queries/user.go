package queries

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/google/uuid"
)

func generateUUID() string {
	return uuid.New().String()
}

// UserQueries contains all SQL queries for user operations
type UserQueries struct {
	InsertUser        string
	CheckUserExists   string
	GetUserByProvider string
	GetAccountCount   string
}

// NewUserQueries returns a new instance of UserQueries
func NewUserQueries() *UserQueries {
	return &UserQueries{
		InsertUser:        "INSERT INTO user (id, name, email) VALUES (?, ?, ?)",
		CheckUserExists:   "SELECT id FROM user",
		GetUserByProvider: "SELECT user.id, user.name, user.email FROM user INNER JOIN account ON user.id = account.user_id INNER JOIN provider ON account.id = provider.account_id WHERE provider.login = ? AND provider.email = ?",
		GetAccountCount:   "SELECT COUNT(*) FROM account",
	}
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
	err = CreateDBGitHubProvider(database, account.ID, userInfo)
	if err != nil {
		logger.Error("Failed to create GitHub provider", "error", err)
		return nil, "", nil, err
	}
	logger.Info("GitHub provider created successfully")

	logger.Debug("Creating session for account", "account_id", account.ID)
	session, err := CreateDBSession(database, browserInfo, accessToken, account.ID)
	if err != nil {
		logger.Error("Failed to create session", "error", err)
		return nil, "", nil, err
	}
	logger.Info("Session created successfully")

	return user, account.ID, session, nil
}

// CreateDBUser creates a new user in the database
func CreateDBUser(database *sql.DB, userInfo *db.GithubUserInfo) (*db.User, error) {
	user := &db.User{
		ID:    generateUUID(),
		Name:  userInfo.Login,
		Email: userInfo.Emails[0],
	}

	_, err := database.Exec(
		NewUserQueries().InsertUser,
		user.ID, user.Name, user.Email,
	)
	return user, err
}

// CheckDBUserExists checks if any user exists in the database
func CheckDBUserExists(database *sql.DB) (bool, error) {
	var userID string
	err := database.QueryRow(NewUserQueries().CheckUserExists).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	if userID != "" {
		return true, nil
	}

	return false, nil
}

// CheckDBUserIsGood checks if the user matches the provided GitHub info
func CheckDBUserIsGood(database *sql.DB, userInfo *db.GithubUserInfo) (*db.User, bool, error) {
	login := userInfo.Login
	email := userInfo.Emails[0]

	user := &db.User{}
	
	// Check if the user exists based on github login and email
	err := database.QueryRow(NewUserQueries().GetUserByProvider, login, email).Scan(
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
	user, isGood, err := CheckDBUserIsGood(database, userInfo)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error checking user: %w", err)
	}
	
	if !isGood {
		return nil, "", nil, fmt.Errorf("user not found in database")
	}
	
	// Get the account ID for this user
	var accountID string
	err = database.QueryRow("SELECT id FROM account WHERE user_id = ?", user.ID).Scan(&accountID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error getting account ID: %w", err)
	}
	
	// Check if the session exists using the access token and browser info
	existingSession, err := GetDBUserSession(database, accessToken, browserInfo)
	if err != nil && err != sql.ErrNoRows {
		return nil, "", nil, fmt.Errorf("error checking for session: %w", err)
	}

	var session *db.Sessions
	
	// If the session exists, update the session
	if existingSession != nil {
		expiresTime := time.Now().Add(time.Hour * 24).Format(time.RFC3339)
		err := UpdateDBSession(database, existingSession.ID, accessToken)
		if err != nil {
			return nil, "", nil, fmt.Errorf("error updating session: %w", err)
		}
		// Update the session struct with new values
		existingSession.AccessToken = accessToken
		existingSession.Expires = expiresTime
		session = existingSession
		logger.Debug("Session updated in database", "accountID", accountID, "sessionID", session.ID)
	} else {
		// If the session does not exist, create the session
		session, err = CreateDBSession(database, browserInfo, accessToken, accountID)
		if err != nil {
			return nil, "", nil, fmt.Errorf("could not create session: %w", err)
		}
		logger.Debug("New session created in database", "accountID", accountID, "sessionID", session.ID)
	}

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
