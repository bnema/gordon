package app

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestLoadConfigSetsImagePruneDefaults(t *testing.T) {
	v := viper.New()

	require.NoError(t, loadConfig(v, ""))

	assert.False(t, v.GetBool("images.prune.enabled"))
	assert.Equal(t, string(domain.ScheduleDaily), v.GetString("images.prune.schedule"))
	assert.Equal(t, domain.DefaultImagePruneKeepLast, v.GetInt("images.prune.keep_last"))
}

func TestResolveImagePruneSchedule(t *testing.T) {
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
				got, err := resolveImagePruneSchedule(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("invalid value", func(t *testing.T) {
		_, err := resolveImagePruneSchedule("every-minute")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "images.prune.schedule")
	})
}
