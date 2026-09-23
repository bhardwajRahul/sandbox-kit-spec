package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"gopkg.in/yaml.v3"

	"github.com/docker/sandbox-kit-spec/v3/internal/version"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// runInspect prints what a kit says about itself: the descriptor it was
// published with and the content recipe it staged. Nothing is judged, so
// a kit that does not conform can still be read.
func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	layout := fs.String("layout", "", "read from an OCI layout directory instead of a registry")
	plainHTTP := fs.Bool("plain-http", false,
		"reach the registry over HTTP; implied for a loopback registry, which serves no TLS")
	platform := fs.String("platform", "", "read only this image of a multi-platform kit")
	onlyDescriptor := fs.Bool("descriptor", false, "print only the descriptor")
	onlyRecipe := fs.Bool("dockerfile", false, "print only the content recipe")
	p := addOutputFlags(fs)
	operands, err := parseOperands(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		usage()
		return fmt.Errorf("exactly one reference is required")
	}
	if *onlyDescriptor && *onlyRecipe {
		return fmt.Errorf("--descriptor and --dockerfile each print one half; drop both to print the two")
	}
	if p.format != "text" && p.format != "json" {
		return fmt.Errorf("unknown --format %q; text or json", p.format)
	}

	ctx := context.Background()
	target, artifacts, err := loadArtifacts(ctx, *layout, *plainHTTP, operands[0])
	if err != nil {
		return err
	}
	if artifacts, err = selectPlatform(artifacts, *platform); err != nil {
		return err
	}
	inspected := inspection{target: target}
	// The descriptor is in the manifest annotation, so asking for it alone
	// fetches no layer; the staged sources are what cost the download.
	read := func(a tckkit.Artifact) (*tckkit.Inspection, error) { return tckkit.Inspect(ctx, a) }
	if *onlyDescriptor {
		read = func(a tckkit.Artifact) (*tckkit.Inspection, error) { return tckkit.InspectDescriptor(a) }
	}
	for _, artifact := range artifacts {
		in, err := read(artifact)
		if err != nil {
			return err
		}
		inspected.add(platformOf(artifact), in)
	}

	show := halves{descriptor: !*onlyRecipe, recipe: !*onlyDescriptor}
	switch {
	case p.format == "json":
		return writeInspectionJSON(os.Stdout, inspected, show)
	case *onlyRecipe, *onlyDescriptor:
		in, err := inspected.single(hostPlatform())
		if err != nil {
			return err
		}
		if *onlyDescriptor {
			return writeDescriptorYAML(os.Stdout, in.Descriptor)
		}
		if in.Recipe == nil {
			return fmt.Errorf("%s stages no content recipe: it is declaration-only, a set, or its sources are not staged", target)
		}
		_, err = os.Stdout.Write(in.Recipe)
		return err
	default:
		return p.writeInspectionText(os.Stdout, inspected)
	}
}

// selectPlatform narrows a kit to the one image asked for, or leaves every
// image when none was.
func selectPlatform(artifacts []tckkit.Artifact, want string) ([]tckkit.Artifact, error) {
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("the reference resolves to no runnable image")
	}
	if want == "" {
		return artifacts, nil
	}
	wanted, err := platforms.Parse(want)
	if err != nil {
		return nil, fmt.Errorf("--platform %q: %w", want, err)
	}
	var have []string
	for _, a := range artifacts {
		p := platformOf(a)
		if samePlatform(p, wanted) {
			return []tckkit.Artifact{a}, nil
		}
		have = append(have, p)
	}
	return nil, fmt.Errorf("no %s image; the kit carries %s", want, strings.Join(have, ", "))
}

// samePlatform compares an image's platform label with a requested one the
// way the resolver compares platforms: normalized, so linux/arm64 answers
// for the linux/arm64/v8 an index commonly writes and amd64 for amd64/v1,
// while arm/v7 still does not answer for arm/v6.
func samePlatform(label string, want ocispec.Platform) bool {
	have, err := platforms.Parse(label)
	if err != nil {
		return false
	}
	have, want = platforms.Normalize(have), platforms.Normalize(want)
	return have.OS == want.OS && have.Architecture == want.Architecture && have.Variant == want.Variant
}

// inspection is one run's result, grouped the way a validate run's reports
// are: platforms whose sources agree read as one group. A mixin's usually
// do; a workload's descriptor carries provides derived from each image's
// package database, which differ by architecture.
type inspection struct {
	target string
	groups []inspectedGroup
}

type inspectedGroup struct {
	platforms []string
	kit       *tckkit.Inspection
}

func (o *inspection) add(platform string, in *tckkit.Inspection) {
	for i, g := range o.groups {
		if sameInspection(g.kit, in) {
			o.groups[i].platforms = append(o.groups[i].platforms, platform)
			return
		}
	}
	var labels []string
	if platform != "" {
		labels = []string{platform}
	}
	o.groups = append(o.groups, inspectedGroup{platforms: labels, kit: in})
}

// single is the one reading raw output can print. Images that disagree —
// a workload's derived provides differ by architecture — cannot be
// concatenated into one file, so the host's image answers, the way
// `docker pull` would pick it.
func (o inspection) single(host string) (*tckkit.Inspection, error) {
	if len(o.groups) == 1 {
		return o.groups[0].kit, nil
	}
	var labels []string
	for _, g := range o.groups {
		for _, p := range g.platforms {
			if sameOSArch(p, host) {
				return g.kit, nil
			}
		}
		labels = append(labels, g.platforms...)
	}
	return nil, fmt.Errorf("the images of %s carry different kit sources and none is %s; pick one with --platform (%s)",
		o.target, host, strings.Join(labels, ", "))
}

