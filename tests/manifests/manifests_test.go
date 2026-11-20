// Package manifests checks the rendered Kubernetes overlays against the
// project's security baseline. It needs kubectl (for its built-in kustomize)
// but no cluster, so it runs in CI on every pull request.
package manifests

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type obj map[string]any

func (o obj) get(path ...string) any {
	var cur any = map[string]any(o)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func (o obj) str(path ...string) string { s, _ := o.get(path...).(string); return s }
func (o obj) kind() string              { return o.str("kind") }
func (o obj) name() string              { return o.str("metadata", "name") }
func (o obj) id() string                { return o.kind() + "/" + o.name() }

func render(t *testing.T, overlay string) []obj {
	t.Helper()
	out, err := exec.Command("kubectl", "kustomize", "../../deploy/k8s/overlays/"+overlay).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("kustomize %s: %v\n%s", overlay, err, ee.Stderr)
		}
		t.Fatalf("kustomize %s: %v (is kubectl installed?)", overlay, err)
	}
	var objs []obj
	dec := yaml.NewDecoder(bytes.NewReader(out))
	for {
		var m map[string]any
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if m != nil {
			objs = append(objs, m)
		}
	}
	return objs
}

func podSpec(o obj) obj {
	switch o.kind() {
	case "Deployment", "StatefulSet", "Job":
		if m, ok := o.get("spec", "template", "spec").(map[string]any); ok {
			return m
		}
	}
	return nil
}

