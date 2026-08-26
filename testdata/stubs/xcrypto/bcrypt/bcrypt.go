// Package bcrypt is a stub with the exact signatures the security effect
// detector matches.
package bcrypt

const DefaultCost = 10

func CompareHashAndPassword(hashedPassword, password []byte) error { return nil }

func GenerateFromPassword(password []byte, cost int) ([]byte, error) { return password, nil }
