// Command kit-tck judges conformance to the kit specification.
//
// `kit-tck validate` checks one artifact: that its annotations, layers,
// staged sources, and image config are what the spec requires. The same
// checks run inside the BuildKit frontend during a build; running them
// here is how an artifact built by anything else, or changed by an
// exporter or a registry on its way out, gets judged.
//
// `kit-tck inspect` prints the descriptor and the staged content recipe
// without judging either. `kit-tck runtime` drives a candidate runtime
// through the adapter contract and checks the behavior the capability
// pages require.
package main
