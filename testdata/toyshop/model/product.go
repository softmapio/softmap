package model

// Product is the second business entity: the entity shelf must derive
// "product" from the products table, separately from orders.
type Product struct {
	ID    string
	Name  string
	Price int
}
