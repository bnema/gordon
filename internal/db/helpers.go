package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/pkg/logger"
)

// ExecWithRetry performs a database execution with retry logic for handling locked database
func ExecWithRetry(db *sql.DB, query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error
	maxRetries := 10
	retryDelay := 200 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = db.Exec(query, args...)
		if err == nil {
			return result, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			logger.Debug("Database locked, retrying operation",
				"attempt", attempt,
				"max_retries", maxRetries,
				"query", query)
			time.Sleep(retryDelay)
			// Increase delay for next retry (exponential backoff)
			retryDelay *= 2
			continue
		}

		// If it's not a locking error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", err)
}

// QueryWithRetry performs a database query with retry logic for handling locked database
func QueryWithRetry(db *sql.DB, query string, args ...interface{}) (*sql.Rows, error) {
	var rows *sql.Rows
	var err error
	maxRetries := 10
	retryDelay := 200 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		rows, err = db.Query(query, args...)
		if err == nil {
			return rows, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			logger.Debug("Database locked, retrying operation",
				"attempt", attempt,
				"max_retries", maxRetries,
				"query", query)
			time.Sleep(retryDelay)
			// Increase delay for next retry (exponential backoff)
			retryDelay *= 2
			continue
		}

		// If it's not a locking error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", err)
}

// BeginWithRetry starts a transaction with retry logic for handling locked database
func BeginWithRetry(db *sql.DB) (*sql.Tx, error) {
	var tx *sql.Tx
	var err error
	maxRetries := 10
	retryDelay := 200 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		tx, err = db.Begin()
		if err == nil {
			return tx, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			logger.Debug("Database locked, retrying transaction start",
				"attempt", attempt,
				"max_retries", maxRetries)
			time.Sleep(retryDelay)
			// Increase delay for next retry (exponential backoff)
			retryDelay *= 2
			continue
		}

		// If it's not a locking error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", err)
}

// TxExecWithRetry executes a query within a transaction with retry logic
func TxExecWithRetry(tx *sql.Tx, query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error
	maxRetries := 10
	retryDelay := 200 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = tx.Exec(query, args...)
		if err == nil {
			return result, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			logger.Debug("Database locked, retrying transaction operation",
				"attempt", attempt,
				"max_retries", maxRetries,
				"query", query)
			time.Sleep(retryDelay)
			// Increase delay for next retry (exponential backoff)
			retryDelay *= 2
			continue
		}

		// If it's not a locking error, return immediately
		return nil, err
	}

	return nil, fmt.Errorf("max retries exceeded: %w", err)
}
