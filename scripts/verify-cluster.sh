#!/usr/bin/env bash
# Verifies cluster-side controls of the running stand:
#   1. Pod Security Admission rejects pods that are not "restricted"
#   2. NetworkPolicies are enforced (the CNI actually implements them)
#   3. If the Istio policies are installed: audit's port 8081 requires mTLS
#      and accepts only the expenses identity
#
# Starts short-lived probe pods (restricted, non-root) with different labels and
# checks which connections succeed. Every "blocked" expectation has a positive
# control, so a blocked result means the policy works, not that the network is
# broken. Exit code 1 on any mismatch.
set -uo pipefail
CTX="${KUBE_CONTEXT:-docker-desktop}"
NS="${NS:-expense-platform-local}"
source "$(dirname "$0")/lib.sh"
guard_target "$CTX" "$NS"
IMAGE='busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec'

PROBE_NS="$NS-probe"  # temporary namespace without policies, like an ingress controller's

probe_pod() { # name labels [namespace]
  local ns="${3:-$NS}"
  kubectl --context "$CTX" -n "$ns" delete pod "$1" --ignore-not-found --wait=true >/dev/null
  kubectl --context "$CTX" -n "$ns" run "$1" --image="$IMAGE" --restart=Never --labels="$2" --overrides='{
    "spec": {
      "automountServiceAccountToken": false,
      "securityContext": {"runAsNonRoot": true, "runAsUser": 65534, "runAsGroup": 65534, "seccompProfile": {"type": "RuntimeDefault"}},
      "containers": [{
        "name": "probe", "image": "'"$IMAGE"'", "command": ["sleep", "300"],
        "resources": {"requests": {"cpu": "5m", "memory": "8Mi"}, "limits": {"cpu": "50m", "memory": "16Mi"}},
        "securityContext": {"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}}
      }]}}' >/dev/null
}

# A previous run deletes its probe namespace asynchronously; wait for it.
kubectl --context "$CTX" wait --for=delete "namespace/$PROBE_NS" --timeout=90s >/dev/null 2>&1 || true
kubectl --context "$CTX" create namespace "$PROBE_NS" --dry-run=client -o yaml \
  | kubectl --context "$CTX" label --local -f - pod-security.kubernetes.io/enforce=restricted -o yaml \
  | kubectl --context "$CTX" apply -f - >/dev/null
probes="probe-anon:app.kubernetes.io/name=netpol-probe probe-as-expenses:app.kubernetes.io/name=expenses probe-db-client:app.kubernetes.io/name=netpol-probe,expense-platform/db-client=true"
for p in $probes; do probe_pod "${p%%:*}" "${p#*:}"; done
probe_pod outsider app.kubernetes.io/name=netpol-probe "$PROBE_NS"
cleanup() {
  for p in $probes; do k delete pod "${p%%:*}" --ignore-not-found --wait=false >/dev/null; done
  kubectl --context "$CTX" delete namespace "$PROBE_NS" --wait=false >/dev/null 2>&1
}
trap cleanup EXIT
for p in $probes; do k wait --for=condition=Ready "pod/${p%%:*}" --timeout=60s >/dev/null; done
kubectl --context "$CTX" -n "$PROBE_NS" wait --for=condition=Ready pod/outsider --timeout=60s >/dev/null

fail=0
step "Pod Security Admission in $NS"
pss() { # description overrides-json
  local out
  out="$(k run pss-check --image="$IMAGE" --restart=Never --dry-run=server --overrides="$2" -- sleep 1 2>&1)"
  if grep -q 'violates PodSecurity "restricted' <<<"$out"; then
    printf '  %-4s rejected: %s\n' ok "$1"
  else
    printf '  %-4s ADMITTED: %s\n' FAIL "$1"; fail=1
  fi
}
pss "root container" '{"spec":{"containers":[{"name":"c","image":"'"$IMAGE"'","securityContext":{"runAsUser":0}}]}}'
pss "privileged container" '{"spec":{"containers":[{"name":"c","image":"'"$IMAGE"'","securityContext":{"privileged":true}}]}}'
pss "hostPath volume" '{"spec":{"volumes":[{"name":"h","hostPath":{"path":"/"}}],"containers":[{"name":"c","image":"'"$IMAGE"'","volumeMounts":[{"name":"h","mountPath":"/h"}]}]}}'

check() { # pod host port expect description [namespace]
  local got ns="${6:-$NS}"
  if kubectl --context "$CTX" -n "$ns" exec "$1" -- sh -c "echo | nc -w 3 $2 $3" >/dev/null 2>&1; then got=allowed; else got=blocked; fi
  local mark=ok; [[ "$got" == "$4" ]] || { mark=FAIL; fail=1; }
  printf '  %-4s %-18s -> %-22s %-8s (%s)\n' "$mark" "$1" "$2:$3" "$got" "$5"
}

svc() { echo "$1.$NS.svc.cluster.local"; }

