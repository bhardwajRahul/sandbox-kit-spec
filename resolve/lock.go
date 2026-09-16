package resolve

import (
	"encoding/json"
	"fmt"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// LockVersion is the current lock schema version.
const LockVersion = 1

// Lock is the reproducibility record of one resolution: exactly which
// manifests composed, in what order, with what argument values, under what
// permission surface. Recreate pulls by digest and compares; the gate diffs
// a candidate update's surface against the one recorded here — the surface
// the user approved.
type Lock struct {
	Version int         `json:"version"`
	Kits    []LockedKit `json:"kits"`
}

// LockedKit is one kit's lock entry, in composition order (workload first).
type LockedKit struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	// Image is the runtime-resolvable image reference (see Unit.Image).
	// Consumers assembling from the lock use it verbatim; empty in locks
	// recorded before source-form kits existed, where the pinned
	// Reference@Digest form is derivable instead.
	Image       string            `json:"image,omitempty"`
	Kind        string            `json:"kind"`
	Args        map[string]string `json:"args,omitempty"`
	Permissions spec.Surface      `json:"permissions"`
}

// LockFrom records a resolution.
func LockFrom(r *Resolution) *Lock {
	l := &Lock{Version: LockVersion}
	for _, u := range r.Ordered() {
		l.Kits = append(l.Kits, LockedKit{
			Reference:   u.Reference,
			Digest:      u.Digest,
			Image:       u.Image,
			Kind:        u.Descriptor.Kind,
			Args:        u.Args,
			Permissions: spec.SurfaceOf(u.Descriptor),
		})
	}
	return l
}

// Marshal renders the lock as stable, human-readable JSON. The CLI renders
// it; nothing edits it.
func (l *Lock) Marshal() ([]byte, error) {
	return json.MarshalIndent(l, "", "  ")
}

// ParseLock reads a lock, rejecting versions this code does not know.
func ParseLock(data []byte) (*Lock, error) {
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("parse kit lock: %w", err)
	}
	if l.Version != LockVersion {
		return nil, fmt.Errorf("kit lock version %d is not supported (this loader reads %d)", l.Version, LockVersion)
	}
	return &l, nil
}

// Gate diffs a fresh resolution against the locked grant, kit by kit,
// matched by reference. The returned widenings are what the user must
// approve before the new resolution replaces the lock; an empty result
// means the movement stays within what was already granted and applies
// silently. Kits absent from the lock are new grants: their whole surface
// is the widening.
func (l *Lock) Gate(r *Resolution) []spec.Widening {
	granted := map[string]spec.Surface{}
	for _, k := range l.Kits {
		granted[k.Reference] = k.Permissions
	}

	var out []spec.Widening
	for _, u := range r.Ordered() {
		out = append(out, spec.DiffWidenings(granted[u.Reference], spec.SurfaceOf(u.Descriptor))...)
	}
	return out
}

// VerifyRecreate checks that a resolution reproduces the lock: same kits by
// reference, same digests, same order. It is the recreate-from-lock
// invariant — any difference means the registry moved a tag or the caller
// changed the set, and the caller must re-resolve rather than silently
// diverge from the record.
func (l *Lock) VerifyRecreate(r *Resolution) error {
	ordered := r.Ordered()
	if len(ordered) != len(l.Kits) {
		return fmt.Errorf("kit lock records %d kits but the set resolved %d", len(l.Kits), len(ordered))
	}
	for i, u := range ordered {
		locked := l.Kits[i]
		if locked.Reference != u.Reference {
			return fmt.Errorf("kit lock position %d records %s but the set resolved %s", i, locked.Reference, u.Reference)
		}
		if locked.Digest != u.Digest {
			return fmt.Errorf("kit %s resolved to digest %s but the lock records %s; the tag moved — re-resolve to accept it", u.Reference, u.Digest, locked.Digest)
		}
	}
	return nil
}
