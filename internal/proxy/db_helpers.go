package proxy

import (
	"database/sql"

	"github.com/bnema/gordon/internal/db"
)

// dbQueryWithRetry performs a database query with retry logic for handling locked database
func (p *Proxy) dbQueryWithRetry(query string, args ...interface{}) (*sql.Rows, error) {
	return db.QueryWithRetry(p.app.GetDB(), query, args...)
}

// dbBeginWithRetry starts a transaction with retry logic for handling locked database
func (p *Proxy) dbBeginWithRetry() (*sql.Tx, error) {
	return db.BeginWithRetry(p.app.GetDB())
}

// txExecWithRetry executes a query within a transaction with retry logic
func txExecWithRetry(tx *sql.Tx, query string, args ...interface{}) (sql.Result, error) {
	return db.TxExecWithRetry(tx, query, args...)
}
