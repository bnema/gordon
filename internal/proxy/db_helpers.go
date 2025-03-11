package proxy

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/charmbracelet/log"
)

// dbExecWithRetry performs a database execution with retry logic for handling locked database
func (p *Proxy) dbExecWithRetry(query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = p.app.GetDB().Exec(query, args...)
		if err == nil {
			return result, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			log.Debug("Database locked, retrying operation",
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

// dbQueryWithRetry performs a database query with retry logic for handling locked database
func (p *Proxy) dbQueryWithRetry(query string, args ...interface{}) (*sql.Rows, error) {
	var rows *sql.Rows
	var err error
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		rows, err = p.app.GetDB().Query(query, args...)
		if err == nil {
			return rows, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			log.Debug("Database locked, retrying operation",
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

// dbBeginWithRetry starts a transaction with retry logic for handling locked database
func (p *Proxy) dbBeginWithRetry() (*sql.Tx, error) {
	var tx *sql.Tx
	var err error
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		tx, err = p.app.GetDB().Begin()
		if err == nil {
			return tx, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			log.Debug("Database locked, retrying transaction start",
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

// txExecWithRetry executes a query within a transaction with retry logic
func txExecWithRetry(tx *sql.Tx, query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error
	maxRetries := 5
	retryDelay := 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		result, err = tx.Exec(query, args...)
		if err == nil {
			return result, nil
		}

		// Check if this is a database locked error
		if err.Error() == "database is locked (5) (SQLITE_BUSY)" {
			log.Debug("Database locked, retrying transaction operation",
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
