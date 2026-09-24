package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/internal/version"
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
	tcksandbox "github.com/docker/sandbox-kit-spec/v3/tck/sandbox"
)

// errNotConformant is the verdict, not an error to report: the run said
// what was wrong on stdout already, and main only has to fail the exit
// status without saying it twice.
var errNotConformant = errors.New("does not conform")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, errNotConformant) {
			fmt.Fprintln(os.Stderr, "kit-tck:", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("a subcommand is required")
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:])
	case "inspect":
		return runInspect(args[1:])
	case "runtime":
		return runRuntime(args[1:])
	case "version", "-version", "--version":
		fmt.Println(version.String())
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `kit-tck %s

usage:
  kit-tck validate <reference>     check a kit in a registry
  kit-tck validate --plain-http <ref>
                                   ... over HTTP (implied for localhost)
  kit-tck validate --layout <dir> <tag>
                                   check a kit in an OCI layout directory
  kit-tck inspect <reference>      print a kit's descriptor and content recipe
                                   (--layout and --plain-http as for validate)
  kit-tck inspect --dockerfile <ref>  ... only the recipe, as staged
  kit-tck inspect --descriptor <ref>  ... only the descriptor
  kit-tck inspect --platform <os/arch> <ref>
                                   ... from one image of a multi-platform kit
                                   (--descriptor/--dockerfile read the host's
                                   when the images differ)
  kit-tck runtime --adapter <path> [--fixtures <dir>]
                                    check a runtime through its adapter
  kit-tck version                  print the build version and revision

reporting:
  --verbose, -v                    list the checks that passed (validate, runtime)
  --format text|json               json reports every check and its spec link,
                                   or for inspect, the sources as data
  --color auto|always|never        auto follows the terminal and NO_COLOR
`, version.String())
}

// parse reads a subcommand's flags and returns what was left over.
//
// The flag package stops at the first argument that is not a flag, which
// would silently ignore `kit-tck validate <ref> --verbose` — the order most
// people type. Parsing resumes after each operand instead, so a flag is
// a flag wherever it appears. The package's own output is discarded: a
// bad flag should produce one usage block, not two.
func parse(fs *flag.FlagSet, args []string) (*presentation, []string, error) {
	p := addReportingFlags(fs)
	operands, err := parseOperands(fs, args)
	if err != nil {
		return nil, nil, err
	}
	return p, operands, nil
}

func parseOperands(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var operands []string
	for {
		if err := fs.Parse(args); err != nil {
			usage()
			return nil, err
		}
		if fs.NArg() == 0 {
			return operands, nil
		}
		operands = append(operands, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	layout := fs.String("layout", "", "read from an OCI layout directory instead of a registry")
	plainHTTP := fs.Bool("plain-http", false,
		"reach the registry over HTTP; implied for a loopback registry, which serves no TLS")
	presentation, operands, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(operands) != 1 {
		usage()
		return fmt.Errorf("exactly one reference is required")
	}
	reference := operands[0]

	ctx := context.Background()
	// Every runnable platform: §9 and §10 apply to each manifest, so a
	// multi-platform artifact conforms only if all of them do.
	target, artifacts, err := loadArtifacts(ctx, *layout, *plainHTTP, reference)
	if err != nil {
		return err
	}

	judged := outcome{suite: "validate", target: target}
	for _, artifact := range artifacts {
		rep, err := tckkit.Run(ctx, artifact)
		if err != nil {
			return err
		}
		judged.add(platformOf(artifact), rep)
	}
	return presentation.write(os.Stdout, judged)
}

// loadArtifacts reads every runnable platform of a kit from a layout or a
// registry, and names the target as the reader would have to type it
// again: a tag alone does not say which layout on disk it was read from.
func loadArtifacts(ctx context.Context, layout string, plainHTTP bool, reference string) (string, []tckkit.Artifact, error) {
	if layout != "" {
		artifacts, err := tckkit.FromLayoutAll(ctx, layout, reference)
		return layout + " " + reference, artifacts, err
	}
	var opts []tckkit.RegistryOption
	if plainHTTP {
		opts = append(opts, tckkit.WithPlainHTTP())
	}
	artifacts, err := tckkit.FromRegistryAll(ctx, reference, opts...)
	return reference, artifacts, err
}

// runRuntime judges a candidate runtime through the adapter contract in
// docs/spec/conformance.md.
func runRuntime(args []string) error {
	fs := flag.NewFlagSet("runtime", flag.ContinueOnError)
	path := fs.String("adapter", "", "executable implementing the conformance contract")
	fixtures := fs.String("fixtures", "", "directory holding the suite's fixture kits (default: the shipped ones)")
	timeout := fs.Duration("timeout", 30*time.Minute,
		"overall deadline for the run; a hanging adapter fails with a timeout instead of hanging the suite")
	presentation, _, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *path == "" {
		usage()
		return fmt.Errorf("--adapter is required")
	}
	root := *fixtures
	if root == "" {
		var cleanup func()
		if root, cleanup, err = tcksandbox.DefaultFixtureRoot(); err != nil {
			return err
		}
		defer cleanup()
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep, err := tcksandbox.Run(ctx, &tcksandbox.Env{
		Adapter:  adapter.New(*path),
		Fixtures: tcksandbox.Fixtures(root),
	})
	if err != nil {
		// A timeout is the harness's verdict on responsiveness, not a
		// capability finding, so it is named as what it is.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("the run exceeded --timeout=%s; the adapter or runtime is hanging: %w", *timeout, err)
		}
		return err
	}
	judged := outcome{suite: "runtime", target: *path}
	judged.add("", rep)
	return presentation.write(os.Stdout, judged)
}

// platformOf names the image a report covers, when the artifact came from
// an index and there is more than one.
func platformOf(a tckkit.Artifact) string {
	if p, ok := a.(interface{ Platform() string }); ok {
		return p.Platform()
	}
	return ""
}
