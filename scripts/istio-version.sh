# Pinned Istio release for the local stand and CI. Source, do not execute.
# Update all four values together (hashes from the release's .sha256 files,
# checked against a second download).
ISTIO_VERSION=1.32.0
ISTIOCTL_SHA256_LINUX_AMD64=4771175dec92527cd90ddc7edb3b3abcb32ca2f3bc32b31aa31e0b12ad36f897
ISTIOCTL_SHA256_LINUX_ARM64=6ddfad272f4faa34ef5177e568b7473de2b17ac0464668aea93bf8a450fbbc8a
ISTIOCTL_SHA256_OSX_ARM64=7fae14feec588df5d57638c3a1477e92ee65b98ddc5b49da664a1bc776aa5dc3

# istioctl to use: .local/bin (make istioctl) first, then PATH. The version
# must match ISTIO_VERSION so the installed control plane is reproducible.
resolve_istioctl() {
  local root="$1" cand v
  for cand in "$root/.local/bin/istioctl" "$(command -v istioctl || true)"; do
    [[ -x "$cand" ]] || continue
    v="$("$cand" version --remote=false 2>/dev/null | awk '{print $NF}')"
    if [[ "$v" == "$ISTIO_VERSION" ]]; then echo "$cand"; return 0; fi
    echo "ignoring $cand: version $v, need $ISTIO_VERSION" >&2
  done
  echo "istioctl $ISTIO_VERSION not found; run: make istioctl" >&2
  return 1
}