step "Inside $NS: egress and ingress policies"
check probe-anon        postgres       5432 blocked "database only for labelled clients"
check probe-db-client   postgres       5432 allowed "labelled database client (control)"
check probe-anon        audit-internal 8081 blocked "event ingestion only from expenses"
check probe-as-expenses audit-internal 8081 allowed "expenses may deliver events (control)"
check probe-anon        expenses       8080 blocked "unlisted pods have no egress at all"
check probe-anon        1.1.1.1         443 blocked "no egress to the internet"
check probe-anon        169.254.169.254  80 blocked "no egress to a metadata service"
if k exec probe-anon -- nslookup "$(svc expenses)" >/dev/null 2>&1; then
  printf '  %-4s %-18s -> %-22s %-8s (%s)\n' ok probe-anon "kube-dns:53" allowed "DNS works (control)"
else
  printf '  %-4s %-18s -> %-22s %-8s (%s)\n' FAIL probe-anon "kube-dns:53" blocked "DNS works (control)"; fail=1
fi

step "From another namespace: ingress policies only (like the ingress controller)"
check outsider "$(svc expenses)"       8080 allowed "public API (control)" "$PROBE_NS"
check outsider "$(svc audit)"          8080 allowed "public history API (control)" "$PROBE_NS"
check outsider "$(svc postgres)"       5432 blocked "database not reachable from outside" "$PROBE_NS"
check outsider "$(svc audit-internal)" 8081 blocked "event ingestion not reachable from outside" "$PROBE_NS"
check outsider "$(svc expenses)"       9090 blocked "metrics not reachable from outside" "$PROBE_NS"
check outsider "$(svc audit)"          9090 blocked "metrics not reachable from outside" "$PROBE_NS"

# 3. Mesh: checked at the HTTP level. A TCP connect is not enough, because
# with STRICT mTLS the audit sidecar accepts the TCP connection and only
# rejects the TLS handshake.
if k get peerauthentication audit-internal-mtls >/dev/null 2>&1; then
  step "Istio mTLS and authorization on audit:8081"
  # Meshed probes carry the expenses name label, so NetworkPolicy lets them
  # through to 8081 and to istiod; only the mesh can reject them.
  mesh_pod() { # name serviceaccount
    k delete pod "$1" --ignore-not-found --wait=true >/dev/null
    k run "$1" --image="$IMAGE" --restart=Never \
      --labels="app.kubernetes.io/name=expenses,sidecar.istio.io/inject=true" --overrides='{
      "spec": {
        "serviceAccountName": "'"$2"'",
        "automountServiceAccountToken": false,
        "securityContext": {"runAsNonRoot": true, "runAsUser": 65534, "runAsGroup": 65534, "seccompProfile": {"type": "RuntimeDefault"}},
        "containers": [{
          "name": "probe", "image": "'"$IMAGE"'", "command": ["sleep", "300"],
          "resources": {"requests": {"cpu": "5m", "memory": "8Mi"}, "limits": {"cpu": "50m", "memory": "16Mi"}},
          "securityContext": {"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}}
        }]}}' >/dev/null
  }
  k create serviceaccount netpol-probe --dry-run=client -o yaml | k apply -f - >/dev/null
  mesh_pod mesh-as-expenses expenses
  mesh_pod mesh-as-other netpol-probe
  cleanup_mesh() { k delete pod mesh-as-expenses mesh-as-other --ignore-not-found --wait=false >/dev/null; k delete serviceaccount netpol-probe --ignore-not-found >/dev/null; }
  trap 'cleanup; cleanup_mesh' EXIT
  k wait --for=condition=Ready pod/mesh-as-expenses pod/mesh-as-other --timeout=120s >/dev/null

  # status <pod>: HTTP status of GET http://audit-internal:8081/internal/v1/events, or "reset"
  status() {
    local out
    out="$(k exec "$1" -c probe -- wget -S -q -O /dev/null -T 5 http://audit-internal:8081/internal/v1/events 2>&1 || true)"
    if grep -qE 'HTTP/1\.[01] [0-9]+' <<<"$out"; then grep -oE 'HTTP/1\.[01] [0-9]+' <<<"$out" | tail -1 | awk '{print $2}'; else echo reset; fi
  }
  mesh_check() { # pod expected-pattern description
    local got mark=ok
    got="$(status "$1")"
    [[ "$got" =~ ^($2)$ ]] || { mark=FAIL; fail=1; }
    printf '  %-4s %-18s -> %-22s %-8s (%s)\n' "$mark" "$1" "audit-internal:8081" "$got" "$3"
  }
  mesh_check probe-as-expenses reset   "no sidecar: plaintext refused by STRICT mTLS"
  mesh_check mesh-as-other     403     "mTLS, other ServiceAccount: denied by AuthorizationPolicy"
  # 405: the app itself answers (the route only accepts POST), so the mesh let it through.
  mesh_check mesh-as-expenses  405     "mTLS as expenses: reaches the app (control)"
fi

echo
[[ $fail == 0 ]] && echo "all cluster controls behave as expected" || echo "MISMATCH: see FAIL lines above"
exit $fail
