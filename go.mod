module diffharbor

go 1.26.0

// go fetches this toolchain or newer (GOTOOLCHAIN=auto, the default; with local, install it): older ones carry
// fixed stdlib CVEs (x509, net/http).
toolchain go1.27.4

require golang.org/x/tools v0.52.0

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
)
