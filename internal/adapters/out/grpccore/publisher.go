// Package grpccore implements an EventPublisher that sends events to gordon-core via gRPC.
// This is used by the registry component to notify core of image pushes.
package grpccore

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	gordonv1 "github.com/bnema/gordon/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// defaultTimeout is the default timeout for gRPC calls to core.
const defaultTimeout = 5 * time.Second

// EventPublisher implements out.EventPublisher by sending events to core via gRPC.
type EventPublisher struct {
	client gordonv1.CoreServiceClient
	conn   *grpc.ClientConn
}

// NewEventPublisher creates a new gRPC-based event publisher.
func NewEventPublisher(coreAddr string) (*EventPublisher, error) {
	conn, err := grpc.NewClient(coreAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to core: %w", err)
	}

	return &EventPublisher{
		client: gordonv1.NewCoreServiceClient(conn),
		conn:   conn,
	}, nil
}

// Close closes the gRPC connection.
func (p *EventPublisher) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// Publish sends an event to core via gRPC.
// Currently only supports EventImagePushed.
func (p *EventPublisher) Publish(eventType domain.EventType, payload any) error {
	switch eventType {
	case domain.EventImagePushed:
		return p.publishImagePushed(payload)
	default:
		// Silently ignore other event types - core doesn't need them
		return nil
	}
}

// publishImagePushed translates domain.ImagePushedPayload to NotifyImagePushed gRPC call.
func (p *EventPublisher) publishImagePushed(payload any) error {
	imagePushed, ok := payload.(domain.ImagePushedPayload)
	if !ok {
		return fmt.Errorf("invalid payload type for EventImagePushed: %T", payload)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	_, err := p.client.NotifyImagePushed(ctx, &gordonv1.NotifyImagePushedRequest{
		Name:        imagePushed.Name,
		Reference:   imagePushed.Reference,
		Manifest:    imagePushed.Manifest,
		Annotations: imagePushed.Annotations,
	})

	return err
}

// Ensure EventPublisher implements out.EventPublisher
var _ out.EventPublisher = (*EventPublisher)(nil)
