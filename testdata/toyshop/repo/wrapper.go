package repo

import (
	"context"

	"example.com/toyshop/model"
)

// Save is a deliberately trivial wrapper: the trivial-wrapper heuristic
// should collapse it into its caller, keeping save's SQL effect.
func (r *Repo) Save(ctx context.Context, o *model.Order) error {
	return r.save(ctx, o)
}
