//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/npavlov/expense-platform/libs/platform/devtoken"
)

// TestDemoScenario is the first demo: Alice submits a taxi claim, Bob cannot
// see it, Rita approves it, and the history shows exactly one decision.
func TestDemoScenario(t *testing.T) {
	id := mustSubmit(t, "alice", 1200, "Taxi")

	// Bob has a valid token and knows the ID.
	must(t, call(t, http.MethodGet, expensesURL, "/v1/claims/"+id, token(t, "bob"), nil), http.StatusNotFound, "bob reads alice's claim")
	bobs := call(t, http.MethodGet, expensesURL, "/v1/claims?limit=100", token(t, "bob"), nil)
	for _, c := range bobs.Body["items"].([]any) {
		if c.(map[string]any)["id"] == id {
			t.Fatal("alice's claim appears in bob's list")
		}
	}

	approved := decide(t, "rita", id, "approved")
	must(t, approved, http.StatusOK, "rita approves")
	if approved.str("status") != "approved" {
		t.Fatalf("status after approval: %v", approved.Body)
	}

	// Alice sees the outcome.
	if got := call(t, http.MethodGet, expensesURL, "/v1/claims/"+id, token(t, "alice"), nil); got.str("status") != "approved" {
		t.Fatalf("alice sees %v", got.Body)
	}

	entries := waitForHistory(t, id, 1, 20*time.Second)
	time.Sleep(2 * time.Second) // give a duplicate the chance to show up
	_, entries = history(t, id)
	if len(entries) != 1 || entries[0]["decision"] != "approved" || entries[0]["amount_cents"] != 1200.0 {
		t.Fatalf("history of %s = %v, want exactly one approval of 1200", describe(id), entries)
	}
}

func TestIdempotentRetryOverHTTP(t *testing.T) {
	key := uuid.NewString()
	body := map[string]any{"amount_cents": 4550, "description": "Train"}
	first := call(t, http.MethodPost, expensesURL, "/v1/claims", token(t, "alice"), body, "Idempotency-Key", key)
	must(t, first, http.StatusCreated, "first submit")
	retry := call(t, http.MethodPost, expensesURL, "/v1/claims", token(t, "alice"), body, "Idempotency-Key", key)
	must(t, retry, http.StatusCreated, "retry")
	if retry.str("id") != first.str("id") || retry.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry created a different claim: %v vs %v", retry.Body, first.Body)
	}
	changed := call(t, http.MethodPost, expensesURL, "/v1/claims", token(t, "alice"),
		map[string]any{"amount_cents": 99999, "description": "Train"}, "Idempotency-Key", key)
	must(t, changed, http.StatusUnprocessableEntity, "same key, different body")
}

func TestSelfApprovalIsForbidden(t *testing.T) {
	id := mustSubmit(t, "rita", 800, "Coffee with client")
	r := decide(t, "rita", id, "approved")
	must(t, r, http.StatusForbidden, "rita approves her own claim")
	if r.str("code") != "self_decision_forbidden" {
		t.Fatalf("code = %v", r.Body["code"])
	}
}