func list(v any) []obj {
	var out []obj
	items, _ := v.([]any)
	for _, i := range items {
		if m, ok := i.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

var overlays = []string{"local", "local-observability", "aws"}

func TestWorkloadBaseline(t *testing.T) {
	for _, overlay := range overlays {
		for _, o := range render(t, overlay) {
			spec := podSpec(o)
			if spec == nil {
				continue
			}
			t.Run(overlay+"/"+o.id(), func(t *testing.T) {
				if spec.get("automountServiceAccountToken") != false {
					t.Error("service account token is mounted")
				}
				if spec.get("hostNetwork") == true || spec.get("hostPID") == true || spec.get("hostIPC") == true {
					t.Error("uses host namespaces")
				}
				if spec.get("securityContext", "runAsNonRoot") != true {
					t.Error("pod does not require runAsNonRoot")
				}
				if spec.str("securityContext", "seccompProfile", "type") != "RuntimeDefault" {
					t.Error("seccomp profile is not RuntimeDefault")
				}
				for _, v := range list(spec.get("volumes")) {
					if v.get("hostPath") != nil {
						t.Errorf("hostPath volume %s", v.str("name"))
					}
				}
				for _, c := range list(spec.get("containers")) {
					name := c.str("name")
					sc := obj(c)
					if sc.get("securityContext", "allowPrivilegeEscalation") != false {
						t.Errorf("%s: allowPrivilegeEscalation not false", name)
					}
					if sc.get("securityContext", "privileged") == true {
						t.Errorf("%s: privileged", name)
					}
					if sc.get("securityContext", "readOnlyRootFilesystem") != true {
						t.Errorf("%s: root filesystem is writable", name)
					}
					drop, _ := sc.get("securityContext", "capabilities", "drop").([]any)
					if !slices.Contains(drop, any("ALL")) {
						t.Errorf("%s: capabilities not dropped", name)
					}
					for _, r := range []string{"cpu", "memory"} {
						if sc.get("resources", "limits", r) == nil || sc.get("resources", "requests", r) == nil {
							t.Errorf("%s: missing %s request/limit", name, r)
						}
					}
					img := c.str("image")
					if img == "" || strings.HasSuffix(img, ":latest") || (!strings.Contains(img, ":") && !strings.Contains(img, "@")) {
						t.Errorf("%s: image %q is not pinned to a tag or digest", name, img)
					}
					if o.kind() != "Job" && (sc.get("readinessProbe") == nil || sc.get("livenessProbe") == nil) {
						t.Errorf("%s: missing readiness or liveness probe", name)
					}
				}
			})
		}
	}
}

func TestClusterBoundaries(t *testing.T) {
	for _, overlay := range overlays {
		t.Run(overlay, func(t *testing.T) {
			objs := render(t, overlay)
			var ns, denyAll bool
			for _, o := range objs {
				switch o.kind() {
				case "Namespace":
					ns = true
					if o.str("metadata", "labels", "pod-security.kubernetes.io/enforce") != "restricted" {
						t.Errorf("%s: Pod Security is not enforced as restricted", o.id())
					}
				case "Secret":
					t.Errorf("%s: secrets must be generated outside Git (scripts/secrets.sh)", o.id())
				case "Service":
					if typ := o.str("spec", "type"); typ == "NodePort" || typ == "LoadBalancer" {
						t.Errorf("%s: type %s exposes the service outside the cluster", o.id(), typ)
					}
				case "NetworkPolicy":
					types, _ := o.get("spec", "policyTypes").([]any)
					sel, _ := o.get("spec", "podSelector").(map[string]any)
					if len(sel) == 0 && o.get("spec", "ingress") == nil && o.get("spec", "egress") == nil &&
						slices.Contains(types, any("Ingress")) && slices.Contains(types, any("Egress")) {
						denyAll = true
					}
				case "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding":
					t.Errorf("%s: workloads do not need Kubernetes API access", o.id())
				}
			}
			if !ns {
				t.Error("overlay does not define its namespace")
			}
			if !denyAll {
				t.Error("no default-deny NetworkPolicy")
			}
		})
	}
}

// TestAWSOverlayHasNoLocalShortcuts makes sure the local development
// conveniences cannot reach the cloud deployment.
func TestAWSOverlayHasNoLocalShortcuts(t *testing.T) {
	objs := render(t, "aws")
	for _, o := range objs {
		if strings.Contains(o.name(), "devidp") {
			t.Errorf("%s: the development identity provider is in the AWS overlay", o.id())
		}
		if o.kind() == "ConfigMap" {
			data, _ := o.get("data").(map[string]any)
			if v, ok := data["AUTH_ALLOW_INSECURE_ISSUER"]; ok && v != "false" {
				t.Errorf("%s: AUTH_ALLOW_INSECURE_ISSUER=%v", o.id(), v)
			}
			if iss, ok := data["AUTH_ISSUER"].(string); ok && !strings.HasPrefix(iss, "https://cognito-idp.") {
				t.Errorf("%s: issuer %q is not Cognito over https", o.id(), iss)
			}
		}
		if spec := podSpec(o); spec != nil {
			for _, c := range list(spec.get("containers")) {
				for _, e := range list(c.get("env")) {
					if e.str("name") == "AUTH_ALLOW_INSECURE_ISSUER" {
						t.Errorf("%s: sets AUTH_ALLOW_INSECURE_ISSUER", o.id())
					}
				}
				if img := c.str("image"); strings.HasPrefix(img, "expense-platform/") {
					t.Errorf("%s: local image %q in AWS overlay", o.id(), img)
				}
			}
		}
	}
}

// TestAWSIngressExposesOnlyPublicAPIs checks every ingress backend.
func TestAWSIngressExposesOnlyPublicAPIs(t *testing.T) {
	allowed := map[string]bool{"expenses/http": true, "audit/http": true}
	found := 0
	for _, o := range render(t, "aws") {
		if o.kind() != "Ingress" {
			continue
		}
		if o.str("metadata", "annotations", "traefik.ingress.kubernetes.io/router.entrypoints") != "websecure" {
			t.Errorf("%s: not restricted to the TLS entrypoint", o.id())
		}
		for _, rule := range list(o.get("spec", "rules")) {
			for _, p := range list(rule.get("http", "paths")) {
				found++
				b := p.str("backend", "service", "name") + "/" + p.str("backend", "service", "port", "name")
				if !allowed[b] {
					t.Errorf("%s: path %s routes to %s", o.id(), p.str("path"), b)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no ingress paths found")
	}
}
