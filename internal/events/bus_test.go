package events

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockEventHandler is a mock implementation of EventHandler
type MockEventHandler struct {
	mock.Mock
	mu       sync.Mutex
	events   []Event
	canHandle bool
}

func (m *MockEventHandler) Handle(event Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	args := m.Called(event)
	return args.Error(0)
}

func (m *MockEventHandler) CanHandle(eventType EventType) bool {
	args := m.Called(eventType)
	return args.Bool(0)
}

func (m *MockEventHandler) GetReceivedEvents() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Event{}, m.events...)
}

func TestInMemoryEventBus_NewInMemoryEventBus(t *testing.T) {
	bus := NewInMemoryEventBus(50)
	
	assert.NotNil(t, bus)
	assert.Equal(t, 50, cap(bus.eventChan))
	assert.Empty(t, bus.handlers)
	assert.Equal(t, 50, bus.bufferSize)
}

func TestInMemoryEventBus_Subscribe(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	handler := &MockEventHandler{}
	
	bus.Subscribe(handler)
	
	assert.Len(t, bus.handlers, 1)
	assert.Contains(t, bus.handlers, handler)
}

func TestInMemoryEventBus_Subscribe_DuplicateHandler(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	handler := &MockEventHandler{}
	
	bus.Subscribe(handler)
	bus.Subscribe(handler) // Subscribe same handler twice
	
	// Implementation allows duplicates (handlers are appended to slice)
	assert.Len(t, bus.handlers, 2)
}

func TestInMemoryEventBus_Unsubscribe(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	handler1 := &MockEventHandler{}
	handler2 := &MockEventHandler{}
	
	bus.Subscribe(handler1)
	bus.Subscribe(handler2)
	assert.Len(t, bus.handlers, 2)
	
	err := bus.Unsubscribe(handler1)
	
	assert.NoError(t, err)
	assert.Len(t, bus.handlers, 1)
	// Check that handler2 is still in the list (compare by position)
	assert.Equal(t, handler2, bus.handlers[0])
}

func TestInMemoryEventBus_Unsubscribe_NonExistentHandler(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	handler1 := &MockEventHandler{}
	handler2 := &MockEventHandler{}
	
	bus.Subscribe(handler1)
	err := bus.Unsubscribe(handler2) // Try to unsubscribe handler that wasn't subscribed
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler not found")
	assert.Len(t, bus.handlers, 1)
	assert.Equal(t, handler1, bus.handlers[0])
}

func TestInMemoryEventBus_StartStop(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	// Start the bus
	err := bus.Start()
	assert.NoError(t, err)
	
	// Stop the bus
	err = bus.Stop()
	assert.NoError(t, err)
}

func TestInMemoryEventBus_Publish_Running(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	handler := &MockEventHandler{}
	handler.On("CanHandle", ImagePushed).Return(true)
	handler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	bus.Subscribe(handler)
	bus.Start()
	defer bus.Stop()
	
	payload := ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	}
	
	bus.Publish(ImagePushed, payload)
	
	// Give some time for async processing
	time.Sleep(50 * time.Millisecond)
	
	handler.AssertExpectations(t)
	events := handler.GetReceivedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, ImagePushed, events[0].Type)
	assert.Equal(t, "nginx:latest", events[0].ImageName)
	assert.Equal(t, "latest", events[0].Tag)
}

func TestInMemoryEventBus_Publish_NotRunning(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	handler := &MockEventHandler{}
	bus.Subscribe(handler)
	
	payload := ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	}
	
	// Publish when bus is not running
	bus.Publish(ImagePushed, payload)
	
	// Give some time in case it processes (it shouldn't)
	time.Sleep(10 * time.Millisecond)
	
	// Handler should not have received any events
	events := handler.GetReceivedEvents()
	assert.Empty(t, events)
}

