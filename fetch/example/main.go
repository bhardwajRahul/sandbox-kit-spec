// Command example fetches, expands, selects, composes, and validates a set
// of published Kits, then assembles their image metadata. It reads
// []fetch.Request as JSON from stdin and writes typed results to stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	supportedTypes := flag.String("supported-types", strings.Join(spec.KnownCapabilities(), ","), "comma-separated capability types to accept in this preview")
	allowVolumes := flag.Bool("allow-volumes", true, "simulate host policy allowing persistent volumes")
	flag.Parse()

	claimed := strings.Split(*supportedTypes, ",")
	for i := range claimed {
		claimed[i] = strings.TrimSpace(claimed[i])
	}
	supported := spec.Supported(claimed...)
	selectCapability := func(capability spec.Capability) bool {
		if !supported(capability) {
			return false
		}
		// A real runtime uses host availability and policy here. The full
		// entry includes expanded config; deciding must not apply effects.
		if capability.Type == spec.CapabilityVolume {
			return *allowVolumes
		}
		return true
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx, selectCapability); err != nil {
		// Validation errors already include source excerpts and locations.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, selectCapability spec.SelectCapability) error {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read requests: %w", err)
	}
	var requests []fetch.Request
	if err := json.Unmarshal(raw, &requests); err != nil {
		return fmt.Errorf("decode requests: %w", err)
	}
	client, err := fetch.New(fetch.WithDockerCredentials())
	if err != nil {
		return err
	}
	// The API owns all-or-nothing groups, flattening, and composition.
	result, err := client.Resolve(ctx, requests, fetch.WithCapabilitySelector(selectCapability))
	if err != nil {
		return err
	}
	image, err := assemble.Assemble(ctx, result.Kits, client.LoadImage)
	if err != nil {
		return err
	}
	manifest, err := image.Manifest()
	if err != nil {
		return err
	}
	// The runtime imports the image and applies ContainerEnv when creating
	// the container. Keep Kits for handlers that need per-Kit declarations.
	// Persist the effective Descriptor and Selections for restart; only a
	// fresh creation resolves and selects again.
	output := struct {
		Resolved *fetch.Resolved
		Image    *assemble.Image
		Manifest ocispec.Manifest
	}{result, image, manifest}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
