// Package report is the vocabulary both conformance suites answer in: a
// finding names the requirement it judged, so a failure is traceable to
// the sentence of the specification it violates.
package report

import (
	"fmt"
	"strings"
)

// Severity separates non-conformance from what is worth saying anyway.
type Severity int

const (
	// Fail marks a violated requirement.
	Fail Severity = iota
	// Warn marks a SHOULD, or an optimization a consumer must not depend
	// on.
	Warn
	// Skip marks a check that does not apply here: a source that cannot
	// observe it, or a capability the runtime does not claim.
	Skip
)

func (s Severity) String() string {
	switch s {
	case Fail:
		return "FAIL"
	case Warn:
		return "WARN"
	default:
		return "SKIP"
	}
}

// Finding is one check's verdict.
type Finding struct {
	// Check names the rule that ran.
	Check string
	// Requirement names the specification statement it judged.
	Requirement string
	Severity    Severity
	Detail      string
}

// Check is a rule that ran, recorded whether or not it had anything to
// say. A conforming run produces no findings at all, and without the
// roster a reader cannot tell that from a run where nothing was judged.
type Check struct {
	Name        string
	Requirement string
}

// Report collects the findings of one run, and the checks that produced
// them.
type Report struct {
	Findings []Finding
	// Checks is every check that ran, in the order the suite ran them.
	Checks []Check
}

// Add appends findings, filling in the check and requirement a rule did
// not set for itself, and records the check as having run — which is why
// a suite calls it for a check that found nothing too.
func (r *Report) Add(check, requirement string, findings ...Finding) {
	r.ran(Check{Name: check, Requirement: requirement})
	for _, f := range findings {
		if f.Check == "" {
			f.Check = check
		}
		if f.Requirement == "" {
			f.Requirement = requirement
		}
		r.Findings = append(r.Findings, f)
	}
}

// ran records a check once. A check reporting several findings calls Add
// once, but one reporting them one at a time — cleanup, naming a
// sandbox per line — would otherwise count as several checks.
func (r *Report) ran(c Check) {
	for _, seen := range r.Checks {
		if seen.Name == c.Name {
			return
		}
	}
	r.Checks = append(r.Checks, c)
}

// Summary counts the checks a run judged by the worst thing each one
// said, so the categories add up to the number of checks — several
// findings from one check are one failed check, and the findings
// themselves are the evidence under it.
type Summary struct {
	Checks  int
	Failed  int
	Warned  int
	Skipped int
	Passed  int
}

// Summary tallies the run.
func (r Report) Summary() Summary {
	worst := make(map[string]Severity, len(r.Checks))
	for _, f := range r.Findings {
		if s, seen := worst[f.Check]; !seen || f.Severity < s {
			worst[f.Check] = f.Severity
		}
	}
	out := Summary{Checks: len(r.Checks)}
	for _, c := range r.Checks {
		switch s, judged := worst[c.Name]; {
		case !judged:
			out.Passed++
		case s == Fail:
			out.Failed++
		case s == Warn:
			out.Warned++
		default:
			out.Skipped++
		}
	}
	return out
}

// Failed reports whether any requirement was violated.
func (r Report) Failed() bool {
	for _, f := range r.Findings {
		if f.Severity == Fail {
			return true
		}
	}
	return false
}

// Err returns the failures as one error, or nil when nothing failed.
func (r Report) Err() error {
	var failures []string
	for _, f := range r.Findings {
		if f.Severity == Fail {
			failures = append(failures, fmt.Sprintf("%s (%s): %s", f.Check, f.Requirement, f.Detail))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("conformance:\n  - %s", strings.Join(failures, "\n  - "))
}

// Failf returns a finding at Fail severity.
func Failf(format string, args ...any) Finding {
	return Finding{Severity: Fail, Detail: fmt.Sprintf(format, args...)}
}

// Warnf returns a finding at Warn severity.
func Warnf(format string, args ...any) Finding {
	return Finding{Severity: Warn, Detail: fmt.Sprintf(format, args...)}
}

// Skipf returns a finding recording why a check did not apply.
func Skipf(format string, args ...any) Finding {
	return Finding{Severity: Skip, Detail: fmt.Sprintf(format, args...)}
}
