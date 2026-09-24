package specdocs

import "embed"

// Pages are the documents the suites name in their findings: the
// specification, the adapter contract, and every capability page.
//
//go:embed SPEC-v3.md conformance.md capabilities/com.docker.sandbox/*.md
var Pages embed.FS
