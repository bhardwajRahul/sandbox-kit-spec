package kit

import (
	"archive/tar"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
)

const (
	// maxLookupSymlinks is the kernel's bound on the symlinks one path
	// lookup may expand (MAXSYMLINKS); past it the lookup fails with
	// ELOOP.
	maxLookupSymlinks = 40
	// maxExtractSymlinks is where containerd's path resolution gives up:
	// it refuses to expand another symlink once it has expanded more
	// than this many while resolving one path.
	maxExtractSymlinks = 255
	// maxPathLen bounds the names and symlink targets a Linux extractor
	// can create; longer ones fail with ENAMETOOLONG.
	maxPathLen = 4095
)

type nodeKind int

const (
	nodeDir nodeKind = iota
	nodeFile
	nodeSymlink
	// nodeWhiteout marks a path a layer deleted: in a layer's own tree
	// the device the overlay applier writes, in the stacked tree what is
	// left of it. Lookups treat it as absent.
	nodeWhiteout
)

// fsNode is one path in a tree. Only directories are ever modified after
// they are made; a hard link shares its target's node, so an alias keeps
// the inode it captured whatever later happens to the name it came from.
type fsNode struct {
	kind nodeKind
	link string
	// explicit marks a directory whose owner and mode below are known
	// from the overlay itself: it had an entry of its own, or the
	// extractor created it and found where to take its metadata from in
	// one of the overlay's lower layers. Otherwise that metadata comes
	// from the unknown base.
	explicit bool
	uid, gid int
	mode     int64
	// opaque marks a directory that hides what lower layers put in it.
	opaque   bool
	children map[string]*fsNode
}

func newDir() *fsNode {
	return &fsNode{kind: nodeDir, children: map[string]*fsNode{}}
}

// overlayFS is a mixin's layers stacked the way containerd's overlay
// applier and moby's overlay2 driver stack them: each layer is extracted
// into an empty directory of its own, where an entry's parent resolves
// only through what that same layer wrote, and the extracted layers are
// then merged as overlayfs merges them. It starts from an empty root,
// since the base the overlay lands on is unknown.
type overlayFS struct {
	root *fsNode
}

// overlayModel builds the model once per artifact.
func (a *ociArtifact) overlayModel(ctx context.Context) (*overlayFS, error) {
	if a.overlay != nil {
		return a.overlay, nil
	}
	o := &overlayFS{root: newDir()}
	// Every layer's own tree, lowest first: the extractor consults them
	// for the metadata of a directory it has to create.
	var layers []*fsNode
	for _, layer := range a.manifest.Layers {
		rc, err := a.fetcher.Fetch(ctx, layer)
		if err != nil {
			return nil, fmt.Errorf("fetch layer %s: %w", layer.Digest, err)
		}
		upper := newDir()
		err = assemble.WalkLayer(rc, func(hdr *tar.Header) error {
			extract(upper, layers, hdr)
			return nil
		})
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		stack(o.root, upper)
		layers = append(layers, upper)
	}
	a.overlay = o
	return o, nil
}

// DirStat is the metadata of the directory the layers expose at name,
// looked up literally rather than through links: what an overlay's own
// entry says is what replaces the base's. A directory whose metadata
// comes from the base reports false.
func (a *ociArtifact) DirStat(ctx context.Context, name string) (FileStat, bool, error) {
	o, err := a.overlayModel(ctx)
	if err != nil {
		return FileStat{}, false, err
	}
	n := o.root
	for _, seg := range strings.Split(strings.TrimPrefix(path.Clean("/"+name), "/"), "/") {
		if seg == "" {
			continue
		}
		if n = n.children[seg]; n == nil || n.kind != nodeDir {
			return FileStat{}, false, nil
		}
	}
	if !n.explicit {
		return FileStat{}, false, nil
	}
	return FileStat{Mode: n.mode, Uid: n.uid, Gid: n.gid}, true, nil
}

