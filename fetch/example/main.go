// Command example fetches, expands, resolves, composes, and validates a set
// of published Kits, then assembles their image metadata. It reads
// []fetch.Request as JSON from stdin and writes typed results to stdout.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/fetch"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		// Validation errors already include source excerpts and locations.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
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
	result, err := client.Resolve(ctx, requests)
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
	output := struct {
		Resolved *fetch.Resolved
		Image    *assemble.Image
		Manifest ocispec.Manifest
	}{result, image, manifest}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
