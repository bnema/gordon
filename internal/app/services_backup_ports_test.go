package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/backup"
)

func TestBackupServicePortsReturnUntypedNilWhenDisabled(t *testing.T) {
	assert.True(t, backupServicePort(nil) == nil)
	assert.True(t, volumeBackupServicePort(nil) == nil)

	assert.True(t, backupServicePort(&backup.Service{}) != nil)
	assert.True(t, volumeBackupServicePort(&backup.VolumeService{}) != nil)
}

// TestAdminBackupRoutesWhenBackupsDisabled proves the admin API answers
// 503 instead of panicking when database or volume backups are disabled.
func TestAdminBackupRoutesWhenBackupsDisabled(t *testing.T) {
	var disabled *backup.Service
	var disabledVolumes *backup.VolumeService
	handler := admin.NewHandler(admin.HandlerDeps{
		Log:             zerowrap.Default(),
		BackupSvc:       backupServicePort(disabled),
		VolumeBackupSvc: volumeBackupServicePort(disabledVolumes),
	})

	for _, path := range []string{"/admin/backups/status", "/admin/backups/volumes/status"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req = req.WithContext(context.WithValue(req.Context(), domain.ContextKeyScopes, []string{"admin:*:*"}))
			rec := httptest.NewRecorder()

			require.NotPanics(t, func() { handler.ServeHTTP(rec, req) })
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		})
	}
}