// DanglingSymlinks lists the symlinks the stacked layers expose whose
// targets they do not carry, resolved the way the kernel resolves them.
func (a *ociArtifact) DanglingSymlinks(ctx context.Context) ([]Symlink, error) {
	o, err := a.overlayModel(ctx)
	if err != nil {
		return nil, err
	}
	var out []Symlink
	dirs := []*fsNode{o.root}
	var walk func(dir *fsNode, dirPath string)
	walk = func(dir *fsNode, dirPath string) {
		for name, n := range dir.children {
			p := dirPath + "/" + name
			switch n.kind {
			case nodeDir:
				dirs = append(dirs, n)
				walk(n, p)
				dirs = dirs[:len(dirs)-1]
			case nodeSymlink:
				// The link itself is the first expansion.
				if lookup(o.root, dirs, n.link, 1) == nil {
					out = append(out, Symlink{Path: p, Target: n.link})
				}
			}
		}
	}
	walk(o.root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// extract applies one archive entry to a layer's own tree the way
// containerd's overlay applier does, given the trees of the layers below
// it: the name is cleaned as written and its parent resolved with
// rootPath, missing directories are created as mkparent creates them, a
// whiteout is placed only where nothing is, an opaque marker marks its
// directory except at the layer's root, where overlayfs ignores it, and
// any other entry replaces what held its path unless both are
// directories. An entry the extractor would refuse — and so fail the
// whole layer on — is not placed; that failure is not what these checks
// judge.
func extract(upper *fsNode, lowers []*fsNode, hdr *tar.Header) {
	if hdr.Typeflag == tar.TypeXGlobalHeader {
		return
	}
	// Cleaned as written, not rooted first: rootPath treats a symlink at
	// the top of a relative name differently from one under "/".
	name := path.Clean(hdr.Name)
	if name == "." || name == "/" || len(name) > maxPathLen {
		return
	}
	dir, base := path.Split(name)
	resolved, ok := rootPath(upper, dir)
	if !ok {
		return
	}
	parent, ok := mkparent(upper, lowers, resolved)
	if !ok {
		return
	}

	if base == ".wh..wh..opq" {
		if parent != upper {
			parent.opaque = true
		}
		return
	}
	if victim, ok := strings.CutPrefix(base, ".wh."); ok {
		if victim != "" && parent.children[victim] == nil {
			parent.children[victim] = &fsNode{kind: nodeWhiteout}
		}
		return
	}

	var n *fsNode
	switch hdr.Typeflag {
	case tar.TypeDir:
		if existing := parent.children[base]; existing != nil && existing.kind == nodeDir {
			existing.explicit, existing.uid, existing.gid, existing.mode = true, hdr.Uid, hdr.Gid, hdr.Mode
			return
		}
		n = newDir()
		n.explicit, n.uid, n.gid, n.mode = true, hdr.Uid, hdr.Gid, hdr.Mode
	case tar.TypeSymlink:
		if hdr.Linkname == "" || len(hdr.Linkname) > maxPathLen {
			return
		}
		n = &fsNode{kind: nodeSymlink, link: hdr.Linkname}
	case tar.TypeLink:
		// As containerd's hardlinkRootPath: the target's directory
		// through rootPath, then its last component literally, since
		// link(2) names a symlink rather than following it. A whiteout
		// is a device there, and linking to it makes another.
		tdir, tbase := path.Split(hdr.Linkname)
		tresolved, ok := rootPath(upper, tdir)
		if !ok {
			return
		}
		n = newResolver(upper).lstat(path.Join(tresolved, tbase))
		if n == nil || n.kind == nodeDir {
			return
		}
	default:
		n = &fsNode{kind: nodeFile}
	}
	parent.children[base] = n
}

// mkparent creates the directories along a path rootPath resolved that do
// not exist yet, as containerd's mkparent does, and returns the last one.
// rootPath has expanded every symlink on the way, so anything but a
// directory there — a file, or the device a whiteout is — makes it fail.
// A directory it creates takes its metadata from the first lower layer,
// nearest first, where the path resolves to anything: that layer's
// directory's, or root ownership and mode 0755 when something else is
// there. Where no lower layer has the path, the metadata comes from the
// base.
func mkparent(upper *fsNode, lowers []*fsNode, p string) (*fsNode, bool) {
	cur, at := upper, ""
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			continue
		}
		at += "/" + seg
		n := cur.children[seg]
		if n == nil {
			n = newDir()
			if !inheritDirInfo(n, lowers, at) {
				return nil, false
			}
			cur.children[seg] = n
		}
		if n.kind != nodeDir {
			return nil, false
		}
		cur = n
	}
	return cur, true
}

