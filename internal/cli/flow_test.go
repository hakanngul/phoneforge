package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const flowOK = "version: 1\nname: t\nsteps:\n  - {id: a, type: act, do: tap}\n"

func TestFlowValidateFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mobflow")
	bad := filepath.Join(dir, "bad.mobflow")
	if err := os.WriteFile(good, []byte(flowOK), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(strings.Replace(flowOK, "do: tap", "do: 'tap ${x}'", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run(t, nil, "", "flow", "validate", good); code != 0 || !strings.Contains(out, "ok") {
		t.Errorf("good: %d %q", code, out)
	}
	code, _, errOut := run(t, nil, "", "flow", "validate", bad)
	if code != 1 || !strings.Contains(errOut, "undeclared parameter ${x}") {
		t.Errorf("bad: %d %q", code, errOut)
	}
	if code, _, _ := run(t, nil, "", "flow", "validate", filepath.Join(dir, "missing.mobflow")); code != 2 {
		t.Errorf("missing file code=%d", code)
	}
	if code, _, _ := run(t, nil, "", "flow", "validate"); code != 2 {
		t.Errorf("no args code=%d", code)
	}
}

func TestFlowListAndRemoteValidate(t *testing.T) {
	f := newFake(t)
	f.json("GET /flows/cases", `[{"id":"p/c","title":"Checkout","steps":2,"valid":true,"cache":{"present":true,"fresh":1,"stale":1,"missing":0}}]`)
	code, out, errOut := run(t, f, "", "flow", "list", "--project", "p")
	if code != 0 || !strings.Contains(out, "p/c") || !strings.Contains(out, "fresh 1, stale 1") {
		t.Fatalf("list: %d %q %q", code, out, errOut)
	}
	if r, _ := f.last("GET", "/flows/cases"); r.Query != "project=p" {
		t.Errorf("query=%q", r.Query)
	}
	if code, out, _ := run(t, f, "", "flow", "list", "--json"); code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("json: %d %q", code, out)
	}
	f.h["GET /flows/cases/p/bad"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"step":"a","field":"do","message":"is required"}]}`)
	}
	f.json("GET /flows/cases/p/good", `{"id":"p/good"}`)
	if code, _, errOut := run(t, f, "", "flow", "validate", "p/bad"); code != 1 || !strings.Contains(errOut, "step a: do: is required") {
		t.Errorf("remote bad: %d %q", code, errOut)
	}
	if code, _, _ := run(t, f, "", "flow", "validate", "p/good"); code != 0 {
		t.Errorf("remote good: %d", code)
	}
	if code, _, _ := run(t, nil, "", "flow", "list"); code == 0 {
		t.Error("unreachable server should fail")
	}
}
