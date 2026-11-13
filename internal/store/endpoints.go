package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Endpoint is a webhook destination plus its two secrets.
//
// IngestSecret verifies events sent *to* EventRelay by the producer.
// DeliverySecret signs events sent *from* EventRelay to URL.
type Endpoint struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	URL            string
	Description    string
	IngestSecret   []byte
	DeliverySecret []byte
	Enabled        bool
	MaxAttempts    int16
	Ordered        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateEndpointInput is the caller-supplied part of an endpoint.
type CreateEndpointInput struct {
	TenantID       uuid.UUID
	URL            string
	Description    string
	IngestSecret   []byte
	DeliverySecret []byte
	MaxAttempts    int16
}

const endpointColumns = `
	id::text, tenant_id::text, url, description, enabled,
	max_attempts, ordered, created_at, updated_at`

// CreateEndpoint inserts an endpoint and returns it, secrets included.
func (s *Store) CreateEndpoint(ctx context.Context, in CreateEndpointInput) (Endpoint, error) {
	const q = `
		INSERT INTO endpoints (tenant_id, url, description, ingest_secret, delivery_secret, max_attempts)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		RETURNING ` + endpointColumns

	var out Endpoint
	row := s.pool.QueryRow(ctx, q,
		in.TenantID.String(), in.URL, in.Description, in.IngestSecret, in.DeliverySecret, in.MaxAttempts)

	err := row.Scan(
		&out.ID, &out.TenantID, &out.URL, &out.Description, &out.Enabled,
		&out.MaxAttempts, &out.Ordered, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return Endpoint{}, fmt.Errorf("insert endpoint: %w", err)
	}

	out.IngestSecret = in.IngestSecret
	out.DeliverySecret = in.DeliverySecret
	return out, nil
}

// ListEndpoints returns endpoints for a tenant, newest first. Secrets are not
// populated: they are only available at creation time.
func (s *Store) ListEndpoints(ctx context.Context, tenantID uuid.UUID) ([]Endpoint, error) {
	const q = `
		SELECT ` + endpointColumns + `
		FROM endpoints
		WHERE tenant_id = $1::uuid
		ORDER BY created_at DESC, id DESC`

	rows, err := s.pool.Query(ctx, q, tenantID.String())
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	defer rows.Close()

	endpoints := make([]Endpoint, 0, 8)
	for rows.Next() {
		var e Endpoint
		if err := rows.Scan(
			&e.ID, &e.TenantID, &e.URL, &e.Description, &e.Enabled,
			&e.MaxAttempts, &e.Ordered, &e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan endpoint: %w", err)
		}
		endpoints = append(endpoints, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate endpoints: %w", err)
	}
	return endpoints, nil
}

// EndpointByID loads a single endpoint including its secrets. It is used by the
// ingest path, which is authorised by the signature rather than by a tenant, so
// there is deliberately no tenant filter here.
func (s *Store) EndpointByID(ctx context.Context, id uuid.UUID) (Endpoint, error) {
	const q = `
		SELECT ` + endpointColumns + `, ingest_secret, delivery_secret
		FROM endpoints
		WHERE id = $1::uuid`

	var e Endpoint
	row := s.pool.QueryRow(ctx, q, id.String())
	err := row.Scan(
		&e.ID, &e.TenantID, &e.URL, &e.Description, &e.Enabled,
		&e.MaxAttempts, &e.Ordered, &e.CreatedAt, &e.UpdatedAt,
		&e.IngestSecret, &e.DeliverySecret,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, ErrNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("get endpoint: %w", err)
	}
	return e, nil
}