// hostPlatform is the image a container here would run. Kits are Linux
// images, and Docker Desktop runs them in a Linux VM of the host's
// architecture, so the host's own OS does not enter into it.
func hostPlatform() string {
	return "linux/" + runtime.GOARCH
}

// sameOSArch compares platforms without their variant: the host knows its
// architecture, not which arm revision an index happened to label.
func sameOSArch(a, b string) bool {
	osArch := func(p string) string {
		parts := strings.SplitN(p, "/", 3)
		return strings.Join(parts[:min(len(parts), 2)], "/")
	}
	return osArch(a) == osArch(b)
}

func sameInspection(a, b *tckkit.Inspection) bool {
	return a.Stem == b.Stem &&
		bytes.Equal(a.Descriptor, b.Descriptor) &&
		bytes.Equal(a.Recipe, b.Recipe) && (a.Recipe == nil) == (b.Recipe == nil)
}

// halves says which of the two a run asked to see.
type halves struct {
	descriptor, recipe bool
}

// writeInspectionText lays the kit out the way a validate run lays out its
// report: the same header, a heading per platform group, and each half
// indented under a dim label naming where it lives in the filesystem.
func (p *presentation) writeInspectionText(w io.Writer, o inspection) error {
	tty, _ := terminal(w)
	style := ansi(p.painting(tty))

	out := &lines{w: w}
	out.printf("%s", style(dim, fmt.Sprintf("kit-tck %s · inspect · %s", version.String(), o.target)))
	for _, g := range o.groups {
		out.printf("")
		if len(g.platforms) > 0 {
			out.printf("%s", style(bold, strings.Join(g.platforms, ", ")))
		}
		in := g.kit

		where := in.DescriptorPath()
		if where == "" {
			where = "annotation only; no staged sources found"
		}
		out.printf("  %s  %s", style(dim, "descriptor"), style(dim, where))
		var descriptor bytes.Buffer
		if err := writeDescriptorYAML(&descriptor, in.Descriptor); err != nil {
			return err
		}
		out.block(descriptor.Bytes())

		out.printf("")
		if in.Recipe == nil {
			out.printf("  %s  %s", style(dim, "dockerfile"),
				style(dim, "none staged: the kit is declaration-only, a set, or its sources are not staged"))
			continue
		}
		out.printf("  %s  %s", style(dim, "dockerfile"), style(dim, in.RecipePath()))
		out.block(in.Recipe)
	}
	out.printf("")
	return out.err
}

// block writes content indented under its label. Blank lines stay blank,
// so nothing trails that a copy would carry along.
func (l *lines) block(content []byte) {
	for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
		if line == "" {
			l.printf("")
			continue
		}
		l.printf("    %s", line)
	}
}

// The JSON form carries the same envelope a validate run does, so one parser
// reads both; each result is one platform group's sources.
type jsonInspection struct {
	Tool     string                 `json:"tool"`
	Version  string                 `json:"version"`
	Revision string                 `json:"revision,omitempty"`
	Suite    string                 `json:"suite"`
	Target   string                 `json:"target"`
	Results  []jsonInspectionResult `json:"results"`
}

type jsonInspectionResult struct {
	Platforms      []string        `json:"platforms,omitempty"`
	DescriptorPath string          `json:"descriptorPath,omitempty"`
	Descriptor     json.RawMessage `json:"descriptor,omitempty"`
	DockerfilePath string          `json:"dockerfilePath,omitempty"`
	// Dockerfile is null rather than absent when the kit stages none, so
	// "has no recipe" and "was not asked for" read differently.
	Dockerfile json.RawMessage `json:"dockerfile,omitempty"`
}

func writeInspectionJSON(w io.Writer, o inspection, show halves) error {
	out := jsonInspection{
		Tool: "kit-tck", Version: version.Tag(), Revision: version.Rev(),
		Suite: "inspect", Target: o.target, Results: []jsonInspectionResult{},
	}
	for _, g := range o.groups {
		in := g.kit
		result := jsonInspectionResult{Platforms: g.platforms}
		if show.descriptor {
			descriptor, err := descriptorJSON(in.Descriptor)
			if err != nil {
				return err
			}
			result.DescriptorPath = in.DescriptorPath()
			result.Descriptor = descriptor
		}
		if show.recipe {
			result.Dockerfile = json.RawMessage("null")
			if in.Recipe != nil {
				recipe, err := json.Marshal(string(in.Recipe))
				if err != nil {
					return err
				}
				result.Dockerfile = recipe
				result.DockerfilePath = in.RecipePath()
			}
		}
		out.Results = append(out.Results, result)
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

// descriptorJSON is the annotation as JSON. A JSON annotation passes
// through untouched, keeping its published key order; an older kit's YAML
// annotation is converted, since embedding it raw would not be JSON.
func descriptorJSON(annotation []byte) (json.RawMessage, error) {
	if json.Valid(annotation) {
		return json.RawMessage(annotation), nil
	}
	var doc any
	if err := yaml.Unmarshal(annotation, &doc); err != nil {
		return nil, fmt.Errorf("descriptor annotation does not parse: %w", err)
	}
	converted, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("descriptor annotation has no JSON form: %w", err)
	}
	return converted, nil
}

// writeDescriptorYAML renders the annotation's JSON as block YAML. It goes
// through a node tree rather than a map so the published key order
// survives.
func writeDescriptorYAML(w io.Writer, descriptor []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(descriptor, &doc); err != nil {
		return fmt.Errorf("descriptor annotation does not decode: %w", err)
	}
	blockStyle(&doc)
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return err
	}
	return encoder.Close()
}

// blockStyle clears the flow and quoting styles JSON parses into. The
// encoder quotes any string that would otherwise read as another type.
func blockStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		blockStyle(c)
	}
}
