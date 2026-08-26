// Package validation is a minimal stub of go-ozzo/ozzo-validation/v4
// carrying the shapes softmap's validation-rule extraction keys on.
package validation

type Rule interface{ Validate(value interface{}) error }

type stubRule struct{}

func (stubRule) Validate(interface{}) error { return nil }

var Required Rule = stubRule{}

func Length(min, max int) Rule { return stubRule{} }

func Validate(value interface{}, rules ...Rule) error { return nil }

type FieldRules struct{}

func Field(fieldPtr interface{}, rules ...Rule) *FieldRules { return &FieldRules{} }

func ValidateStruct(structPtr interface{}, fields ...*FieldRules) error { return nil }
