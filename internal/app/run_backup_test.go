package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateBackupRetentionRejectsNegativeValues(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		err  string
	}{
		{
			name: "hourly",
			set: func(cfg *Config) {
				cfg.Backups.Retention.Hourly = -1
			},
			err: "backups.retention.hourly",
		},
		{
			name: "daily",
			set: func(cfg *Config) {
				cfg.Backups.Retention.Daily = -1
			},
			err: "backups.retention.daily",
		},
		{
			name: "weekly",
			set: func(cfg *Config) {
				cfg.Backups.Retention.Weekly = -1
			},
			err: "backups.retention.weekly",
		},
		{
			name: "monthly",
			set: func(cfg *Config) {
				cfg.Backups.Retention.Monthly = -1
			},
			err: "backups.retention.monthly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			tt.set(&cfg)

			_, err := validateBackupRetention(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func TestValidateBackupRetentionAcceptsZeroAndPositiveValues(t *testing.T) {
	t.Run("positive values", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Retention.Hourly = 1
		cfg.Backups.Retention.Daily = 7
		cfg.Backups.Retention.Weekly = 4
		cfg.Backups.Retention.Monthly = 12

		retention, err := validateBackupRetention(cfg)
		require.NoError(t, err)
		assert.Equal(t, 1, retention.Hourly)
		assert.Equal(t, 7, retention.Daily)
		assert.Equal(t, 4, retention.Weekly)
		assert.Equal(t, 12, retention.Monthly)
	})

	t.Run("zero values", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Retention.Hourly = 0
		cfg.Backups.Retention.Daily = 0
		cfg.Backups.Retention.Weekly = 0
		cfg.Backups.Retention.Monthly = 0

		retention, err := validateBackupRetention(cfg)
		require.NoError(t, err)
		assert.Equal(t, 0, retention.Hourly)
		assert.Equal(t, 0, retention.Daily)
		assert.Equal(t, 0, retention.Weekly)
		assert.Equal(t, 0, retention.Monthly)
	})
}
