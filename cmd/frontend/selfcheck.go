package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// buildArtifact lets the conformance checks read the filesystem a build is
// about to export, so a malformed kit fails here instead of being
// published and found later. Layers do not exist yet — the exporter makes
// them — which the checks treat as unobservable rather than empty.
type buildArtifact struct {
	ctx         context.Context
	ref         gwclient.Reference
	annotations map[string]string
	config      *ocispecs.Image
}

func (a *buildArtifact) Annotations() map[string]string { return a.annotations }

func (a *buildArtifact) Config(context.Context) (*ocispecs.Image, error) { return a.config, nil }

func (a *buildArtifact) Layers(context.Context) ([]ocispecs.Descriptor, bool, error) {
	return nil, false, nil
}

func (a *buildArtifact) IndexAnnotations() (map[string]string, bool) { return nil, false }

func (a *buildArtifact) ReadFile(ctx context.Context, name string) ([]byte, bool, error) {
	// A declaration-only kit's solve produces no reference, so there is
	// nothing to read and the file is genuinely absent.
	if a.ref == nil {
		return nil, false, nil
	}
	// Bounded like the post-export source: an untrusted base image's
	// enormous file must not make the builder allocate without limit,
	// and the two sides have to agree on what is readable.
	body, err := a.ref.ReadFile(ctx, gwclient.ReadRequest{
		Filename: name,
		Range:    &gwclient.FileRange{Length: assemble.MaxFileEntryBytes + 1},
	})
	if err != nil {
		if isNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(body) > assemble.MaxFileEntryBytes {
		return nil, false, fmt.Errorf("%s exceeds %d bytes: %w", name, assemble.MaxFileEntryBytes, assemble.ErrFileTooLarge)
	}
	return body, true, nil
}

// HasFile establishes presence through a stat, so bulky staged bodies are
// never buffered just to learn they exist.
func (a *buildArtifact) HasFile(ctx context.Context, name string) (bool, error) {
	if a.ref == nil {
		return false, nil
	}
	st, err := a.ref.StatFile(ctx, gwclient.StatRequest{Path: name})
	if err != nil {
		if isNotExist(err) {
			return false, nil
		}
		return false, err
	}
	// A directory at the path is not a file there, which is what the
	// post-export source reports; the two sides of publication have to
	// agree on what is present.
	return !os.FileMode(st.Mode).IsDir(), nil
}

// FileStat reports the permission metadata of the filesystem about to be
// exported, so an image whose shells the declared user cannot run is
// refused here rather than after publication.
func (a *buildArtifact) FileStat(ctx context.Context, name string) (tckkit.FileStat, bool, error) {
	if a.ref == nil {
		return tckkit.FileStat{}, false, nil
	}
	// StatFile resolves the path within the root, final symlink
	// included, so what it reports is the target's, as the OCI source
	// reports after export.
	st, err := a.ref.StatFile(ctx, gwclient.StatRequest{Path: name})
	if err != nil {
		if isNotExist(err) {
			return tckkit.FileStat{}, false, nil
		}
		return tckkit.FileStat{}, false, err
	}
	mode := os.FileMode(st.Mode)
	return tckkit.FileStat{
		Mode:    int64(mode.Perm()),
		Uid:     int(st.Uid),
		Gid:     int(st.Gid),
		Regular: mode.IsRegular(),
	}, true, nil
}

// StagedStems enumerates the staged root of the filesystem about to be
// exported, exactly as the post-export OCI source will. Reporting the stem
// the frontend intended would let a base image's stale staged kit ride
// along unseen here and fail only after publication.
func (a *buildArtifact) StagedStems(ctx context.Context) ([]string, error) {
	if a.ref == nil {
		return nil, nil
	}
	entries, err := a.ref.ReadDir(ctx, gwclient.ReadDirRequest{Path: tckkit.StagedKitRoot})
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stems []string
	for _, entry := range entries {
		if !os.FileMode(entry.Mode).IsDir() {
			continue
		}
		name := path.Base(entry.Path)
		_, staged, err := a.ReadFile(ctx, path.Join(tckkit.StagedKitRoot, name, "kit.yaml"))
		if err != nil {
			return nil, err
		}
		if staged {
			stems = append(stems, name)
		}
	}
	return stems, nil
}

// isNotExist recognizes a missing path across the gateway, where the error
// crosses a gRPC boundary and arrives as a message rather than an fs error.
func isNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "not found")
}

// selfCheck judges what the build is about to export. It runs per platform
// against that platform's filesystem, because staging does too.
func selfCheck(ctx context.Context, ref gwclient.Reference, publishedJSON, imageConfigJSON []byte, annotations map[string]string) error {
	var img ocispecs.Image
	if err := json.Unmarshal(imageConfigJSON, &img); err != nil {
		return err
	}

	ann := map[string]string{}
	for k, v := range annotations {
		ann[k] = v
	}
	ann[spec.AnnotationDescriptor] = string(publishedJSON)

	report, err := tckkit.Run(ctx, &buildArtifact{
		ctx:         ctx,
		ref:         ref,
		annotations: ann,
		config:      &img,
	})
	if err != nil {
		return err
	}
	return report.Err()
}

// stagedGuidanceCollides reports whether a staged context body would
// overwrite the kit's own staged sources. Both land in the same directory,
// so a contentFile named kit.yaml would replace the descriptor a composed
// sandbox reads to know what the kit declared.
func stagedGuidanceCollides(contentFile string) bool {
	switch path.Base(contentFile) {
	case "kit.yaml", "kit.dockerfile":
		return true
	default:
		return false
	}
}
