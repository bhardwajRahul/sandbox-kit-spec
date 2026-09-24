// Package sandbox judges a candidate runtime against the behavior the
// capability pages require.
//
// The suite never inspects the runtime: it composes kits through the
// adapter contract and asserts what it can observe from inside the
// sandbox. A runtime therefore conforms by what it does, which is the
// only claim a specification can meaningfully make of an implementation
// it did not write.
package sandbox
