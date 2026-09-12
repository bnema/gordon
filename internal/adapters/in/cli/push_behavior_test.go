package cli

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingPushOps struct {
	existsRef string
	tags      [][2]string
	pushes    []string
	exists    bool
}

func (o *recordingPushOps) Exists(_ context.Context, ref string) (bool, error) {
	o.existsRef = ref
	return o.exists, nil
}

func (o *recordingPushOps) Tag(_ context.Context, source, target string) error {
	o.tags = append(o.tags, [2]string{source, target})
	return nil
}

func (o *recordingPushOps) Push(_ context.Context, ref string) error {
	o.pushes = append(o.pushes, ref)
	return nil
}

func (*recordingPushOps) Build(context.Context, []string) error { return nil }

func TestTagAndPushPreservesExactSourceReference(t *testing.T) {
	ops := &recordingPushOps{exists: true}
	img := imagePush{
		SourceRef: "registry.example.com/team/app:v3",
		Version:   "v4", VersionRef: "registry.example.com/team/app:v4",
		LatestRef: "registry.example.com/team/app:latest",
	}

	require.NoError(t, tagAndPush(context.Background(), io.Discard, ops, img))
	require.Equal(t, img.SourceRef, ops.existsRef)
	require.Equal(t, [][2]string{
		{img.SourceRef, img.VersionRef},
		{img.SourceRef, img.LatestRef},
	}, ops.tags)
	require.Equal(t, []string{img.VersionRef, img.LatestRef}, ops.pushes)
}

func TestTagAndPushRejectsMissingSourceReference(t *testing.T) {
	ops := &recordingPushOps{exists: true}
	err := tagAndPush(context.Background(), io.Discard, ops, imagePush{
		Registry: "registry.example.com", ImageName: "team/app", Version: "v4",
	})
	require.EqualError(t, err, "push source image reference cannot be empty")
	require.Empty(t, ops.existsRef)
	require.Empty(t, ops.tags)
	require.Empty(t, ops.pushes)
}