func TestInMemoryEventBus_HandlerFiltering(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	// Handler that only handles ImagePushed events
	imagePushedHandler := &MockEventHandler{}
	imagePushedHandler.On("CanHandle", ImagePushed).Return(true)
	imagePushedHandler.On("CanHandle", ConfigReload).Return(false)
	imagePushedHandler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	// Handler that only handles ConfigReload events
	configReloadHandler := &MockEventHandler{}
	configReloadHandler.On("CanHandle", ImagePushed).Return(false)
	configReloadHandler.On("CanHandle", ConfigReload).Return(true)
	configReloadHandler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	bus.Subscribe(imagePushedHandler)
	bus.Subscribe(configReloadHandler)
	bus.Start()
	defer bus.Stop()
	
	// Publish ImagePushed event
	bus.Publish(ImagePushed, ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	})
	
	// Publish ConfigReload event
	bus.Publish(ConfigReload, nil)
	
	// Give time for processing
	time.Sleep(20 * time.Millisecond)
	
	// Check that each handler only received appropriate events
	imagePushedEvents := imagePushedHandler.GetReceivedEvents()
	configReloadEvents := configReloadHandler.GetReceivedEvents()
	
	require.Len(t, imagePushedEvents, 1)
	assert.Equal(t, ImagePushed, imagePushedEvents[0].Type)
	
	require.Len(t, configReloadEvents, 1)
	assert.Equal(t, ConfigReload, configReloadEvents[0].Type)
	
	imagePushedHandler.AssertExpectations(t)
	configReloadHandler.AssertExpectations(t)
}

func TestInMemoryEventBus_HandlerError(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	// Handler that returns an error
	errorHandler := &MockEventHandler{}
	errorHandler.On("CanHandle", ImagePushed).Return(true)
	errorHandler.On("Handle", mock.AnythingOfType("Event")).Return(assert.AnError)
	
	// Handler that succeeds
	successHandler := &MockEventHandler{}
	successHandler.On("CanHandle", ImagePushed).Return(true)
	successHandler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	bus.Subscribe(errorHandler)
	bus.Subscribe(successHandler)
	bus.Start()
	defer bus.Stop()
	
	payload := ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	}
	
	bus.Publish(ImagePushed, payload)
	
	// Give time for processing
	time.Sleep(20 * time.Millisecond)
	
	// Both handlers should have been called despite error
	errorHandler.AssertExpectations(t)
	successHandler.AssertExpectations(t)
	
	// Both should have received the event
	errorEvents := errorHandler.GetReceivedEvents()
	successEvents := successHandler.GetReceivedEvents()
	
	assert.Len(t, errorEvents, 1)
	assert.Len(t, successEvents, 1)
}

func TestInMemoryEventBus_ContextCancellation(t *testing.T) {
	bus := NewInMemoryEventBus(10)
	
	handler := &MockEventHandler{}
	handler.On("CanHandle", ImagePushed).Return(true)
	handler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	bus.Subscribe(handler)
	bus.Start()
	
	// Stop the bus to trigger shutdown
	err := bus.Stop()
	assert.NoError(t, err)
}

func TestInMemoryEventBus_FullChannel(t *testing.T) {
	// Create bus with very small buffer
	bus := NewInMemoryEventBus(1)
	
	handler := &MockEventHandler{}
	handler.On("CanHandle", ImagePushed).Return(true)
	// Make handler slow to process events
	handler.On("Handle", mock.AnythingOfType("Event")).Return(nil).Run(func(args mock.Arguments) {
		time.Sleep(50 * time.Millisecond)
	})
	
	bus.Subscribe(handler)
	bus.Start()
	defer bus.Stop()
	
	payload := ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	}
	
	// Publish multiple events quickly to potentially fill the channel
	for i := 0; i < 5; i++ {
		bus.Publish(ImagePushed, payload)
	}
	
	// Give time for processing
	time.Sleep(200 * time.Millisecond)
	
	// Should still work, events might be dropped but no crash
	events := handler.GetReceivedEvents()
	assert.True(t, len(events) >= 1) // At least some events should be processed
}

func TestInMemoryEventBus_ConcurrentPublish(t *testing.T) {
	bus := NewInMemoryEventBus(100)
	
	handler := &MockEventHandler{}
	handler.On("CanHandle", ImagePushed).Return(true)
	handler.On("Handle", mock.AnythingOfType("Event")).Return(nil)
	
	bus.Subscribe(handler)
	bus.Start()
	defer bus.Stop()
	
	payload := ImagePushedPayload{
		Name:      "nginx:latest",
		Reference: "latest",
	}
	
	// Publish events concurrently from multiple goroutines
	numGoroutines := 10
	eventsPerGoroutine := 5
	var wg sync.WaitGroup
	
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				bus.Publish(ImagePushed, payload)
			}
		}()
	}
	
	wg.Wait()
	
	// Give time for processing
	time.Sleep(100 * time.Millisecond)
	
	events := handler.GetReceivedEvents()
	expectedEvents := numGoroutines * eventsPerGoroutine
	assert.Equal(t, expectedEvents, len(events))
}