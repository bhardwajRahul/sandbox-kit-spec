// Package specdocs ships the specification text inside the binaries that
// judge against it. A finding names the statement it violated, and the
// only way a released kit-tck can turn that name into a place to read —
// with no checkout to consult — is to carry the pages it was built from.
package specdocs

import "embed"

// Pages are the documents the suites name in their findings: the
// specification, the adapter contract, and every capability page.
//
//go:embed SPEC-v3.md conformance.md capabilities/com.docker.sandbox/*.md
var Pages embed.FS
