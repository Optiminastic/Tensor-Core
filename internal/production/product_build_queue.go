package production

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// ProductBuildQueueName is where Product Creation's background builds wait -
// the sized moon lamp's top and cap, cut by OpenSCAD. One at a time: each is
// CPU-bound for several seconds on a box that also renders the floor's
// models, and an operator waiting on one file is not helped by two at once.
const ProductBuildQueueName = "product_creation"

// ProductBuildArgs builds one recorded product_creation_builds row.
type ProductBuildArgs struct {
	BuildID uuid.UUID `json:"build_id"`
}

func (ProductBuildArgs) Kind() string { return "product_creation_build" }

// InsertOpts routes the build to its own queue. Two attempts: a transient
// failure (a photo host blinking) gets one retry, a real one is reported.
func (ProductBuildArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: ProductBuildQueueName, MaxAttempts: 2}
}

// ProductBuildEnqueuer schedules a build.
type ProductBuildEnqueuer struct{ client *river.Client[pgx.Tx] }

// NewProductBuildEnqueuer builds an enqueuer for Product Creation builds.
func NewProductBuildEnqueuer(client *river.Client[pgx.Tx]) *ProductBuildEnqueuer {
	return &ProductBuildEnqueuer{client: client}
}

// Enqueue schedules one build.
func (e *ProductBuildEnqueuer) Enqueue(ctx context.Context, id uuid.UUID) error {
	if e == nil || e.client == nil {
		return errors.New("product builds are not configured on this service")
	}
	_, err := e.client.Insert(ctx, ProductBuildArgs{BuildID: id}, nil)
	return err
}
