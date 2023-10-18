package queries

import (
	"database/sql"
	"fmt"

	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/db"
	"github.com/google/uuid"
)

func generateUUID() string {
	return uuid.New().String()
}

// UserInfo holds the essential information for a Github user.
type GithubUserInfo struct {
	Login      string `json:"login"`
	AvatarURL  string `json:"avatar_url"`
	ProfileURL string `json:"html_url"`
	Emails     []string
}

// CreateUser creates a new user along with the associated account, provider, and session.
func CreateUser(a *app.App, accessToken string, browserInfo string, userInfo *GithubUserInfo) error {
	// Check if a user already exists. If so, return an error.
	if exists, err := CheckDBUserExists(a); err != nil || exists {
		return fmt.Errorf("error checking user or user already exists: %v", err)
	}

	user, err := createDBUser(a, userInfo)
	if err != nil {
		return err
	}

	fmt.Println("user", user)

	// update the global state
	a.DBTables.User.ID = user.ID
	a.DBTables.User.Name = user.Name
	a.DBTables.User.Email = user.Email

	account, err := createDBAccount(a)
	if err != nil {
		return err
	}

	// update the global state
	a.DBTables.Account.ID = account.ID
	a.DBTables.Account.UserID = account.UserID

	err = createDBGitHubProvider(a, userInfo)
	if err != nil {
		return err
	}

	return createDBSession(a, browserInfo, accessToken, account.ID)
}

func createDBUser(a *app.App, userInfo *GithubUserInfo) (*db.User, error) {
	user := &db.User{
		ID:    generateUUID(),
		Name:  userInfo.Login,
		Email: userInfo.Emails[0],
	}

	_, err := a.DB.Exec(
		"INSERT INTO user (id, name, email) VALUES (?, ?, ?)",
		user.ID, user.Name, user.Email,
	)
	return user, err
}

func CheckDBUserExists(a *app.App) (bool, error) {
	var userID string
	err := a.DB.QueryRow("SELECT id FROM user").Scan(&userID)
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

// If there is already a user, we compare the login and email to see if it is the same user
func CheckDBUserIsGood(a *app.App, userInfo *GithubUserInfo) (bool, error) {
	login := userInfo.Login
	email := userInfo.Emails[0]

	// Check if the user exists based on github login and email
	query := "SELECT user.id, user.name, user.email FROM user INNER JOIN account ON user.id = account.user_id INNER JOIN provider ON account.id = provider.account_id WHERE provider.login = ? AND provider.email = ?"
	err := a.DB.QueryRow(query, login, email).Scan(&a.DBTables.User.ID, &a.DBTables.User.Name, &a.DBTables.User.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	if a.DBTables.User.ID != "" {
		return true, nil
	}

	return false, nil
}

func UpdateUser(a *app.App, accessToken string, browserInfo string, userInfo *GithubUserInfo) (*db.User, error) {

	err := CreateOrUpdateDBSession(a, accessToken, browserInfo)
	if err != nil {
		return nil, fmt.Errorf("could not create or update session: %w", err)
	}

	user := a.DBTables.User

	query := "SELECT user.id, user.name, user.email FROM user INNER JOIN account ON user.id = account.user_id INNER JOIN provider ON account.id = provider.account_id WHERE provider.login = ? AND provider.email = ?"
	err = a.DB.QueryRow(query, userInfo.Login, userInfo.Emails[0]).Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		return nil, err
	}

	return &user, nil

}

func GetAccountCount(a *app.App) (int, error) {
	var count int
	err := a.DB.QueryRow("SELECT COUNT(*) FROM account").Scan(&count)
	if err != nil {
		return 0, err
	}

	return count, nil
}
