package queries

import (
	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/server"
)

func createDBGitHubProvider(a *server.App, userInfo *GithubUserInfo) error {
	provider := &db.Provider{
		ID:         generateUUID(),
		Email:      userInfo.Emails[0],
		Login:      userInfo.Login,
		AvatarURL:  userInfo.AvatarURL,
		ProfileURL: userInfo.ProfileURL,
		Name:       "GitHub",
	}

	query := "INSERT INTO provider (id, account_id, name, login, avatar_url, profile_url, email) VALUES (?, ?, ?, ?, ?, ?, ?)"
	_, err := a.DB.Exec(query, provider.ID, a.DBTables.Account.ID, provider.Name, provider.Login, provider.AvatarURL, provider.ProfileURL, provider.Email)
	return err
}

func PopulateProviderFromDB(a *server.App) error {
	rows, err := a.DB.Query("SELECT id, account_id, name, login, avatar_url, profile_url, email FROM provider")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		provider := db.Provider{}
		err := rows.Scan(&provider.ID, &provider.AccountID, &provider.Name, &provider.Login, &provider.AvatarURL, &provider.ProfileURL, &provider.Email)
		if err != nil {
			return err
		}

		a.DBTables.Provider = provider
	}

	return rows.Err()
}
