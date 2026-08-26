module github.com/softmapio/softmap

// Track the newest Go release: the analyzer can only type-check language
// versions up to the toolchain it was built with, and `go install` selects
// its toolchain from this directive.
go 1.25.0

require (
	golang.org/x/tools v0.41.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	golang.org/x/mod v0.32.0 // indirect
	golang.org/x/sync v0.19.0 // indirect
)
