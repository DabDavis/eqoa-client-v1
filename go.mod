module github.com/eqoa/client

go 1.24.0

toolchain go1.24.4

require (
	github.com/DabDavis/eqoa-esf-tools v0.0.0
	github.com/gen2brain/raylib-go/raylib v0.55.1
)

require (
	github.com/ebitengine/purego v0.8.0 // indirect
	github.com/mattn/go-sqlite3 v1.14.34 // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/sys v0.37.0 // indirect
)

replace github.com/DabDavis/eqoa-esf-tools => ../go-eqoa-esf-tools-v1

replace github.com/gen2brain/raylib-go/raylib => ../raylib-go/raylib
