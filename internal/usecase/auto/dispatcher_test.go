package auto

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

type mockHandler struct {
	called bool
}

func (m *mockHandler) Handle(_ context.Context, _ domain.Event) error {
	m.called = true
	return nil
}

func (m *mockHandler) CanHandle(t domain.EventType) bool {
	return t == domain.EventImagePushed
}

type mockAutoConfig struct {
	previewEnabled bool
	tagPatterns    []string
}

func (m *mockAutoConfig) IsAutoEnabled() bool                    { return true }
func (m *mockAutoConfig) IsPreviewEnabled() bool                 { return m.previewEnabled }
func (m *mockAutoConfig) GetPreviewTagPatterns() []string        { return m.tagPatterns }
func (m *mockAutoConfig) GetPreviewConfig() domain.PreviewConfig { return domain.PreviewConfig{} }
func (m *mockAutoConfig) GetAllowedDomains() []string            { return nil }

func TestDispatcher_PreviewTag_DelegatesToPreview(t *testing.T) {
	route := &mockHandler{}
	preview := &mockHandler{}
	d := NewImagePushDispatcher(
		&mockAutoConfig{previewEnabled: true, tagPatterns: []string{"preview-*"}},
		route, preview,
	)
	event := domain.Event{
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{Name: "myapp", Reference: "preview-feat"},
	}
	err := d.Handle(context.Background(), event)
	assert.NoError(t, err)
	assert.True(t, preview.called)
	assert.False(t, route.called)
}

func TestDispatcher_NormalTag_DelegatesToRoute(t *testing.T) {
	route := &mockHandler{}
	preview := &mockHandler{}
	d := NewImagePushDispatcher(
		&mockAutoConfig{previewEnabled: true, tagPatterns: []string{"preview-*"}},
		route, preview,
	)
	event := domain.Event{
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{Name: "myapp", Reference: "v1.0.0"},
	}
	err := d.Handle(context.Background(), event)
	assert.NoError(t, err)
	assert.True(t, route.called)
	assert.False(t, preview.called)
}

func TestDispatcher_PreviewDisabled_AlwaysRoute(t *testing.T) {
	route := &mockHandler{}
	preview := &mockHandler{}
	d := NewImagePushDispatcher(
		&mockAutoConfig{previewEnabled: false, tagPatterns: []string{"preview-*"}},
		route, preview,
	)
	event := domain.Event{
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{Name: "myapp", Reference: "preview-feat"},
	}
	err := d.Handle(context.Background(), event)
	assert.NoError(t, err)
	assert.True(t, route.called)
	assert.False(t, preview.called)
}

func TestDispatcher_CanHandle(t *testing.T) {
	d := &ImagePushDispatcher{}
	assert.True(t, d.CanHandle(domain.EventImagePushed))
	assert.False(t, d.CanHandle(domain.EventConfigReload))
}
