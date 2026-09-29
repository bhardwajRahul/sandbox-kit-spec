package fetch

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/internal/overlayfs"
)

type pathOwner struct {
	kit       string
	directory bool
}

func checkFileCollisions(ctx context.Context, inventories []kitInventory) error {
	owners := map[string]pathOwner{}
	for _, inventory := range inventories {
		model := overlayfs.New()
		for _, headers := range inventory.layers {
			layer := model.NewLayer()
			for i := range headers {
				if err := ctx.Err(); err != nil {
					return err
				}
				hdr := headers[i].header()
				if !layer.Add(&hdr) {
					return fmt.Errorf("kit %s: layer entry %q cannot be extracted", inventory.reference, hdr.Name)
				}
			}
			model.Apply(layer)
		}
		entries := model.Entries()
		// Compare against lower Kits before adding this Kit's surviving entries.
		// Deletions affect lower owners, but do not claim absent paths forever:
		// a later Kit may legitimately create a file this Kit removed internally.
		paths := slices.Sorted(maps.Keys(owners))
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if previous, exists := owners[entry.Path]; exists && (!entry.Directory || !previous.directory) {
				return fileCollision(entry.Path, previous.kit, inventory.reference)
			}
			if !entry.Directory || entry.Opaque {
				prefix := strings.TrimSuffix(entry.Path, "/") + "/"
				i := sort.SearchStrings(paths, prefix)
				if i < len(paths) && strings.HasPrefix(paths[i], prefix) {
					return fileCollision(paths[i], owners[paths[i]].kit, inventory.reference)
				}
			}
		}
		for _, entry := range entries {
			if entry.Whiteout {
				continue
			}
			if _, exists := owners[entry.Path]; !exists {
				owners[entry.Path] = pathOwner{kit: inventory.reference, directory: entry.Directory}
			}
		}
	}
	return nil
}

func fileCollision(path, lower, upper string) error {
	return fmt.Errorf("kit file collisions:\n  %s: %s replaces or deletes a path contributed by %s", path, upper, lower)
}
