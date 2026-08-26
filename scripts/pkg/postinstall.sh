#!/bin/sh
# Runs after the package installs /usr/bin/op-forward on any distribution.
set -e

echo "op-forward installed to /usr/bin/op-forward."
echo "Run 'op-forward install' as each user to set up the op shim."

# Releases up to 0.7.1 installed the binary to /usr/local/bin and the shim
# recorded that absolute path. Warn when such a shim is still present so the
# user knows why op would fall through to the real binary after upgrading.
for shim in /home/*/.local/bin/op /root/.local/bin/op; do
    [ -f "$shim" ] || continue
    if grep -q '^OP_FORWARD_BIN="/usr/local/bin/op-forward"' "$shim" 2>/dev/null; then
        echo "Note: $shim references /usr/local/bin/op-forward from a previous release;"
        echo "      run 'op-forward install' as that user to refresh it."
    fi
done
exit 0
