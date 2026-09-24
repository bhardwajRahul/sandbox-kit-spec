// Package version is the build stamp the repository's two binaries carry:
// the released version and the commit they were built from.
//
// Both are set at link time, because neither binary can read git for
// itself. kit-tck is linked by GoReleaser from a tagged checkout, and the
// frontend is linked inside a Docker build whose context excludes .git —
// so the builder has to hand the values down. Where nobody did, the build
// info Go records answers instead, which keeps a plain `go build` or a
// `go install …@v3.0.0` from claiming to be a release it is not.
package version
