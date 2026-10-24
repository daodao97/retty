package rex

import (
	"os"
	"path/filepath"
	"strings"
)

// A pane-local launcher observes the existing shared daemon through a private
// relay. The CLI still uses that daemon; no daemon environment is required.
// PATH injection works with login shells as well as direct Codex sessions.
func codexEnv(env []string) ([]string, string, error) {
	dir, err := os.MkdirTemp(Dir(), "codex-")
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(codexLauncher), 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, "", err
	}
	var clean []string
	path := ""
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = value
		} else {
			clean = append(clean, kv)
		}
	}
	return append(clean, "PATH="+dir+string(os.PathListSeparator)+path, "RETTY_CODEX_BIN="+dir, "RETTY_CODEX_BRIDGE=1"), dir, nil
}

const codexLauncher = `#!/bin/sh
# Remove this shim while resolving the user's actual Codex executable.
launcher_dir=${0%/*}
real_path=
separator=
remaining=$PATH
while :; do
  entry=${remaining%%:*}
  if [ "$entry" != "$launcher_dir" ]; then
    real_path=$real_path$separator$entry
    separator=:
  fi
  case "$remaining" in *:*) remaining=${remaining#*:} ;; *) break ;; esac
done
codex_exe=${RETTY_CODEX_EXE-}
if [ -z "$codex_exe" ]; then
  codex_exe=$(PATH=$real_path command -v codex) || exit 127
fi

# Preserve explicit server connections, daemon management and utility commands.
for arg do
  case "$arg" in
    --no-daemon|--remote|--remote=*|--help|-h|--version|-V|app-server|remote-control|agents|queue|exec|e|review|login|logout|mcp|plugin|app|completion|update|doctor|sandbox|debug|apply|archive|delete|migrate-rollouts|unarchive|cloud|exec-server|features|help)
      exec "$codex_exe" "$@" ;;
  esac
done
# Failure to attach notifications must not prevent ordinary CLI startup.
if [ -x "$RETTY_HOOK" ] &&
   "$RETTY_HOOK" -codex-bridge "$launcher_dir" "$$" "$codex_exe"; then
  endpoint=$(cat "$launcher_dir/endpoint")
  if [ -n "$endpoint" ]; then
    # Remote TUI mode otherwise defaults to the daemon's startup directory.
    use_cwd=1
    for arg do
      case "$arg" in -C|--cd|--cd=*|-C?*) use_cwd=0 ;; esac
    done
    if [ "$use_cwd" = 1 ]; then
      exec "$codex_exe" --remote "$endpoint" -C "$PWD" "$@"
    fi
    exec "$codex_exe" --remote "$endpoint" "$@"
  fi
fi
exec "$codex_exe" "$@"
`
