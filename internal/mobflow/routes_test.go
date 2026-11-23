package mobflow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRoutes(t *testing.T) {
	st, pid := newTestStore(t)
	h := (&Service{Store: st}).Routes()
	base := "/cases/" + pid + "/checkout"

	if w := doReq(t, h, "PUT", base, sample); w.Code != 201 {
		t.Fatalf("put yaml: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "PUT", base, sample); w.Code != 200 {
		t.Fatalf("re-put: %d", w.Code)
	}
	j, _ := json.Marshal(mustParse(t, sample))
	if w := doReq(t, h, "PUT", "/cases/"+pid+"/viajson", string(j)); w.Code != 201 {
		t.Fatalf("put json: %d %s", w.Code, w.Body)
	}
	w := doReq(t, h, "PUT", base, "version: 1\nname: x\nsteps: []\n")
	var ae apiError
	_ = json.Unmarshal(w.Body.Bytes(), &ae)
	if w.Code != 400 || ae.Code != "invalid_case" || len(ae.Errors) == 0 {
		t.Fatalf("invalid put: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases?project="+pid, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"steps":3`) {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases?project=nope", ""); w.Code != 404 {
		t.Fatalf("list unknown project: %d", w.Code)
	}
	if w := doReq(t, h, "GET", base, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Checkout happy path") {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", "/cases/"+pid+"/missing", ""); w.Code != 404 {
		t.Fatalf("get missing: %d", w.Code)
	}
	if w := doReq(t, h, "GET", "/cases/"+pid+"/a.b", ""); w.Code != 400 {
		t.Fatalf("bad name: %d", w.Code)
	}

	// validate
	w = doReq(t, h, "POST", "/cases/validate", sample)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("validate ok: %d %s", w.Code, w.Body)
	}
	w = doReq(t, h, "POST", "/cases/validate", strings.Replace(sample, "${item}", "${zzz}", 1))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "zzz") {
		t.Fatalf("validate bad: %d %s", w.Code, w.Body)
	}

	// cache
	w = doReq(t, h, "GET", base+"/cache", "")
	var cv struct {
		Cache     *Cache            `json:"cache"`
		Staleness map[string]string `json:"staleness"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &cv)
	if w.Code != 200 || cv.Cache != nil || cv.Staleness["s1"] != StateMissing {
		t.Fatalf("empty cache: %d %s", w.Code, w.Body)
	}
	body := `{"segments":[{"id":"s1","steps":[{"action":"tap","text":"x"}]},{"id":"s2","steps":[{"action":"tap","text":"y"}]}]}`
	w = doReq(t, h, "PUT", base+"/cache", body)
	_ = json.Unmarshal(w.Body.Bytes(), &cv)
	if w.Code != 200 || cv.Cache == nil || cv.Cache.Version != 1 || cv.Staleness["s2"] != StateFresh || cv.Staleness["s3"] != StateMissing {
		t.Fatalf("put cache: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "PUT", base+"/cache", `{"segments":[{"id":"zz","steps":[{}]}]}`); w.Code != 400 {
		t.Fatalf("bad segment: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "PUT", base+"/cache", `nope`); w.Code != 400 {
		t.Fatalf("bad body: %d", w.Code)
	}
	doReq(t, h, "PUT", base+"/cache", body)
	w = doReq(t, h, "GET", base+"/cache/history", "")
	var hist []HistoryEntry
	_ = json.Unmarshal(w.Body.Bytes(), &hist)
	if w.Code != 200 || len(hist) != 1 || hist[0].Version != 1 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	w = doReq(t, h, "POST", base+"/cache/history/"+hist[0].ID+"/restore", "")
	_ = json.Unmarshal(w.Body.Bytes(), &cv)
	if w.Code != 200 || cv.Cache.Version != 3 {
		t.Fatalf("restore: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", base+"/cache?platform=web", ""); w.Code != 400 {
		t.Fatalf("bad platform: %d", w.Code)
	}
	if w := doReq(t, h, "DELETE", base+"/cache", ""); w.Code != 204 {
		t.Fatalf("delete cache: %d", w.Code)
	}
	if w := doReq(t, h, "DELETE", base+"/cache", ""); w.Code != 404 {
		t.Fatalf("delete cache again: %d", w.Code)
	}
	if w := doReq(t, h, "DELETE", base, ""); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := doReq(t, h, "GET", base+"/cache", ""); w.Code != 404 {
		t.Fatalf("cache of deleted case: %d", w.Code)
	}
}
