package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestService_ListRoutesWithDetails(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{NetworkPrefix: "gordon"})
	ctx := testContext()

	svc.containers["app.example.com"] = &domain.Container{
		ID:     "container-1",
		Image:  "registry/app:latest",
		Status: "running",
		Labels: map[string]string{
			domain.LabelImage: "app:latest",
		},
	}

	runtime.EXPECT().GetContainerNetwork(mock.Anything, "container-1").Return("gordon-app-example-com", nil)
	// getAllAttachments calls GetContainerNetwork for each attachment
	runtime.EXPECT().GetContainerNetwork(mock.Anything, "attach-1").Return("gordon-app-example-com", nil)
	runtime.EXPECT().GetContainerNetwork(mock.Anything, "attach-2").Return("gordon-other-example-com", nil)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "attach-1",
			Name:   "gordon-app-example-com-postgres",
			Image:  "postgres:15",
			Status: "running",
			Labels: map[string]string{
				domain.LabelAttachment: "true",
				domain.LabelAttachedTo: "app.example.com",
				domain.LabelImage:      "postgres:15",
			},
		},
		{
			ID:     "attach-2",
			Name:   "gordon-other-redis",
			Image:  "redis:7",
			Status: "running",
			Labels: map[string]string{
				domain.LabelAttachment: "true",
				domain.LabelAttachedTo: "other.example.com",
				domain.LabelImage:      "redis:7",
			},
		},
		{
			ID:     "other",
			Name:   "gordon-other",
			Image:  "busybox",
			Status: "running",
			Labels: map[string]string{
				domain.LabelAttachment: "false",
			},
		},
	}, nil)

	results := svc.ListRoutesWithDetails(ctx)

	assert.Len(t, results, 1)
	assert.Equal(t, "app.example.com", results[0].Domain)
	assert.Equal(t, "app:latest", results[0].Image)
	assert.Equal(t, "container-1", results[0].ContainerID)
	assert.Equal(t, "running", results[0].ContainerStatus)
	assert.Equal(t, "gordon-app-example-com", results[0].Network)
	if assert.Len(t, results[0].Attachments, 1) {
		assert.Equal(t, "postgres", results[0].Attachments[0].Name)
		assert.Equal(t, "postgres:15", results[0].Attachments[0].Image)
		assert.Equal(t, "attach-1", results[0].Attachments[0].ContainerID)
		assert.Equal(t, "gordon-app-example-com", results[0].Attachments[0].Network)
	}
}

func TestService_ListRoutesWithDetails_Empty(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{NetworkPrefix: "gordon"})
	ctx := testContext()

	// getAllAttachments is called even with empty containers
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	results := svc.ListRoutesWithDetails(ctx)

	assert.Empty(t, results)
}

func TestService_ListRoutesWithDetails_NetworkError(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{NetworkPrefix: "gordon"})
	ctx := testContext()

	svc.containers["app.example.com"] = &domain.Container{
		ID:     "container-1",
		Image:  "app:latest",
		Status: "running",
	}

	runtime.EXPECT().GetContainerNetwork(mock.Anything, "container-1").Return("", assert.AnError)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	results := svc.ListRoutesWithDetails(ctx)

	assert.Len(t, results, 1)
	assert.Equal(t, "", results[0].Network)
}

func TestService_ListAttachments(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{})
	ctx := testContext()

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "attach-1",
			Name:   "gordon-app-example-com-postgres",
			Image:  "postgres:15",
			Status: "running",
			Labels: map[string]string{
				domain.LabelAttachment: "true",
				domain.LabelAttachedTo: "app.example.com",
				domain.LabelImage:      "postgres:15",
			},
		},
		{
			ID:     "attach-2",
			Name:   "gordon-other-redis",
			Image:  "redis:7",
			Status: "running",
			Labels: map[string]string{
				domain.LabelAttachment: "true",
				domain.LabelAttachedTo: "other.example.com",
				domain.LabelImage:      "redis:7",
			},
		},
	}, nil)
	runtime.EXPECT().GetContainerNetwork(mock.Anything, "attach-1").Return("gordon-app-example-com", nil)

	attachments := svc.ListAttachments(ctx, "app.example.com")

	assert.Len(t, attachments, 1)
	assert.Equal(t, "postgres", attachments[0].Name)
	assert.Equal(t, "postgres:15", attachments[0].Image)
	assert.Equal(t, "attach-1", attachments[0].ContainerID)
	assert.Equal(t, "gordon-app-example-com", attachments[0].Network)
}

func TestService_ListNetworks(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	svc := NewService(runtime, nil, nil, nil, Config{NetworkPrefix: "gordon"})
	ctx := testContext()

	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: "gordon-app"},
		{Name: "bridge"},
		{Name: "gordon-shared"},
	}, nil)

	networks, err := svc.ListNetworks(ctx)

	assert.NoError(t, err)
	assert.Len(t, networks, 2)
	assert.Equal(t, "gordon-app", networks[0].Name)
	assert.Equal(t, "gordon-shared", networks[1].Name)
}
