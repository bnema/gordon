// Package events adapts the authenticated EventService to component event ports.
package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	eventsv1 "github.com/bnema/gordon/api/gordon/events/v1"
	inevents "github.com/bnema/gordon/internal/adapters/in/grpc/events"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	defaultInitialBackoff = 50 * time.Millisecond
	defaultMaxBackoff     = time.Second
	defaultAttempts       = 5
)

type Option func(*Client)

func WithRetry(initial, maximum time.Duration, attempts int) Option {
	return func(c *Client) {
		if initial > 0 && maximum >= initial {
			c.initialBackoff, c.maxBackoff = initial, maximum
		}
		if attempts > 0 {
			c.attempts = attempts
		}
	}
}

type Client struct {
	client                     eventsv1.EventServiceClient
	initialBackoff, maxBackoff time.Duration
	attempts                   int
	wait                       func(context.Context, time.Duration) bool
}

var _ out.ComponentEventPublisher = (*Client)(nil)

func NewClient(conn grpc.ClientConnInterface, options ...Option) *Client {
	return NewClientWithEventService(eventsv1.NewEventServiceClient(conn), options...)
}
func NewClientWithEventService(service eventsv1.EventServiceClient, options ...Option) *Client {
	c := &Client{client: service, initialBackoff: defaultInitialBackoff, maxBackoff: defaultMaxBackoff, attempts: defaultAttempts, wait: wait}
	for _, option := range options {
		if option != nil {
			option(c)
		}
	}
	return c
}

// PublishComponentEvent preserves event IDs and idempotency keys across bounded retries.
func (c *Client) PublishComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error {
	if ctx == nil {
		return errors.New("component event context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.client == nil {
		return errors.New("event service is required")
	}
	message, err := inevents.EventToProto(event)
	if err != nil {
		return fmt.Errorf("encode component event: %w", err)
	}
	backoff := c.initialBackoff
	for attempt := 0; ; attempt++ {
		_, err = c.client.PublishEvent(ctx, &eventsv1.PublishEventRequest{Event: message})
		if err == nil {
			return nil
		}
		if !transient(err) || attempt+1 >= c.attempts {
			return fmt.Errorf("publish component event: %w", err)
		}
		if !c.wait(ctx, backoff) {
			return ctx.Err()
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}

// Watch subscribes with bounded reconnect backoff. Reconnect delivery is safe
// because the server's hub sends its immutable latest event first.
func (c *Client) Watch(ctx context.Context) (<-chan domain.ComponentEventEnvelope, error) {
	if ctx == nil {
		return nil, errors.New("component event context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.client == nil {
		return nil, errors.New("event service is required")
	}
	out := make(chan domain.ComponentEventEnvelope, 1)
	go c.watch(ctx, out)
	return out, nil
}
func (c *Client) watch(ctx context.Context, out chan<- domain.ComponentEventEnvelope) {
	defer close(out)
	backoff := c.initialBackoff
	for attempts := 0; ctx.Err() == nil && attempts < c.attempts; attempts++ {
		stream, err := c.client.WatchEvents(ctx, &eventsv1.WatchEventsRequest{})
		if err == nil {
			for {
				message, recvErr := stream.Recv()
				if recvErr != nil {
					err = recvErr
					break
				}
				event, convertErr := inevents.EventFromProto(message.GetEvent(), domain.ComponentRole(message.GetEvent().GetOrigin()))
				if convertErr != nil {
					return
				}
				select {
				case <-ctx.Done():
					return
				case out <- event:
				}
			}
			if !transient(err) {
				return
			}
		}
		if !c.wait(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff, c.maxBackoff)
	}
}
func transient(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}
func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func nextBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum/2 {
		return maximum
	}
	return current * 2
}
