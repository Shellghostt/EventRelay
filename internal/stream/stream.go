// Package stream transports durable event IDs; PostgreSQL remains the source of truth.
package stream

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/shellghostt/eventrelay/internal/store"
)

const Group = "eventrelay-v1"

type Stream struct {
	Client *redis.Client
	Key    string
}

func Open(raw string) (*Stream, error) {
	o, err := redis.ParseURL(raw)
	if err != nil {
		return nil, errors.New("invalid EVENTRELAY_REDIS_URL")
	}
	o.DialTimeout = time.Second
	o.ReadTimeout = 2 * time.Second
	o.WriteTimeout = time.Second
	o.MaxRetries = -1
	o.ContextTimeoutEnabled = true
	return &Stream{Client: redis.NewClient(o), Key: "eventrelay:events"}, nil
}
func (s *Stream) Close() error                   { return s.Client.Close() }
func (s *Stream) Ping(ctx context.Context) error { return s.Client.Ping(ctx).Err() }
func (s *Stream) Ensure(ctx context.Context) error {
	err := s.Client.XGroupCreateMkStream(ctx, s.Key, Group, "0").Err()
	if err != nil && strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}
func (s *Stream) Publish(ctx context.Context, d store.Dispatch) error {
	return s.Client.XAdd(ctx, &redis.XAddArgs{Stream: s.Key, MaxLen: 10000, Approx: true, Values: map[string]any{"tenant_id": d.TenantID.String(), "event_id": d.EventID.String()}}).Err()
}
func (s *Stream) Read(ctx context.Context, consumer string) ([]redis.XMessage, error) {
	streams, err := s.Client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: Group, Consumer: consumer, Streams: []string{s.Key, ">"}, Count: 1, Block: time.Second}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(streams) == 0 {
		return nil, nil
	}
	return streams[0].Messages, nil
}
func (s *Stream) Ack(ctx context.Context, id string) error {
	p := s.Client.TxPipeline()
	p.XAck(ctx, s.Key, Group, id)
	p.XDel(ctx, s.Key, id)
	_, err := p.Exec(ctx)
	return err
}

func (s *Stream) CleanPending(ctx context.Context, idle time.Duration) error {
	entries, err := s.Client.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: s.Key, Group: Group, Idle: idle, Start: "-", End: "+", Count: 256}).Result()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := s.Ack(ctx, e.ID); err != nil {
			return err
		}
	}
	return nil
}
