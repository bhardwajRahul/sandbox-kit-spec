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

// Report collects the findings of one run.
type Report struct {
	Findings []Finding
}

// Add appends findings, filling in the check and requirement a rule did
// not set for itself.
func (r *Report) Add(check, requirement string, findings ...Finding) {
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

func (r Report) String() string {
	if len(r.Findings) == 0 {
		return "conforms"
	}
	var b strings.Builder
	for i, f := range r.Findings {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-4s %-34s %s", f.Severity, f.Check, f.Detail)
		if f.Requirement != "" && f.Requirement != f.Check {
			fmt.Fprintf(&b, "  [%s]", f.Requirement)
		}
	}
	return b.String()
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
