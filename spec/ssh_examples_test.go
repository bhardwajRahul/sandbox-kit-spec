package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSSHExampleStartupPreservesExistingConfiguration(t *testing.T) {
	for _, name := range []string{"git-signing", "github-ssh"} {
		for _, existing := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join("..", "examples", name, name+".yaml"))
				require.NoError(t, err)
				d, err := Decode(raw)
				require.NoError(t, err)
				lifecycle, err := LifecycleOf(d.Capabilities)
				require.NoError(t, err)
				home := t.TempDir()
				xdg := filepath.Join(home, "custom-config")
				config := filepath.Join(xdg, "git", "config")
				hosts := filepath.Join(home, ".ssh", "known_hosts")
				require.NoError(t, os.MkdirAll(filepath.Dir(config), 0755))
				require.NoError(t, os.MkdirAll(filepath.Dir(hosts), 0700))
				if existing {
					require.NoError(t, os.WriteFile(config, []byte("[alias]\n kept = status\n[commit]\n gpgsign = false\n"), 0600))
					// Deliberately no final newline: appending must not join two entries.
					require.NoError(t, os.WriteFile(hosts, []byte("other.example ssh-ed25519 existing-key"), 0600))
				}
				for _, file := range lifecycle.Files {
					path := strings.ReplaceAll(file.Path, "/home/agent", home)
					require.NoError(t, os.WriteFile(path, []byte(file.Content), 0600))
				}
				run := func(argv ...string) string {
					cmd := exec.Command(argv[0], argv[1:]...)
					cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + xdg, "GIT_CONFIG_NOSYSTEM=1"}
					cmd.Dir = home
					out, err := cmd.CombinedOutput()
					require.NoError(t, err, "%s", out)
					return strings.TrimSpace(string(out))
				}
				for boot := 0; boot < 2; boot++ {
					for _, hook := range lifecycle.Startup {
						argv := append([]string{}, hook.Command...)
						for i := range argv {
							argv[i] = strings.ReplaceAll(argv[i], "/home/agent", home)
						}
						run(argv...)
					}
					require.Equal(t, "ssh", run("git", "config", "--global", "--get", "gpg.format"))
					require.Equal(t, "ssh-add -L", run("git", "config", "--global", "--get", "gpg.ssh.defaultKeyCommand"))
					require.Equal(t, "true", run("git", "config", "--global", "--get", "tag.gpgsign"))
					want := "true"
					if existing {
						want = "false"
						require.Equal(t, "status", run("git", "config", "--global", "--get", "alias.kept"))
					}
					require.Equal(t, want, run("git", "config", "--global", "--get-all", "commit.gpgsign"))
				}
				if name == "github-ssh" {
					data, err := os.ReadFile(hosts)
					require.NoError(t, err)
					require.Equal(t, 3, strings.Count(string(data), "github.com "))
					if existing {
						require.Contains(t, string(data), "other.example ssh-ed25519 existing-key\n")
					}
				}
			})
		}
	}
}