// inheritDirInfo gives a directory mkparent creates at p the metadata the
// lower layers dictate, reporting false where resolving p in one of them
// fails, which fails the entry.
func inheritDirInfo(n *fsNode, lowers []*fsNode, p string) bool {
	for i := len(lowers) - 1; i >= 0; i-- {
		resolved, ok := rootPath(lowers[i], p)
		if !ok {
			return false
		}
		found := newResolver(lowers[i]).lstat(resolved)
		switch {
		case found == nil:
			continue
		case found.kind == nodeDir:
			n.explicit, n.uid, n.gid, n.mode = found.explicit, found.uid, found.gid, found.mode
		default:
			n.explicit, n.uid, n.gid, n.mode = true, 0, 0, 0o755
		}
		return true
	}
	return true
}

// rootPath ports continuity's fs.RootPath, which containerd resolves an
// entry's parent and a hard link's target directory with, over a layer's
// own tree: walkLinks is repeated until a pass expands no symlink, a
// missing component is taken as a plain name, and resolution fails once
// more than maxExtractSymlinks symlinks have been expanded. A path that
// crosses no symlink resolves to itself, cleaned, without the walk.
func rootPath(t *fsNode, p string) (string, bool) {
	if p == "" {
		return "/", true
	}
	if !crossesSymlink(t, p) {
		return path.Clean("/" + p), true
	}
	r := newResolver(t)
	walked := 0
	for {
		before := walked
		next, ok := r.walkLinks(p, &walked)
		if !ok {
			return "", false
		}
		p = next
		if walked == before {
			return path.Clean("/" + p), true
		}
	}
}

// crossesSymlink reports whether walking p through a tree, ".." taken
// from wherever the walk is, meets a symlink.
func crossesSymlink(t *fsNode, p string) bool {
	dirs := []*fsNode{t}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".":
			continue
		case "..":
			if len(dirs) > 1 {
				dirs = dirs[:len(dirs)-1]
			}
			continue
		}
		var n *fsNode
		if top := dirs[len(dirs)-1]; top != nil {
			n = top.children[seg]
		}
		if n != nil && n.kind == nodeSymlink {
			return true
		}
		dirs = append(dirs, n)
	}
	return false
}

// resolver answers lstat for one tree, remembering the directories it has
// resolved: a walk asks for every prefix of a path in turn.
type resolver struct {
	t    *fsNode
	dirs map[string]*fsNode
}

func newResolver(t *fsNode) *resolver {
	return &resolver{t: t, dirs: map[string]*fsNode{}}
}

// walkLinks resolves the directory part of p before its last component
// and reports the result — for a symlink, its target: absolute as
// written, relative joined to the resolved directory. A symlink at the
// top of p comes back as written, to be walked again by rootPath.
func (r *resolver) walkLinks(p string, walked *int) (string, bool) {
	dir, file := path.Split(p)
	switch {
	case dir == "":
		next, _, ok := r.walkLink(file, walked)
		return next, ok
	case file == "":
		if dir == "/" {
			return dir, true
		}
		return r.walkLinks(dir[:len(dir)-1], walked)
	default:
		newdir, ok := r.walkLinks(dir, walked)
		if !ok {
			return "", false
		}
		next, isLink, ok := r.walkLink(path.Join(newdir, file), walked)
		if !ok || !isLink || strings.HasPrefix(next, "/") {
			return next, ok
		}
		return path.Join(newdir, next), true
	}
}

// walkLink reports the target of the symlink at p, or p itself when
// nothing or something else is there.
func (r *resolver) walkLink(p string, walked *int) (string, bool, bool) {
	if *walked > maxExtractSymlinks {
		return "", false, false
	}
	p = path.Join("/", p)
	if p == "/" {
		return p, false, true
	}
	n := r.lstat(p)
	if n == nil || n.kind != nodeSymlink {
		return p, false, true
	}
	*walked++
	return n.link, true, true
}

