#!/bin/sh
# Runs after the package installs /usr/bin/op-forward on any distribution.
set -e

echo "op-forward installed to /usr/bin/op-forward."
echo "Run 'op-forward install' as each user to set up the op shim."

# Releases up to 0.7.1 installed the binary to /usr/local/bin and the shim
# written by `op-forward install` recorded that absolute path. After this
# package removes the old binary such a shim fails every op command until
# the user re-runs `op-forward install`, and other users on the machine
# never see this package's output. The shim is a file this project
# generated, identified by its header line, so repairing the recorded path
# in place is safe: only that one line changes and ownership is preserved.
OLD_BIN='/usr/local/bin/op-forward'
NEW_BIN='/usr/bin/op-forward'
for shim in /home/*/.local/bin/op /root/.local/bin/op; do
    [ -f "$shim" ] || continue
    head -n 2 "$shim" 2>/dev/null | grep -q '^# op-forward shim' || continue
    grep -q "^OP_FORWARD_BIN=\"$OLD_BIN\"" "$shim" 2>/dev/null || continue
    owner=$(stat -c '%u:%g' "$shim" 2>/dev/null || echo "")
    mode=$(stat -c '%a' "$shim" 2>/dev/null || echo 755)
    tmp="$shim.tmp.$$"
    sed "s|^OP_FORWARD_BIN=\"$OLD_BIN\"|OP_FORWARD_BIN=\"$NEW_BIN\"|" "$shim" > "$tmp"
    [ -n "$owner" ] && chown "$owner" "$tmp"
    chmod "$mode" "$tmp"
    mv "$tmp" "$shim"
    echo "Updated $shim to use $NEW_BIN (previous release installed to $OLD_BIN)."
done
exit 0
