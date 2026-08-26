// Package jwt is a stub of golang-jwt with the signing surface the
// security effect detector matches.
package jwt

type Claims interface{}

type MapClaims map[string]interface{}

type SigningMethod interface{ Alg() string }

type hmacMethod struct{ name string }

func (m *hmacMethod) Alg() string { return m.name }

var SigningMethodHS256 SigningMethod = &hmacMethod{"HS256"}

type Token struct {
	Method SigningMethod
	Claims Claims
}

func NewWithClaims(method SigningMethod, claims Claims) *Token {
	return &Token{Method: method, Claims: claims}
}

func (t *Token) SignedString(key interface{}) (string, error) { return "token", nil }
