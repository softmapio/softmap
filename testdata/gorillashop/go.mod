module example.com/gorillashop

go 1.22

require github.com/gorilla/mux v1.8.0

// The dependency is a local stub so tests are hermetic and fast; the stub
// carries only the signatures softmap matches on.
replace github.com/gorilla/mux => ../stubs/gorilla
