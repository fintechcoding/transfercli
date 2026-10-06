package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// withConfig points the package config at a temporary uploads directory and env file.
func withConfig(t *testing.T, mutate func(*Config)) string {
	t.Helper()
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	old := cfg
	cfg = Config{Listen: "127.0.0.1:0", UploadsDir: uploads, EnvFile: filepath.Join(dir, "transfercli.env"), Title: "Test"}
	if mutate != nil {
		mutate(&cfg)
	}
	t.Cleanup(func() { cfg = old; htcache = htpasswdCache{} })
	return dir
}

// addUpload creates an upload the way transfer.sh stores it: <uploads>/<token>/<name> + <name>.metadata.
func addUpload(t *testing.T, id, name, ctype string, meta map[string]interface{}) string {
	t.Helper()
	d := filepath.Join(cfg.UploadsDir, id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta == nil {
		meta = map[string]interface{}{"ContentType": ctype, "ContentLength": 5, "Downloads": 0, "MaxDownloads": -1, "MaxDate": "0001-01-01T00:00:00Z"}
	}
	b, _ := json.Marshal(meta)
	p := filepath.Join(d, name+".metadata")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFileURLPathEscapesUntrustedNames(t *testing.T) {
	got := fileURLPath("AbC123", `x" onerror="alert(1)<b>.txt`)
	if strings.ContainsAny(got[1:], `"<> `) {
		t.Fatalf("unescaped characters in %q", got)
	}
	u, err := url.Parse("https://files.example" + got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `/AbC123/x" onerror="alert(1)<b>.txt`; u.Path != want {
		t.Fatalf("round trip: got %q want %q", u.Path, want)
	}
}

func TestValidID(t *testing.T) {
	for _, ok := range []string{"AbC123", "x", "a_b-c"} {
		if !validID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../etc", "a/b", `a\b`, "a b", strings.Repeat("a", 65)} {
		if validID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestRenderConfigKeepsOtherLines(t *testing.T) {
	old := []byte("# header\nTC_PORT_UPLOAD=8081\nPURGE_DAYS=30\nCUSTOM=keep me\nPURGE_DAYS=99\n")
	got := string(renderConfig(old, GlobalConfig{PurgeDays: 7, PurgeInterval: 12}))
	want := "# header\nTC_PORT_UPLOAD=8081\nPURGE_DAYS=7\nCUSTOM=keep me\nPURGE_INTERVAL=12\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestValidateGlobalConfig(t *testing.T) {
	cases := []struct {
		g  GlobalConfig
		ok bool
	}{
		{GlobalConfig{0, 0}, true},
		{GlobalConfig{30, 24}, true},
		{GlobalConfig{30, 0}, false}, // purging enabled but never runs
		{GlobalConfig{-1, 24}, false},
		{GlobalConfig{maxPurgeDays + 1, 24}, false},
		{GlobalConfig{30, maxPurgeInterval + 1}, false},
	}
	for _, c := range cases {
		if err := validateGlobalConfig(c.g); (err == nil) != c.ok {
			t.Errorf("%+v: err=%v, want ok=%v", c.g, err, c.ok)
		}
	}
}

func req(method, target string, hdr map[string]string, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = "files.example"
	if body != "" {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestSameOrigin(t *testing.T) {
	withConfig(t, func(c *Config) { c.AllowedOrigins = []string{"https://admin.example"} })
	cases := []struct {
		name string
		hdr  map[string]string
		ok   bool
	}{
		{"same origin", map[string]string{"Origin": "https://files.example"}, true},
		{"allowed extra origin", map[string]string{"Origin": "https://admin.example"}, true},
		{"other site", map[string]string{"Origin": "https://evil.example"}, false},
		{"lookalike suffix", map[string]string{"Origin": "https://files.example.evil.example"}, false},
		{"null origin", map[string]string{"Origin": "null"}, false},
		{"no origin no referer", map[string]string{}, false},
		{"referer fallback", map[string]string{"Referer": "https://files.example/admin/"}, true},
		{"cross-site referer", map[string]string{"Referer": "https://evil.example/x"}, false},
		{"fetch metadata cross-site", map[string]string{"Origin": "https://files.example", "Sec-Fetch-Site": "cross-site"}, false},
		{"fetch metadata same-site", map[string]string{"Origin": "https://files.example", "Sec-Fetch-Site": "same-site"}, false},
	}
	for _, c := range cases {
		if got := sameOrigin(req("POST", "/admin/delete?id=x", c.hdr, "")); got != c.ok {
			t.Errorf("%s: got %v want %v", c.name, got, c.ok)
		}
	}
}

func TestCSRFDeleteIsRejected(t *testing.T) {
	withConfig(t, nil)
	addUpload(t, "Tok1", "a.txt", "text/plain", nil)
	h := routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/admin/delete?id=Tok1", map[string]string{"Origin": "https://evil.example"}, ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin delete: status %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.UploadsDir, "Tok1")); err != nil {
		t.Fatal("file was deleted by a cross-origin request")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/admin/delete?id=Tok1", map[string]string{"Origin": "https://files.example"}, ""))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("same-origin delete: status %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.UploadsDir, "Tok1")); !os.IsNotExist(err) {
		t.Fatal("file still exists after delete")
	}
}

func TestMethodAndPathChecks(t *testing.T) {
	withConfig(t, nil)
	h := routes()
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/admin/delete?id=x", http.StatusMethodNotAllowed},
		{"GET", "/admin/nope", http.StatusNotFound},
		{"GET", "/admin", http.StatusMovedPermanently},
		{"GET", "/admin/", http.StatusOK},
		{"GET", "/healthz", http.StatusOK},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req(c.method, c.path, nil, ""))
		if w.Code != c.code {
			t.Errorf("%s %s: got %d want %d", c.method, c.path, w.Code, c.code)
		}
	}
}

func TestAdminAuth(t *testing.T) {
	dir := withConfig(t, nil)
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	ht := filepath.Join(dir, "htpasswd")
	if err := os.WriteFile(ht, []byte("admin:"+string(hash)+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	cfg.Htpasswd = ht
	h := routes()
	try := func(user, pass string, set bool) int {
		r := req("GET", "/admin/", nil, "")
		if set {
			r.SetBasicAuth(user, pass)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := try("", "", false); c != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", c)
	}
	if c := try("admin", "wrong", true); c != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", c)
	}
	if c := try("nobody", "s3cret-pass", true); c != http.StatusUnauthorized {
		t.Errorf("unknown user: %d", c)
	}
	if c := try("admin", "s3cret-pass", true); c != http.StatusOK {
		t.Errorf("valid credentials: %d", c)
	}
	// healthz stays reachable for monitoring
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("GET", "/healthz", nil, ""))
	if w.Code != http.StatusOK {
		t.Errorf("healthz: %d", w.Code)
	}
}

func TestHtpasswdRejectsWeakHashes(t *testing.T) {
	dir := withConfig(t, nil)
	ht := filepath.Join(dir, "htpasswd")
	if err := os.WriteFile(ht, []byte("admin:$apr1$abc$def\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := htcache.load(ht); err == nil {
		t.Fatal("apr1 (MD5) hash was accepted")
	}
}

func TestAuthThrottleIsTemporary(t *testing.T) {
	authFails.mu.Lock()
	authFails.start, authFails.n = time.Now(), authFailBurst
	authFails.mu.Unlock()
	if !authThrottled() {
		t.Fatal("expected throttling after the burst")
	}
	authFails.mu.Lock()
	authFails.start = time.Now().Add(-2 * authFailWindow)
	authFails.mu.Unlock()
	if authThrottled() {
		t.Fatal("throttling must end after the window")
	}
}

func TestIndexEscapesUntrustedUploads(t *testing.T) {
	withConfig(t, nil)
	evilName := `x"><img src=x onerror=alert(1)>.txt`
	evilType := `text/html"><script>alert(2)</script>`
	addUpload(t, "Tok2", evilName, evilType, map[string]interface{}{
		"ContentType": evilType, "ContentLength": 5, "Downloads": 0, "MaxDownloads": -1, "MaxDate": "0001-01-01T00:00:00Z",
	})
	w := httptest.NewRecorder()
	routes().ServeHTTP(w, req("GET", "/admin/", nil, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, bad := range []string{`<img src=x`, `<script>alert(2)`, `onerror=alert(1)>`} {
		if strings.Contains(body, bad) {
			t.Errorf("rendered page contains raw %q", bad)
		}
	}
	if strings.Contains(body, "innerHTML") {
		t.Error("page still builds markup with innerHTML")
	}
	// The row delete button must not live in a nested <form> (browsers drop nested forms, so the
	// button used to submit the bulk-delete form instead of deleting its own row).
	if n := strings.Count(body, "<form"); n != 2 {
		t.Errorf("expected 2 forms (bulk + edit), found %d", n)
	}
	if !strings.Contains(body, `formaction="/admin/delete?id=Tok2"`) {
		t.Error("row delete button does not target its own file")
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if w.Header().Get(h) == "" {
			t.Errorf("missing %s header", h)
		}
	}
}

func TestExpiryValidationAndExactNumbers(t *testing.T) {
	withConfig(t, nil)
	big := int64(1) << 53 // beyond float64's exact integer range
	meta := addUpload(t, "Tok3", "big.iso", "application/octet-stream", map[string]interface{}{
		"ContentType": "application/octet-stream", "ContentLength": big + 1, "Downloads": 3, "MaxDownloads": -1, "MaxDate": "0001-01-01T00:00:00Z",
	})
	h := routes()
	post := func(form string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req("POST", "/admin/expiry?id=Tok3", map[string]string{"Origin": "https://files.example"}, form))
		return w.Code
	}
	for _, bad := range []string{"max_downloads=0", "max_downloads=-2", "max_days=-1", "max_days=abc", "max_days=99999"} {
		if c := post(bad); c != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, c)
		}
	}
	if c := post("max_downloads=5&max_days=2"); c != http.StatusSeeOther {
		t.Fatalf("valid update: status %d", c)
	}
	b, err := os.ReadFile(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"ContentLength":9007199254740993`)) {
		t.Errorf("ContentLength lost precision: %s", b)
	}
	if !bytes.Contains(b, []byte(`"MaxDownloads":5`)) {
		t.Errorf("MaxDownloads not updated: %s", b)
	}
}

func TestSettingsSaveKeepsUnrelatedKeys(t *testing.T) {
	withConfig(t, nil)
	if err := os.WriteFile(cfg.EnvFile, []byte("TC_PORT_UPLOAD=8081\nPURGE_DAYS=30\nPURGE_INTERVAL=24\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	h := routes()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/admin/settings", map[string]string{"Origin": "https://files.example"}, "purge_days=10&purge_interval=0"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("purge enabled with interval 0 must be rejected, got %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req("POST", "/admin/settings", map[string]string{"Origin": "https://files.example"}, "purge_days=10&purge_interval=6"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("save: status %d body %s", w.Code, w.Body.String())
	}
	b, _ := os.ReadFile(cfg.EnvFile)
	if string(b) != "TC_PORT_UPLOAD=8081\nPURGE_DAYS=10\nPURGE_INTERVAL=6\n" {
		t.Fatalf("env file: %q", b)
	}
}
