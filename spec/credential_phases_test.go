package spec

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func credentialPhaseKit(t *testing.T, phase string) *Descriptor {
	t.Helper()
	return mustDecode(t, "schemaVersion: '3'\nkind: workload\ncapabilities:\n  - type: "+CapabilityCredential+"\n    name: GitHub\n    description: Authenticate to GitHub\n    optional: true\n    config:\n      service: github\n      phase: "+phase+"\n      apiKey: {name: GH_TOKEN}\n")
}

func TestCredentialPhaseGrammar(t *testing.T) {
	for _, phase := range []string{"install", "runtime", "[runtime]", "[install, runtime]", "[runtime, install]"} {
		t.Run(phase, func(t *testing.T) {
			d := credentialPhaseKit(t, phase)
			_, err := Validate(d)
			require.NoError(t, err)
			creds, err := CredentialsOf(d.Capabilities)
			require.NoError(t, err)
			require.Len(t, creds, 1)
			encoded, err := json.Marshal(creds[0].Credential)
			require.NoError(t, err)
			var roundTrip Credential
			require.NoError(t, json.Unmarshal(encoded, &roundTrip))
			require.Equal(t, creds[0].Credential, roundTrip)
			if len(roundTrip.Phase) == 1 {
				require.Contains(t, string(encoded), `"phase":"`+roundTrip.Phase[0]+`"`)
			}
		})
	}
	for _, phase := range []string{"null", "[]", `""`, "always", "[runtime, runtime]", "[install, always]", "[install, null]", "[install, 42]", "42", "{}"} {
		t.Run("invalid/"+phase, func(t *testing.T) {
			_, err := Validate(credentialPhaseKit(t, phase))
			require.Error(t, err)
		})
	}
}

func TestCredentialPhasesAccessorsSurfaceAndComposition(t *testing.T) {
	d := credentialPhaseKit(t, "[install, runtime]")
	for _, phase := range []string{"install", "runtime"} {
		creds, err := CredentialsOfPhase(d.Capabilities, phase)
		require.NoError(t, err)
		require.Len(t, creds, 1)
		require.Equal(t, Phases{"install", "runtime"}, creds[0].Phase)
		require.Equal(t, "GitHub", creds[0].Name)
		require.Equal(t, "Authenticate to GitHub", creds[0].Description)
		require.False(t, creds[0].Required)
	}
	unknown, err := CredentialsOfPhase(d.Capabilities, "unknown")
	require.NoError(t, err)
	require.Empty(t, unknown)
	surface := SurfaceOf(d)
	require.Equal(t, []string{"github"}, surface.CredentialsInstall)
	require.Equal(t, []string{"github"}, surface.CredentialsRuntime)
	widenings := DiffWidenings(SurfaceOf(credentialPhaseKit(t, "install")), surface)
	require.Len(t, widenings, 1)
	require.Equal(t, "credentials.runtime: github", widenings[0].String())

	composed, err := Compose([]Contribution{{Reference: "both", Descriptor: d}})
	require.NoError(t, err)
	require.Equal(t, surface, SurfaceOf(composed))
	creds, err := CredentialsOf(composed.Capabilities)
	require.NoError(t, err)
	require.Len(t, creds, 2)
	for _, c := range creds {
		require.Len(t, c.Phase, 1)
		require.Equal(t, "GitHub", c.Name)
		require.False(t, c.Required)
	}
	for _, phase := range []string{"install", "runtime", "[runtime]"} {
		other := credentialPhaseKit(t, phase)
		other.Kind = KindMixin
		_, err := Compose([]Contribution{{Reference: "both", Descriptor: d}, {Reference: "other", Descriptor: other}})
		require.ErrorContains(t, err, "one credential has one owner")
	}
	install := credentialPhaseKit(t, "install")
	runtime := credentialPhaseKit(t, "runtime")
	runtime.Kind = KindMixin
	_, err = Compose([]Contribution{{Reference: "install", Descriptor: install}, {Reference: "runtime", Descriptor: runtime}})
	require.NoError(t, err)
}

func TestCredentialPhasesRequireEveryNetworkAllowList(t *testing.T) {
	for _, typ := range []string{CapabilityNetworkPolicy, CapabilityNetworkPolicyV2} {
		for _, missing := range []string{"", "install", "runtime"} {
			d := credentialPhaseKit(t, "[install, runtime]")
			d.Capabilities[0].Config["apiKey"] = map[string]any{"inject": []any{map[string]any{"domain": "api.github.com"}}}
			config := map[string]any{}
			for _, phase := range []string{"install", "runtime"} {
				if phase != missing {
					config[phase] = map[string]any{"allow": []any{"api.github.com"}}
				}
			}
			d.Capabilities = append(d.Capabilities, Capability{Type: typ, Config: config})
			_, err := Validate(d)
			if missing == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, missing+" allow list")
			}
		}
	}
}

func TestCredentialPhasesOverlapBeforeExpansion(t *testing.T) {
	for _, parameterized := range []bool{false, true} {
		for _, grouped := range []bool{false, true} {
			d := credentialPhaseKit(t, "[install, runtime]")
			if parameterized {
				d.Args = map[string]Arg{"managed": {}}
				d.Capabilities[0].Config["apiKey"].(map[string]any)["proxyManaged"] = "${{ kit.args.managed }}"
			}
			d.Capabilities = append(d.Capabilities, credentialPhaseKit(t, "[runtime]").Capabilities...)
			if grouped {
				for i := range d.Capabilities {
					d.Capabilities[i].Optional = false
					d.Capabilities[i].optionalSet = false
				}
				d.Capabilities = []Capability{{Group: &CapabilityGroup{Optional: true, Capabilities: d.Capabilities}}}
			}
			_, err := Validate(d)
			require.ErrorContains(t, err, "already declared")
		}
	}
}

func TestCredentialPhasesExpansion(t *testing.T) {
	for _, source := range []string{"args", "env"} {
		for _, phase := range []string{"install", "runtime", "always"} {
			d := credentialPhaseKit(t, `[runtime, "${{ kit.`+source+`.PHASE }}"]`)
			if source == "args" {
				d.Args = map[string]Arg{"PHASE": {Default: &phase}}
			}
			_, err := Validate(d)
			require.NoError(t, err)
			var expanded *Descriptor
			if source == "args" {
				raw, marshalErr := json.Marshal(d)
				require.NoError(t, marshalErr)
				raw, err = ExpandCreateArgs(raw, d.Args, map[string]string{"PHASE": phase})
				require.NoError(t, err)
				expanded, err = Decode(raw)
			} else {
				expanded, err = ExpandEnvironment(d, map[string]string{"PHASE": phase})
			}
			require.NoError(t, err)
			raw, err := json.Marshal(expanded)
			require.NoError(t, err)
			_, err = ValidateEffective(raw, expanded)
			if phase == "install" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		}
	}
	for _, phase := range []string{`[always, "${{ kit.args.phase }}"]`, `["${{ kit.args.phase }}", "${{ kit.args.phase }}"]`, "[]", "null"} {
		d := credentialPhaseKit(t, phase)
		d.Args = map[string]Arg{"phase": {}}
		d.Capabilities[0].Config["apiKey"].(map[string]any)["name"] = "${{ kit.args.phase }}"
		_, err := Validate(d)
		require.Error(t, err, "invalid parameterized phase: "+phase)
	}
}
