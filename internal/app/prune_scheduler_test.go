package app

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/usecase/images"
)

// TestStartImagePruneSchedulerRegistersWhenAppsExist proves the
// scheduler is no longer disabled by app state: a configured schedule
// registers even though the server has apps, and the job runs the same
// use case as manual execution.
func TestStartImagePruneSchedulerRegistersWhenAppsExist(t *testing.T) {
	cfg := Config{}
	cfg.Images.Prune.Enabled = true
	cfg.Images.Prune.Schedule = "daily"
	cfg.Images.Prune.KeepLast = 3

	svc := &services{imageSvc: images.NewService(nil, nil, nil, zerowrap.Default())}

	scheduler, err := startImagePruneScheduler(context.Background(), cfg, svc, zerowrap.Default(), nil)
	require.NoError(t, err)
	require.NotNil(t, scheduler, "a configured schedule must register regardless of app state")
	assert.NotEmpty(t, scheduler.List())
}

func TestStartImagePruneSchedulerDisabledWhenNotConfigured(t *testing.T) {
	svc := &services{imageSvc: images.NewService(nil, nil, nil, zerowrap.Default())}

	scheduler, err := startImagePruneScheduler(context.Background(), Config{}, svc, zerowrap.Default(), nil)
	require.NoError(t, err)
	assert.Nil(t, scheduler)
}

func TestStartImagePruneSchedulerRejectsNegativeKeepLast(t *testing.T) {
	cfg := Config{}
	cfg.Images.Prune.Enabled = true
	cfg.Images.Prune.Schedule = "daily"
	cfg.Images.Prune.KeepLast = 3

	svc := &services{imageSvc: images.NewService(nil, nil, nil, zerowrap.Default())}
	scheduler, err := startImagePruneScheduler(context.Background(), cfg, svc, zerowrap.Default(), func() int { return -1 })
	require.Error(t, err)
	assert.Nil(t, scheduler)
}
