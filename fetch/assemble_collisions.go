package fetch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/internal/overlayfs"
)

// ownershipNode indexes paths by component. Its first owned descendant makes
// subtree checks constant-time. Parent links construct a path only on failure;
// retaining a full path per implied directory would multiply deep-path storage.
type ownershipNode struct {
	name       string
	parent     *ownershipNode
	children   map[string]*ownershipNode
	kit        string
	directory  bool
	descendant *ownershipNode
}

func (n *ownershipNode) path() string {
	var components []string
	for ; n.parent != nil; n = n.parent {
		components = append(components, n.name)
	}
	slices.Reverse(components)
	return "/" + strings.Join(components, "/")
}

func checkFileCollisions(ctx context.Context, inventories []kitInventory) error {
	owners := &ownershipNode{}
	for _, inventory := range inventories {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		// Validate against lower Kits before inserting any of this Kit's nodes.
		// Walk both trees together: each implied directory is visited once,
		// rather than looking up every full prefix of a deeply nested file.
		stack := []*ownershipNode{owners}
		if err := model.Walk(func(entry overlayfs.Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			node := owners
			if entry.Depth != 0 {
				node = stack[entry.Depth-1]
				if node != nil {
					node = node.children[entry.Name]
				}
				stack = append(stack[:entry.Depth], node)
			}
			if node == nil {
				return nil
			}
			if node.kit != "" && (!entry.Directory || !node.directory) {
				return fileCollision(node.path(), node.kit, inventory.reference)
			}
			if previous := node.descendant; previous != nil && (!entry.Directory || entry.Opaque) {
				return fileCollision(previous.path(), previous.kit, inventory.reference)
			}
			return nil
		}); err != nil {
			return err
		}
		stack = stack[:1]
		if err := model.Walk(func(entry overlayfs.Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.Depth == 0 {
				return nil
			}
			// Deletions do not claim absent paths forever. Whiteouts have no
			// children, so the next entry needs only their parent's stack frame.
			stack = stack[:entry.Depth]
			if entry.Whiteout {
				return nil
			}
			parent := stack[entry.Depth-1]
			if parent.children == nil {
				parent.children = map[string]*ownershipNode{}
			}
			node := parent.children[entry.Name]
			if node == nil {
				node = &ownershipNode{name: entry.Name, parent: parent, kit: inventory.reference, directory: entry.Directory}
				parent.children[entry.Name] = node
			}
			if parent.descendant == nil {
				parent.descendant = node
			}
			stack = append(stack, node)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func fileCollision(path, lower, upper string) error {
	return fmt.Errorf("kit file collisions:\n  %s: %s replaces or deletes a path contributed by %s", path, upper, lower)
}
