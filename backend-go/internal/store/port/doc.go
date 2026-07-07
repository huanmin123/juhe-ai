package port

// Package port defines business storage interfaces for the Go backend.
// PostgreSQL, Redis, Asynq, SQL dialect details, keys, pools and clients must
// stay in infrastructure adapters and must not leak into module services.
