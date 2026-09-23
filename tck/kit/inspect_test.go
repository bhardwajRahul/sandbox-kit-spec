package kit

import (
	"context"
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

func TestInspectReturnsTheDescriptorAndTheStagedRecipe(t *testing.T) {
	a := conforming(t)
	recipe := []byte("FROM scratch\n")
	a.files[path.Join(StagedKitRoot, stem, stagedRecipeName)] = recipe

	in, err := Inspect(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, a.annotations[spec.AnnotationDescriptor], string(in.Descriptor))
	require.Equal(t, stem, in.Stem)
	require.Equal(t, recipe, in.Recipe)
	require.Equal(t, "/usr/share/sandbox/kit/demo/kit.dockerfile", in.RecipePath())
}

func TestInspectOfADeclarationOnlyKitHasNoRecipe(t *testing.T) {
	in, err := Inspect(context.Background(), conforming(t))
	require.NoError(t, err)
	require.Equal(t, stem, in.Stem)
	require.Nil(t, in.Recipe)
}

// A kit built FROM another kit carries both kits' staged sources; the
// recipe shown has to be this kit's, not its base's.
func TestInspectReadsTheRecipeOfTheKitItself(t *testing.T) {
	a := conforming(t)
	a.files[path.Join(StagedKitRoot, "base", stagedDescriptorName)] = []byte("schemaVersion: \"3\"\nkind: workload\n")
	a.files[path.Join(StagedKitRoot, "base", stagedRecipeName)] = []byte("FROM base\n")
	a.files[path.Join(StagedKitRoot, stem, stagedRecipeName)] = []byte("FROM demo\n")

	in, err := Inspect(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, stem, in.Stem)
	require.Equal(t, "FROM demo\n", string(in.Recipe))
}

// Inspection judges nothing, so what the strict decoder refuses is still
// shown — and the kit's own root is still found among several.
func TestInspectReadsADescriptorTheGrammarRefuses(t *testing.T) {
	for name, descriptor := range map[string]string{
		"unknown field":  `{"schemaVersion":"3","kind":"workload","colour":"blue"}`,
		"schema version": `{"schemaVersion":"2","kind":"workload"}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := conforming(t)
			a.annotations[spec.AnnotationDescriptor] = descriptor
			a.files = map[string][]byte{
				path.Join(StagedKitRoot, "base", stagedDescriptorName): []byte("schemaVersion: \"3\"\nkind: workload\n"),
				path.Join(StagedKitRoot, "base", stagedRecipeName):     []byte("FROM base\n"),
				path.Join(StagedKitRoot, stem, stagedDescriptorName):   []byte(descriptor),
				path.Join(StagedKitRoot, stem, stagedRecipeName):       []byte("FROM demo\n"),
			}

			in, err := Inspect(context.Background(), a)
			require.NoError(t, err)
			require.Equal(t, descriptor, string(in.Descriptor))
			require.Equal(t, stem, in.Stem)
			require.Equal(t, "FROM demo\n", string(in.Recipe))
		})
	}
}

func TestInspectOfAnUnparseableDescriptorFails(t *testing.T) {
	a := conforming(t)
	a.annotations[spec.AnnotationDescriptor] = "{not: [yaml"

	_, err := Inspect(context.Background(), a)
	require.ErrorContains(t, err, "does not parse")
}

func TestInspectOfSomethingThatIsNotAKitFails(t *testing.T) {
	a := conforming(t)
	delete(a.annotations, spec.AnnotationDescriptor)

	_, err := Inspect(context.Background(), a)
	require.ErrorIs(t, err, ErrNotAKit)
}
