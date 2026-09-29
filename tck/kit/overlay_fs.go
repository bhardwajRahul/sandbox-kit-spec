package kit

import (
	"archive/tar"
	"context"
	"fmt"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/internal/overlayfs"
)

type overlayFS = overlayfs.FS

// overlayModel builds the shared extraction model once per artifact.
func (a *ociArtifact) overlayModel(ctx context.Context) (*overlayFS, error) {
	if a.overlay != nil {
		return a.overlay, nil
	}
	o := overlayfs.New()
	for _, layer := range a.manifest.Layers {
		rc, err := a.fetcher.Fetch(ctx, layer)
		if err != nil {
			return nil, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
		}
		upper := o.NewLayer()
		failed := false
		err = assemble.WalkLayer(rc, func(hdr *tar.Header) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Artifact checks inspect the prefix the extractor could apply. They
			// do not judge the unpack failure itself; runtime assembly does.
			if !failed && !upper.Add(hdr) {
				failed = true
			}
			return nil
		})
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		o.Apply(upper)
	}
	a.overlay = o
	return o, nil
}

func (a *ociArtifact) DirStat(ctx context.Context, name string) (FileStat, bool, error) {
	o, err := a.overlayModel(ctx)
	if err != nil {
		return FileStat{}, false, err
	}
	info, ok := o.DirStat(name)
	return FileStat{Mode: info.Mode, Uid: info.Uid, Gid: info.Gid}, ok, nil
}

func (a *ociArtifact) DanglingSymlinks(ctx context.Context) ([]Symlink, error) {
	o, err := a.overlayModel(ctx)
	if err != nil {
		return nil, err
	}
	var out []Symlink
	for _, link := range o.DanglingSymlinks() {
		out = append(out, Symlink{Path: link.Path, Target: link.Target})
	}
	return out, nil
}
