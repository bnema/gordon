package queries

import (
	"database/sql"

	"github.com/bnema/gordon/internal/app"
)

// Pseudo-code for all queries that will be needed on the user database
// User table
// User has many accounts (one to many) because a user can auth in many ways also have many sessions

// 1. Create a user (one only, if id=1 then it's the admin)
// This happens when the user logs in for the first time
// TODO: prevent anyone from creating another user
func CreateUser(a *app.App, db *sql.DB) error {
	// To create a user i need to :
	// 1. check if gordontoken == gordontoken inside the yaml config file
	// 2. check if user already exists
	// 3. get the informations from the oauth provider (only github for now)
	// 4. create the user in the database
	// 5. create the account in the database
	// 6. create the session in the database
	// 7. return the user informations (id, name, email, image, is_admin)
	return nil
}
