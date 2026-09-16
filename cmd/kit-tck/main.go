// kit-tck judges conformance to the kit specification.
//
// `kit-tck kit` checks one artifact: that its annotations, layers, staged
// sources, and image config are what the spec requires. The same checks
// run inside the BuildKit frontend during a build; running them here is
// how an artifact built by anything else, or changed by an exporter or a
// registry on its way out, gets judged.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
	tcksandbox "github.com/docker/sandbox-kit-spec/v3/tck/sandbox"
)

// Set by GoReleaser via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kit-tck:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("a subcommand is required")
	}
	switch args[0] {
	case "kit":
		return runKit(args[1:])
	case "runtime":
		return runRuntime(args[1:])
	case "version", "-version", "--version":
		fmt.Println(version)
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
  kit-tck kit <reference>          check a kit in a registry
  kit-tck kit --plain-http <ref>   ... over HTTP (implied for localhost)
  kit-tck kit --layout <dir> <tag> check a kit in an OCI layout directory
  kit-tck runtime --adapter <path> [--fixtures <dir>]
                                    check a runtime through its adapter
  kit-tck version                  print the build version
`, version)
}

func runKit(args []string) error {
	fs := flag.NewFlagSet("kit", flag.ContinueOnError)
	layout := fs.String("layout", "", "read from an OCI layout directory instead of a registry")
	plainHTTP := fs.Bool("plain-http", false,
		"reach the registry over HTTP; implied for a loopback registry, which serves no TLS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		usage()
		return fmt.Errorf("exactly one reference is required")
	}

	ctx := context.Background()
	var (
		artifacts []tckkit.Artifact
		err       error
	)
	// Every runnable platform: §9 and §10 apply to each manifest, so a
	// multi-platform artifact conforms only if all of them do.
	if *layout != "" {
		artifacts, err = tckkit.FromLayoutAll(ctx, *layout, fs.Arg(0))
	} else {
		var opts []tckkit.RegistryOption
		if *plainHTTP {
			opts = append(opts, tckkit.WithPlainHTTP())
		}
		artifacts, err = tckkit.FromRegistryAll(ctx, fs.Arg(0), opts...)
	}
	if err != nil {
		return err
	}

	failed := false
	for _, artifact := range artifacts {
		rep, err := tckkit.Run(ctx, artifact)
		if err != nil {
			return err
		}
		if name := platformOf(artifact); name != "" {
			fmt.Printf("%s:\n", name)
		}
		fmt.Println(rep)
		failed = failed || rep.Failed()
	}
	if failed {
		return fmt.Errorf("%s does not conform", fs.Arg(0))
	}
	return nil
}

// runRuntime judges a candidate runtime through the adapter contract in
// docs/spec/conformance.md.
func runRuntime(args []string) error {
	fs := flag.NewFlagSet("runtime", flag.ContinueOnError)
	path := fs.String("adapter", "", "executable implementing the conformance contract")
	fixtures := fs.String("fixtures", "", "directory holding the suite's fixture kits (default: the shipped ones)")
	timeout := fs.Duration("timeout", 30*time.Minute,
		"overall deadline for the run; a hanging adapter fails with a timeout instead of hanging the suite")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		usage()
		return fmt.Errorf("--adapter is required")
	}
	root := *fixtures
	if root == "" {
		var cleanup func()
		var err error
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
	fmt.Println(rep)
	if rep.Failed() {
		return fmt.Errorf("runtime does not conform")
	}
	return nil
}

// platformOf names the image a report covers, when the artifact came from
// an index and there is more than one.
func platformOf(a tckkit.Artifact) string {
	if p, ok := a.(interface{ Platform() string }); ok {
		return p.Platform()
	}
	return ""
}
