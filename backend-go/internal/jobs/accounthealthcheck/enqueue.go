package accounthealthcheck

import "context"

type Enqueuer interface {
	Enqueue(ctx context.Context, task Task) error
}
