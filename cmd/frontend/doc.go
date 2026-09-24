// Command frontend is the sandbox-kit BuildKit frontend. A kit descriptor
// names this image on its first line (`# syntax=docker/sandbox-kit:3`, a
// valid YAML comment), so `docker buildx build . -f <name>.yaml` dispatches
// here with no wrapper and no sbx on the machine. The frontend parses the
// descriptor, builds the optional companion `<name>.dockerfile` through
// dockerfile.v0, validates the declaration against the image it just
// built, and publishes the descriptor as a manifest annotation.
//
// A kind:set descriptor is resolved here too: the kits it lists are
// fetched, judged with resolve, and merged — layers with assemble, and
// declarations with spec.Merge — into one ordinary kit.
package main
