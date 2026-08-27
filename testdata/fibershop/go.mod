module example.com/fibershop

go 1.22

require (
	github.com/gofiber/fiber/v2 v2.0.0
	github.com/gofiber/fiber/v3 v3.0.0
)

// All dependencies are local stubs so tests are hermetic and fast; the stubs
// carry only the signatures softmap matches on.
replace (
	github.com/gofiber/fiber/v2 => ../stubs/fiber
	github.com/gofiber/fiber/v3 => ../stubs/fiberv3
)
