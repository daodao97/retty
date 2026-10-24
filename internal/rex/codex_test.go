//go:build darwin || linux

package rex

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexLauncherPreservesPaneAndExplicitConnections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modern bool
		args   []string
		want   string
		shared bool
	}{
		{"new task", true, []string{"-C", "/path with spaces", "describe project"}, "--remote\nunix:///tmp/bridge.sock\n-C\n/path with spaces\ndescribe project\n", true},
		{"resume", true, []string{"resume", "thread"}, "--remote\nunix:///tmp/bridge.sock\nresume\nthread\n", true},
		{"explicit isolated", true, []string{"--no-daemon"}, "--no-daemon\n", true},
		{"remote", true, []string{"--remote", "unix:///tmp/server"}, "--remote\nunix:///tmp/server\n", true},
		{"daemon management", true, []string{"app-server", "daemon", "start"}, "app-server\ndaemon\nstart\n", true},
		{"exec", true, []string{"exec", "hello"}, "exec\nhello\n", true},
		{"old cli", false, []string{"resume", "thread"}, "resume\nthread\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RETTY_DIR", t.TempDir())
			bin := filepath.Join(t.TempDir(), "space bin")
			os.Mkdir(bin, 0o700)
			helper := filepath.Join(bin, "helper")
			bootstrap := "#!/bin/sh\nexit 1\n"
			if tc.modern {
				bootstrap = fakeCodexBridge
			}
			if err := os.WriteFile(helper, []byte(bootstrap), 0o700); err != nil {
				t.Fatal(err)
			}
			fake := "#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'pane:%s token:%s shared:%s\\n' \"$RETTY_SESSION\" \"$RETTY_AGENT_TOKEN\" \"${RETTY_AGENT_SERVER_TOKEN-unset}\"\n"
			os.WriteFile(filepath.Join(bin, "codex"), []byte(fake), 0o700)
			env, dir, err := codexEnv([]string{"PATH=" + bin + ":/usr/bin:/bin", "RETTY_SESSION=pane", "RETTY_AGENT_TOKEN=private", "RETTY_AGENT_SERVER_TOKEN=shared", "RETTY_HOOK=" + helper})
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			cmd := exec.Command(filepath.Join(dir, "codex"), tc.args...)
			cmd.Env, cmd.Dir = env, bin
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err)
			}
			shared := "unset"
			if tc.shared {
				shared = "shared"
			}
			want := tc.want + "pane:pane token:private shared:" + shared + "\n"
			if tc.name == "resume" {
				cwd, err := filepath.EvalSymlinks(bin)
				if err != nil {
					t.Fatal(err)
				}
				want = strings.Replace(want, "unix:///tmp/bridge.sock\n", "unix:///tmp/bridge.sock\n-C\n"+cwd+"\n", 1)
			}
			if string(out) != want {
				t.Fatalf("got %q, want %q", out, want)
			}
		})
	}
}

func TestCodexLauncherSurvivesLoginPathReset(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	t.Setenv("RETTY_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	config := t.TempDir()
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'bridge:%s:%s:%s\\n' \"$1\" \"$2\" \"${RETTY_AGENT_SERVER_TOKEN-unset}\"\n"), 0o700)
	helper := filepath.Join(bin, "helper")
	os.WriteFile(helper, []byte(fakeCodexBridge), 0o700)
	os.WriteFile(filepath.Join(config, ".zshrc"), []byte("export PATH='"+bin+":/usr/bin:/bin'\nexport RETTY_HOOK='"+helper+"'\nPROMPT='ready> '\n"), 0o600)
	s, err := newSessionForServer("launcher", CreateOptions{Command: []string{zsh, "-i"}, Dir: config, Env: []string{"ZDOTDIR=" + config}}, "shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.kill()
	wait := func(needle string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(s.vt.Text(), needle) {
			if time.Now().After(deadline) {
				t.Fatalf("missing %q: %q", needle, s.vt.Text())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("ready>")
	s.input([]byte("codex\r"))
	wait("bridge:--remote:unix:///tmp/bridge.sock:shared")
}

const fakeCodexBridge = `#!/bin/sh
[ "$1" = -codex-bridge ] || exit 1
printf 'unix:///tmp/bridge.sock' > "$2/endpoint"
`
