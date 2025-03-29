package proxy

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/db"
	"github.com/bnema/gordon/pkg/logger"
)

// dbExecWithRetry performs a database execution with retry logic for handling locked database
func (p *Proxy) dbExecWithRetry(query string, args ...interface{}) (sql.Result, error) {
	return db.ExecWithRetry(p.app.GetDB(), query, args...)
}

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

// ensureAdminDomainConfig ensures the admin domain has proper ACME configuration in the database
func (p *Proxy) ensureAdminDomainConfig() error {
	adminDomain := p.app.GetConfig().Http.FullDomain()
	adminAccountID := "" // Variable to store the admin account ID

	// Check if domain exists
	var exists bool
	err := p.app.GetDB().QueryRow(p.Queries.CheckDomainExists, adminDomain).Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check admin domain existence: %w", err)
	}

	// Get the first account ID (assumed to be the admin)
	err = p.app.GetDB().QueryRow(p.Queries.GetFirstAccount).Scan(&adminAccountID)
	if err != nil {
		if err == sql.ErrNoRows {
			// This case should ideally not happen if account setup is done first
			logger.Error("No accounts found in the database. Cannot associate admin domain.")
			return fmt.Errorf("no accounts found in database")
		}
		return fmt.Errorf("failed to get admin account ID: %w", err)
	}
	if adminAccountID == "" {
		// Sanity check
		logger.Error("Admin account ID is empty. Cannot associate admin domain.")
		return fmt.Errorf("admin account ID is empty")
	}

	if !exists {
		// Create the admin domain with proper ACME config and account ID
		now := time.Now().Format(time.RFC3339)
		_, err = p.app.GetDB().Exec(p.Queries.CreateAdminDomain,
			adminDomain,    // ID (assuming domain name is unique ID here?)
			adminDomain,    // Name
			adminAccountID, // Account ID (NEW)
			now,            // Created At
			now,            // Updated At
			true,           // ACME Enabled
			"http-01",      // ACME Challenge Type
		)
		if err != nil {
			return fmt.Errorf("failed to create admin domain: %w", err)
		}
		logger.Info("Created admin domain with ACME configuration", "domain", adminDomain, "account_id", adminAccountID)
	} else {
		// Update existing domain to ensure ACME is enabled (no need to update account_id here, assume it's set correctly)
		now := time.Now().Format(time.RFC3339)
		_, err = p.app.GetDB().Exec(p.Queries.UpdateAdminDomainAcme, now, adminDomain)
		if err != nil {
			return fmt.Errorf("failed to update admin domain ACME config: %w", err)
		}
		logger.Info("Updated admin domain ACME configuration", "domain", adminDomain)
	}

	return nil
}