// TestConcurrentDecisions races two reviewers with opposite decisions.
func TestConcurrentDecisions(t *testing.T) {
	id := mustSubmit(t, "alice", 2500, "Hotel")
	token(t, "rita")
	token(t, "victor")

	type outcome struct {
		user, decision string
		status         int
		replayed       bool
	}
	results := make(chan outcome, 20)
	var wg sync.WaitGroup
	for i := range 20 {
		user := []string{"rita", "victor"}[i%2]
		decision := []string{"approved", "rejected"}[(i/2)%2]
		wg.Go(func() {
			r := decide(t, user, id, decision)
			results <- outcome{user, decision, r.Status, r.Header.Get("Idempotent-Replayed") == "true"}
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for o := range results {
		switch {
		case o.status == http.StatusOK && !o.replayed:
			winners++
		case o.status == http.StatusOK && o.replayed, o.status == http.StatusConflict:
		default:
			t.Errorf("unexpected outcome %+v", o)
		}
	}
	if winners != 1 {
		t.Fatalf("%d decisions won, want 1", winners)
	}
	waitForHistory(t, id, 1, 20*time.Second)
	time.Sleep(2 * time.Second)
	if _, entries := history(t, id); len(entries) != 1 {
		t.Fatalf("history has %d entries, want 1", len(entries))
	}
}

func TestForgedAndMissingTokensAreRejected(t *testing.T) {
	// A token with the right issuer and claims, signed by a key the
	// services do not trust.
	forger, err := devtoken.NewIssuer("http://devidp:8080")
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := forger.Mint(devtoken.Claims{Subject: "4174a000-0000-4000-8000-000000000003", Username: "rita",
		Groups: []string{"employees", "reviewers"}, ClientID: "expense-platform-local"})

	for name, tok := range map[string]string{"forged": forged, "missing": "", "garbage": "abc.def.ghi"} {
		t.Run(name, func(t *testing.T) {
			must(t, call(t, http.MethodGet, expensesURL, "/v1/claims", tok, nil), http.StatusUnauthorized, "expenses")
			must(t, call(t, http.MethodGet, auditURL, "/v1/audit/claims/"+uuid.NewString()+"/decisions", tok, nil), http.StatusUnauthorized, "audit")
		})
	}
}

func TestRoleChecksOnHistory(t *testing.T) {
	id := mustSubmit(t, "alice", 300, "Parking")
	must(t, call(t, http.MethodGet, auditURL, "/v1/audit/claims/"+id+"/decisions", token(t, "alice"), nil), http.StatusForbidden, "employee reads history")
	must(t, call(t, http.MethodGet, expensesURL, "/v1/review/claims", token(t, "bob"), nil), http.StatusForbidden, "employee reads review queue")
	must(t, decide(t, "bob", id, "approved"), http.StatusForbidden, "employee decides")
}

func TestInternalIngestNotOnPublicPort(t *testing.T) {
	r := call(t, http.MethodPost, auditURL, "/internal/v1/events", "", map[string]string{"event_id": uuid.NewString()})
	if r.Status != http.StatusNotFound && r.Status != http.StatusMethodNotAllowed {
		t.Fatalf("internal endpoint answered on the public port: %d", r.Status)
	}
}

// TestAuditOutageRecovery stops audit, decides a claim, starts audit again
// and waits for the history to catch up. Decisions must not wait for audit.
func TestAuditOutageRecovery(t *testing.T) {
	if os.Getenv("E2E_DISRUPTIVE") != "1" {
		t.Skip("set E2E_DISRUPTIVE=1 to stop and start the audit deployment")
	}
	ctx := env("E2E_KUBE_CONTEXT", env("KUBE_CONTEXT", "docker-desktop"))
	ns := env("E2E_NAMESPACE", "expense-platform-local")
	kubectl := func(args ...string) {
		t.Helper()
		cmd := exec.Command("kubectl", append([]string{"--context", ctx, "-n", ns}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
	}
	reviewer := token(t, "rita")
	id := mustSubmit(t, "alice", 1999, "Conference ticket")

	kubectl("scale", "deployment/audit", "--replicas=0")
	t.Cleanup(func() { kubectl("scale", "deployment/audit", "--replicas=1") })
	kubectl("wait", "--for=delete", "pod", "-l", "app.kubernetes.io/name=audit", "--timeout=60s")

	start := time.Now()
	r := decide(t, "rita", id, "approved")
	must(t, r, http.StatusOK, "approve while audit is down")
	if time.Since(start) > 3*time.Second {
		t.Fatalf("decision waited %v for the unavailable audit service", time.Since(start))
	}
	time.Sleep(5 * time.Second) // let the worker fail a few times
	if status, _ := tryHistory(reviewer, id); status == http.StatusOK {
		t.Fatal("history answered while audit was scaled to zero")
	}

	kubectl("scale", "deployment/audit", "--replicas=1")
	kubectl("rollout", "status", "deployment/audit", "--timeout=120s")
	entries := waitForHistory(t, id, 1, 60*time.Second)
	if len(entries) != 1 || entries[0]["decision"] != "approved" {
		t.Fatalf("history after recovery = %v", entries)
	}
	t.Logf("history caught up %v after the decision", time.Since(start).Round(time.Second))
}
