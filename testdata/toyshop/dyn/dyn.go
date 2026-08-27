// Package dyn holds a function-value hook that is never assigned inside the
// module: calling it is statically unresolvable, so softmap must emit a
// terminal node tagged "dynamic" instead of silently dropping the call.
package dyn

var Hook func(event string, payload any)

func Publish(event string, payload any) {
	if Hook != nil {
		Hook(event, payload)
	}
}
