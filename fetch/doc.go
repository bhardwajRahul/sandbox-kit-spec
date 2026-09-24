// Package fetch reads published kit descriptors from OCI registries.
//
// resolve and spec do no registry IO: a caller hands them descriptors
// it already has. This package is that fetch. Credentials and transport
// belong to the caller, so a private registry is reached the same way
// the rest of the program reaches it.
//
//	client, err := fetch.New(fetch.WithCredential(auth.StaticCredential(
//		"docker.io",
//		auth.Credential{Username: "user", Password: token},
//	)))
//	merged, err := client.Assemble(ctx, []string{
//		"docker.io/me/sbx-kit-hello:1.0.0",
//		"docker.io/me/sbx-kit-tool:1.0.0",
//	}, spec.MergeOptions{})
//
// Docker Hub redirects docker.io to registry-1.docker.io. auth.StaticCredential
// already accounts for that host; a hand-rolled credential func must too.
//
// Assemble resolves a runnable set (exactly one workload) and merges
// descriptors with spec.Merge. AssemblePartial is the mixin-only form.
// The merged image config is assemble.Merge, which needs the manifests
// and configs, not just these descriptors.
package fetch
