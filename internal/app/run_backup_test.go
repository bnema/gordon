package app

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
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
				cfg.Backups.Databases.Retention.Hourly = -1
			},
			err: "backups.databases.retention.hourly",
		},
		{
			name: "daily",
			set: func(cfg *Config) {
				cfg.Backups.Databases.Retention.Daily = -1
			},
			err: "backups.databases.retention.daily",
		},
		{
			name: "weekly",
			set: func(cfg *Config) {
				cfg.Backups.Databases.Retention.Weekly = -1
			},
			err: "backups.databases.retention.weekly",
		},
		{
			name: "monthly",
			set: func(cfg *Config) {
				cfg.Backups.Databases.Retention.Monthly = -1
			},
			err: "backups.databases.retention.monthly",
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
		cfg.Backups.Databases.Retention.Hourly = 1
		cfg.Backups.Databases.Retention.Daily = 7
		cfg.Backups.Databases.Retention.Weekly = 4
		cfg.Backups.Databases.Retention.Monthly = 12

		retention, err := validateBackupRetention(cfg)
		require.NoError(t, err)
		assert.Equal(t, 1, retention.Hourly)
		assert.Equal(t, 7, retention.Daily)
		assert.Equal(t, 4, retention.Weekly)
		assert.Equal(t, 12, retention.Monthly)
	})

	t.Run("zero values", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Databases.Retention.Hourly = 0
		cfg.Backups.Databases.Retention.Daily = 0
		cfg.Backups.Databases.Retention.Weekly = 0
		cfg.Backups.Databases.Retention.Monthly = 0

		retention, err := validateBackupRetention(cfg)
		require.NoError(t, err)
		assert.Equal(t, 0, retention.Hourly)
		assert.Equal(t, 0, retention.Daily)
		assert.Equal(t, 0, retention.Weekly)
		assert.Equal(t, 0, retention.Monthly)
	})
}

func TestValidateBackupRetentionAcceptsLegacyBackupsConfig(t *testing.T) {
	var cfg Config
	cfg.Backups.Retention.Daily = 3

	retention, err := validateBackupRetention(cfg)

	require.NoError(t, err)
	assert.Equal(t, 3, retention.Daily)
}

func TestDatabaseBackupSettingsPreferEnabledLegacySchedule(t *testing.T) {
	var cfg Config
	cfg.Backups.Enabled = true
	cfg.Backups.Schedule = string(domain.ScheduleHourly)
	cfg.Backups.Databases.Schedule = string(domain.ScheduleDaily)

	settings := databaseBackupSettings(cfg)

	assert.True(t, settings.Enabled)
	assert.Equal(t, string(domain.ScheduleHourly), settings.Schedule)
}

func TestValidateVolumeBackupConfig(t *testing.T) {
	t.Run("disabled accepts defaults", func(t *testing.T) {
		var cfg Config

		volumeCfg, err := validateVolumeBackupConfig(cfg)
		require.NoError(t, err)
		assert.False(t, volumeCfg.Enabled)
		assert.Equal(t, domain.VolumeBackupCompressionGzip, volumeCfg.Compression)
		assert.Equal(t, 0, volumeCfg.Retention.Keep)
		assert.Equal(t, 2, volumeCfg.MaxConcurrency)
		assert.Equal(t, "alpine:3.20", volumeCfg.HelperImage)
	})

	t.Run("enabled requires bucket and region", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Volumes.Enabled = true
		cfg.Backups.Volumes.Retention.Keep = 14

		_, err := validateVolumeBackupConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backups.volumes.s3.bucket")

		cfg.Backups.Volumes.S3.Bucket = "gordon-backups"
		_, err = validateVolumeBackupConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backups.volumes.s3.region")
	})

	t.Run("enabled accepts s3 config", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Volumes.Enabled = true
		cfg.Backups.Volumes.Interval = "6h"
		cfg.Backups.Volumes.Timeout = "30m"
		cfg.Backups.Volumes.Compression = "gzip"
		cfg.Backups.Volumes.MaxConcurrency = 3
		cfg.Backups.Volumes.HelperImage = "example/helper:1"
		cfg.Backups.Volumes.S3.Bucket = "gordon-backups"
		cfg.Backups.Volumes.S3.Region = "eu-west-3"
		cfg.Backups.Volumes.S3.Prefix = "prod/gordon"
		cfg.Backups.Volumes.Retention.Keep = 7

		volumeCfg, err := validateVolumeBackupConfig(cfg)
		require.NoError(t, err)
		assert.True(t, volumeCfg.Enabled)
		assert.Equal(t, domain.VolumeBackupCompressionGzip, volumeCfg.Compression)
		assert.Equal(t, 7, volumeCfg.Retention.Keep)
		assert.Equal(t, 3, volumeCfg.MaxConcurrency)
		assert.Equal(t, "prod/gordon", volumeCfg.S3Prefix)
	})

	t.Run("zstd requires helper image with zstd", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Volumes.Compression = "zstd"

		_, err := validateVolumeBackupConfig(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "helper_image")
	})

	t.Run("enabled rejects zero retention", func(t *testing.T) {
		var cfg Config
		cfg.Backups.Volumes.Enabled = true
		cfg.Backups.Volumes.S3.Bucket = "gordon-backups"
		cfg.Backups.Volumes.S3.Region = "eu-west-3"
		cfg.Backups.Volumes.Retention.Keep = 0

		_, err := validateVolumeBackupConfig(cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backups.volumes.retention.keep")
	})
}

func TestLoadConfigSetsBackupDefaults(t *testing.T) {
	v := viper.New()

	require.NoError(t, loadConfig(v, ""))

	assert.Equal(t, string(domain.ScheduleDaily), v.GetString("backups.databases.schedule"))
	assert.Equal(t, "24h", v.GetString("backups.volumes.interval"))
	assert.Equal(t, string(domain.VolumeBackupCompressionGzip), v.GetString("backups.volumes.compression"))
	assert.Equal(t, 14, v.GetInt("backups.volumes.retention.keep"))
}

func TestResolveBackupSchedule(t *testing.T) {
	t.Run("valid values", func(t *testing.T) {
		tests := []struct {
			input string
			want  domain.BackupSchedule
		}{
			{input: "hourly", want: domain.ScheduleHourly},
			{input: "daily", want: domain.ScheduleDaily},
			{input: "weekly", want: domain.ScheduleWeekly},
			{input: "monthly", want: domain.ScheduleMonthly},
			{input: " DAILY ", want: domain.ScheduleDaily},
		}

		for _, tt := range tests {
			t.Run(tt.input, func(t *testing.T) {
				got, err := resolveBackupSchedule(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("invalid value", func(t *testing.T) {
		_, err := resolveBackupSchedule("every-minute")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "backups.databases.schedule")
	})
}
