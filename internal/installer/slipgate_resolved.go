package installer

import (
	"strconv"
	"strings"
)

// SlipGate creates this public system configuration with os.WriteFile(0644),
// which inherits the caller's umask. Under 077 it becomes unreadable by
// systemd-resolve: the daemon discards its configuration and reopens :53.
// Repair only the exact known installer file before its native restart; keep
// the restrictive umask for keys and every other native installer output.
func slipGateResolvedSystemctlScript(script, dropin string) string {
	repair := `case "$action" in restart|try-restart|reload)
  for candidate in "$@"; do
    case "$candidate" in systemd-resolved|systemd-resolved.service)
      dropin=` + strconv.Quote(dropin) + `
      if [ -e "$dropin" ] || [ -L "$dropin" ]; then
        parent=$dropin
        while [ "$parent" != / ]; do
          if [ -L "$parent" ]; then
            echo 'CottenRouter: refusing symlinked SlipGate resolved configuration' >&2
            exit 1
          fi
          parent=$(dirname "$parent")
        done
        if [ ! -f "$dropin" ] || [ "$(stat -c %u "$dropin")" != "$(id -u)" ] ||
           ! printf '[Resolve]\nDNSStubListener=no\n' | cmp -s - "$dropin"; then
          echo 'CottenRouter: refusing unexpected SlipGate resolved configuration' >&2
          exit 1
        fi
        chmod 0644 "$dropin" || exit 1
      fi
      ;;
    esac
  done
  ;;
esac
`
	return strings.Replace(script, "\nexec ", "\n"+repair+"exec ", 1)
}
