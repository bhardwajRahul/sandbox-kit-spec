// Package adapter drives a candidate runtime through the conformance
// contract in docs/spec/conformance.md.
//
// The contract is an executable and a handful of verbs rather than a Go
// interface, so a runtime written in any language can be judged — and so
// the suite tests observable behavior rather than an implementation's
// internals.
package adapter
