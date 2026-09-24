// Package speclink turns the requirement id that a finding names into a place
// in the specification where that requirement is written down.
//
// The suites already answer in the vocabulary of the pages: a kit check
// names a section, a runtime check names an anchored statement. Neither
// is a location, and a reader holding "lifecycle@1/install-once" has to
// go find it. Resolving the id against the embedded pages closes that
// gap, and closes it from the same text the coverage guard holds the
// suites to, so a link cannot point at a section that no longer exists.
package speclink
