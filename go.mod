module diffharbor

go 1.25

// Local builds fetch this toolchain or newer: older ones carry fixed stdlib CVEs (x509, net/http).
toolchain go1.27.3
