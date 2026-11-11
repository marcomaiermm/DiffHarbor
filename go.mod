module diffharbor

go 1.25

// go fetches this toolchain or newer (GOTOOLCHAIN=auto, the default; with local, install it): older ones carry
// fixed stdlib CVEs (x509, net/http).
toolchain go1.27.4
