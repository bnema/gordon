package cli

import (
	"bytes"
	"context"
	"testing"

	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestResolveAttachmentImage_OneTarget(t *testing.T) {
	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().FindAttachmentTargetsByImage(context.Background(), "postgres").Return([]string{"app.example.com"}, nil).Once()
	cpMock.EXPECT().GetStatus(context.Background()).Return(&remote.Status{RegistryDomain: "registry.example.com"}, nil).Once()

	registry, imageName, targets, err := resolveAttachmentImage(context.Background(), cpMock, "postgres")

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "postgres", imageName)
	assert.Equal(t, []string{"app.example.com"}, targets)
}

func TestResolveAttachmentImage_MultipleTargets(t *testing.T) {
	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().FindAttachmentTargetsByImage(context.Background(), "redis").Return([]string{"app.example.com", "backend"}, nil).Once()
	cpMock.EXPECT().GetStatus(context.Background()).Return(&remote.Status{RegistryDomain: "registry.example.com"}, nil).Once()

	registry, imageName, targets, err := resolveAttachmentImage(context.Background(), cpMock, "redis")

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com", registry)
	assert.Equal(t, "redis", imageName)
	assert.Equal(t, []string{"app.example.com", "backend"}, targets)
}

func TestResolveAttachmentImage_NotConfigured(t *testing.T) {
	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().FindAttachmentTargetsByImage(context.Background(), "postgres").Return(nil, nil).Once()

	_, _, _, err := resolveAttachmentImage(context.Background(), cpMock, "postgres")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured as an attachment")
}

func TestAttachmentPushCmd_NoDeploy(t *testing.T) {
	origResolveControlPlane := resolveControlPlaneFn
	origDetermineVersion := determineVersionFn
	origNewImageOps := newImageOpsFn
	t.Cleanup(func() {
		resolveControlPlaneFn = origResolveControlPlane
		determineVersionFn = origDetermineVersion
		newImageOpsFn = origNewImageOps
	})

	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().FindAttachmentTargetsByImage(context.Background(), "postgres").Return([]string{"app.example.com"}, nil).Once()
	cpMock.EXPECT().GetStatus(context.Background()).Return(&remote.Status{RegistryDomain: "registry.example.com"}, nil).Once()

	resolveControlPlaneFn = func(string) (*controlPlaneHandle, error) {
		return &controlPlaneHandle{plane: cpMock}, nil
	}
	determineVersionFn = func(context.Context, string) string { return "v1.2.3" }
	pushCalled := false
	newImageOpsFn = func() (pushImageOps, error) {
		m := climocks.NewMockpushImageOps(t)
		m.On("Tag", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		m.On("Exists", mock.Anything, mock.Anything).Return(true, nil)
		m.On("Push", mock.Anything, mock.Anything).Run(func(mock.Arguments) {
			pushCalled = true
		}).Return(nil)
		return m, nil
	}

	cmd := newAttachmentsPushCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs([]string{"postgres"})

	err := cmd.Execute()

	require.NoError(t, err)
	assert.True(t, pushCalled)
	assert.Contains(t, out.String(), "Push complete")
}

func TestAttachmentPushCmd_TaggedImageInputBuildsValidRefs(t *testing.T) {
	origResolveControlPlane := resolveControlPlaneFn
	origDetermineVersion := determineVersionFn
	origNewImageOps := newImageOpsFn
	t.Cleanup(func() {
		resolveControlPlaneFn = origResolveControlPlane
		determineVersionFn = origDetermineVersion
		newImageOpsFn = origNewImageOps
	})

	cpMock := climocks.NewMockControlPlane(t)
	cpMock.EXPECT().FindAttachmentTargetsByImage(context.Background(), "postgres:18").Return([]string{"app.example.com"}, nil).Once()
	cpMock.EXPECT().GetStatus(context.Background()).Return(&remote.Status{RegistryDomain: "registry.example.com"}, nil).Once()

	resolveControlPlaneFn = func(string) (*controlPlaneHandle, error) {
		return &controlPlaneHandle{plane: cpMock}, nil
	}
	determineVersionFn = func(context.Context, string) string { return "v1.2.3" }

	var gotVersionRef string
	var gotLatestRef string
	newImageOpsFn = func() (pushImageOps, error) {
		m := climocks.NewMockpushImageOps(t)
		m.On("Tag", mock.Anything, mock.Anything, mock.Anything).Return(nil)
		m.On("Exists", mock.Anything, mock.Anything).Return(true, nil)
		m.On("Push", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			ref := args.Get(1).(string)
			if gotVersionRef == "" {
				gotVersionRef = ref
			} else if gotLatestRef == "" {
				gotLatestRef = ref
			}
		}).Return(nil)
		return m, nil
	}

	cmd := newAttachmentsPushCmd()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{"postgres:18"})

	err := cmd.Execute()

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/postgres:v1.2.3", gotVersionRef)
	assert.Equal(t, "registry.example.com/postgres:latest", gotLatestRef)
}

func TestNormalizeAttachmentImageName_PreservesNamespaceWithoutRegistryHost(t *testing.T) {
	got := normalizeAttachmentImageName("myorg/postgres:18")

	assert.Equal(t, "myorg/postgres", got)
}
