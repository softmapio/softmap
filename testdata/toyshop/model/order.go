package model

type Order struct {
	ID   string
	Item string
	Qty  int
}

// GetID exists to exercise the getter collapse heuristic.
func (o *Order) GetID() string { return o.ID }
