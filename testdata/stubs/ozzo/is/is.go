package is

import validation "github.com/go-ozzo/ozzo-validation/v4"

type digitRule struct{}

func (digitRule) Validate(interface{}) error { return nil }

var Digit validation.Rule = digitRule{}
