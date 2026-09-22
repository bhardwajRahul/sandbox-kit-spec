package version

import "sync"

// reset clears the memoized stamp so a test can install its own. The
// answer is computed once in a real binary, where the linker values never
// change after start; only a test has reason to ask twice.
func reset() { resolve = sync.OnceValue(compute) }
