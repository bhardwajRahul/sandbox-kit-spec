// Package kit checks whether an artifact is a conforming kit.
//
// The checks run in two places against the same definitions. The BuildKit
// frontend runs the ones observable before export, so `docker buildx
// build` fails on a malformed kit instead of publishing one; `kit-tck
// validate <ref>` runs all of them against a published image, which is
// the only way to see what the exporter and the registry actually did —
// and the only way to judge an artifact this frontend did not build.
package kit