// lstat is what lstat(2) reports for p within the tree: its directory
// looked up as the kernel does, its last component as it is, a whiteout
// device included. The lookup stays inside the tree, where the extractor
// would resolve an absolute symlink against the host; rootPath has
// already bounded the paths it hands over, so the two agree.
func (r *resolver) lstat(p string) *fsNode {
	dir, base := path.Split(path.Clean("/" + p))
	if base == "" {
		return r.t
	}
	d := r.dir(path.Clean(dir))
	if d == nil {
		return nil
	}
	return d.children[base]
}

// dir resolves a clean absolute directory path as the kernel does,
// through each prefix it has already resolved.
func (r *resolver) dir(d string) *fsNode {
	if d == "/" {
		return r.t
	}
	if n, ok := r.dirs[d]; ok {
		return n
	}
	parent, base := path.Split(d)
	var n *fsNode
	if pn := r.dir(path.Clean(parent)); pn != nil {
		switch c := pn.children[base]; {
		case c == nil:
		case c.kind == nodeDir:
			n = c
		case c.kind == nodeSymlink:
			if found := lookup(r.t, []*fsNode{r.t}, d, 0); found != nil && found.kind == nodeDir {
				n = found
			}
		}
	}
	r.dirs[d] = n
	return n
}

// stack merges an extracted layer onto the layers below it, as overlayfs
// presents them: a whiteout hides the path, a directory over a directory
// merges with it (hiding its contents first when opaque), a directory
// shows its own metadata where it has one, and anything else replaces
// what was there.
func stack(lower, upper *fsNode) {
	if upper.opaque {
		lower.children = map[string]*fsNode{}
	}
	if upper.explicit {
		lower.explicit, lower.uid, lower.gid, lower.mode = true, upper.uid, upper.gid, upper.mode
	}
	for name, u := range upper.children {
		if u.kind != nodeDir {
			lower.children[name] = u
			continue
		}
		l := lower.children[name]
		if l == nil || l.kind != nodeDir {
			l = newDir()
			lower.children[name] = l
		}
		stack(l, u)
	}
}

// lookup resolves target the way the kernel's path lookup does, starting
// in the directory at the end of dirs, and returns what it reaches or
// nil: components in order, each symlink expanded before the ones after
// it — so a ".." climbs from where a link led — nothing reachable through
// a file or a whiteout, not even "" or ".", and at most maxLookupSymlinks
// expansions, followed of them already spent.
func lookup(root *fsNode, dirs []*fsNode, target string, followed int) *fsNode {
	stackDirs := append([]*fsNode{}, dirs...)
	if strings.HasPrefix(target, "/") {
		stackDirs = []*fsNode{root}
	}
	pending := pushPath(nil, target)
	for len(pending) > 0 {
		seg := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch seg {
		case "", ".":
			continue
		case "..":
			if len(stackDirs) > 1 {
				stackDirs = stackDirs[:len(stackDirs)-1]
			}
			continue
		}
		n := stackDirs[len(stackDirs)-1].children[seg]
		if n == nil {
			return nil
		}
		switch n.kind {
		case nodeDir:
			stackDirs = append(stackDirs, n)
		case nodeSymlink:
			if followed++; followed > maxLookupSymlinks {
				return nil
			}
			if strings.HasPrefix(n.link, "/") {
				stackDirs = []*fsNode{root}
			}
			pending = pushPath(pending, n.link)
		case nodeFile:
			if len(pending) > 0 {
				return nil
			}
			return n
		default:
			return nil
		}
	}
	return stackDirs[len(stackDirs)-1]
}

// pushPath pushes p's components onto a stack of components still to
// walk, the next one last, so expanding a symlink costs the length of its
// own target rather than of everything left to walk.
func pushPath(pending []string, p string) []string {
	segs := strings.Split(p, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		pending = append(pending, segs[i])
	}
	return pending
}
