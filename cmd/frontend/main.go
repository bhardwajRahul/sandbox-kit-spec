// The sandbox-kit BuildKit frontend. A kit descriptor names this image on its
// first line (`# syntax=docker/sandbox-kit:3`, a valid YAML comment), so
// `docker buildx build . -f <name>.yaml` dispatches here with no wrapper and
// no sbx on the machine. The frontend parses the descriptor, builds the
// optional companion `<name>.dockerfile` through dockerfile.v0, validates the
// declaration against the image it just built, and publishes the descriptor
// as a manifest annotation.
package main

import (
	"github.com/moby/buildkit/frontend/gateway/grpcclient"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/bklog"
)

func main() {
	if err := grpcclient.RunFromEnvironment(appcontext.Context(), Build); err != nil {
		bklog.L.Errorf("fatal error: %+v", err)
		panic(err)
	}
}
