// Package report is the vocabulary both conformance suites answer in: a
// finding names the requirement it judged, so a failure is traceable to
// the sentence of the specification it violates.
//
// The kit suite (tck/kit) and the runtime suite (tck/sandbox) both
// return a Report. Render writes it for a person; the JSON form is the
// same run for a program. speclink turns each finding's requirement id
// into a place in the specification text.
package report
