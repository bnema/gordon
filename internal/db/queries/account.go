package queries

import (
	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/internal/server"
)

func createDBAccount(a *server.App) (*db.Account, error) {
	account := &db.Account{
		ID: generateUUID(),
	}

	_, err := a.DB.Exec(
		"INSERT INTO account (id, user_id) VALUES (?, ?)",
		account.ID, a.DBTables.User.ID,
	)
	return account, err
}

func CheckDBAccountExists(a *server.App, accountID string) (bool, error) {
	var exists bool
	err := a.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM account WHERE id = ?)", accountID).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func PopulateAccountFromDB(a *server.App) error {
	rows, err := a.DB.Query("SELECT id, user_id FROM account")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		account := db.Account{}
		err := rows.Scan(&account.ID, &account.UserID)
		if err != nil {
			return err
		}

		a.DBTables.Account = account
	}

	return rows.Err()
}
