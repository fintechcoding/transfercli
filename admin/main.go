package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var cfg = loadConfig()

type Config struct {
	Listen     string
	UploadsDir string
	EnvFile    string
	BaseURL    string
	Title      string
	// Htpasswd is a bcrypt htpasswd file (htpasswd -B). When set, every /admin request must carry valid
	// HTTP Basic credentials, checked by this process. When empty, the admin panel has NO authentication
	// of its own and must only be reachable through a reverse proxy that authenticates.
	Htpasswd string
	// AllowedOrigins are extra origins (scheme://host[:port]) accepted for state-changing requests,
	// in addition to the origin of the request's own Host.
	AllowedOrigins []string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	c := Config{
		Listen:     getenv("TC_LISTEN", "127.0.0.1:8082"),
		UploadsDir: getenv("TC_UPLOADS_DIR", "/var/lib/transfercli/uploads"),
		EnvFile:    getenv("TC_ENV_FILE", "/etc/transfercli.env"),
		BaseURL:    strings.TrimRight(getenv("TC_BASE_URL", ""), "/"),
		Title:      getenv("TC_TITLE", "TransferCLI"),
		Htpasswd:   getenv("TC_ADMIN_HTPASSWD", ""),
	}
	for _, o := range strings.Split(getenv("TC_ADMIN_ALLOWED_ORIGINS", ""), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	return c
}

type Metadata struct {
	ContentType          string    `json:"ContentType"`
	ContentLength        int64     `json:"ContentLength"`
	Downloads            int       `json:"Downloads"`
	MaxDownloads         int       `json:"MaxDownloads"`
	MaxDate              time.Time `json:"MaxDate"`
	DeletionToken        string    `json:"DeletionToken"`
	Encrypted            bool      `json:"Encrypted"`
	DecryptedContentType string    `json:"DecryptedContentType"`
}

type FileEntry struct {
	ID           string
	Filename     string
	Size         int64
	SizeHuman    string
	RelPath      string
	ContentType  string
	UploadedAt   time.Time
	UploadedAgo  string
	Downloads    int
	MaxDownloads int
	MaxDate      time.Time
	ExpiresIn    string
	Encrypted    bool
}

type GlobalConfig struct {
	PurgeDays     int
	PurgeInterval int
}

// Limits for values accepted from the admin forms.
const (
	maxPurgeDays     = 3650
	maxPurgeInterval = 8760 // hours
	maxExpiryDays    = 3650
	maxDownloadLimit = 1000000
	maxFormBytes     = 1 << 20
)

type DayBar struct {
	Label  string
	Count  int
	Height int
}

type TypeSlice struct {
	Name    string
	Count   int
	Size    int64
	Percent float64
	Color   string
	Offset  float64
}

type Stats struct {
	TotalFiles      int
	TotalSize       int64
	TotalSizeHuman  string
	TotalDownloads  int
	DiskTotal       int64
	DiskUsed        int64
	DiskFree        int64
	DiskUsedPercent int
	DiskUsedHuman   string
	DiskTotalHuman  string
	DiskFreeHuman   string
	DailyUploads    []DayBar
	TypeBreakdown   []TypeSlice
	TopDownloads    []FileEntry
	TopSize         []FileEntry
}

func diskStats() (total, free int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(cfg.UploadsDir, &st); err != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}

func computeStats(files []FileEntry) Stats {
	var st Stats
	st.TotalFiles = len(files)
	typeCount := map[string]int{}
	typeSize := map[string]int64{}
	for i := range files {
		st.TotalSize += files[i].Size
		st.TotalDownloads += files[i].Downloads
		cat := categorize(files[i].ContentType, files[i].Filename)
		typeCount[cat]++
		typeSize[cat] += files[i].Size
	}
	st.TotalSizeHuman = humanSize(st.TotalSize)
	st.DiskTotal, st.DiskFree = diskStats()
	st.DiskUsed = st.DiskTotal - st.DiskFree
	if st.DiskTotal > 0 {
		st.DiskUsedPercent = int(float64(st.DiskUsed) * 100 / float64(st.DiskTotal))
	}
	st.DiskTotalHuman = humanSize(st.DiskTotal)
	st.DiskUsedHuman = humanSize(st.DiskUsed)
	st.DiskFreeHuman = humanSize(st.DiskFree)

	// daily uploads last 7 days
	now := time.Now()
	days := make([]DayBar, 7)
	maxCount := 0
	for i := 0; i < 7; i++ {
		d := now.AddDate(0, 0, -6+i)
		days[i] = DayBar{Label: d.Format("Mon"), Count: 0}
	}
	for _, f := range files {
		diff := int(now.Sub(f.UploadedAt).Hours() / 24)
		if diff < 0 || diff > 6 {
			continue
		}
		idx := 6 - diff
		days[idx].Count++
		if days[idx].Count > maxCount {
			maxCount = days[idx].Count
		}
	}
	for i := range days {
		if maxCount > 0 {
			days[i].Height = days[i].Count * 100 / maxCount
		}
	}
	st.DailyUploads = days

	// type breakdown (by count, convert to slices)
	colors := []string{"#3182ce", "#dd6b20", "#38a169", "#e53e3e", "#805ad5", "#d69e2e", "#319795", "#9f7aea"}
	type kv struct {
		k string
		c int
		s int64
	}
	var kvs []kv
	for k, c := range typeCount {
		kvs = append(kvs, kv{k, c, typeSize[k]})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].c > kvs[j].c })
	total := st.TotalFiles
	if total == 0 {
		total = 1
	}
	off := 0.0
	for i, kv := range kvs {
		if i >= 8 {
			break
		}
		pct := float64(kv.c) * 100 / float64(total)
		st.TypeBreakdown = append(st.TypeBreakdown, TypeSlice{
			Name: kv.k, Count: kv.c, Size: kv.s, Percent: pct,
			Color: colors[i%len(colors)], Offset: off,
		})
		off += pct
	}

	// Top downloads (top 5)
	sortedByDL := make([]FileEntry, len(files))
	copy(sortedByDL, files)
	sort.Slice(sortedByDL, func(i, j int) bool { return sortedByDL[i].Downloads > sortedByDL[j].Downloads })
	n := 5
	if len(sortedByDL) < n {
		n = len(sortedByDL)
	}
	st.TopDownloads = sortedByDL[:n]

	// Top size (top 10)
	sortedBySize := make([]FileEntry, len(files))
	copy(sortedBySize, files)
	sort.Slice(sortedBySize, func(i, j int) bool { return sortedBySize[i].Size > sortedBySize[j].Size })
	n = 10
	if len(sortedBySize) < n {
		n = len(sortedBySize)
	}
	st.TopSize = sortedBySize[:n]

	return st
}

func categorize(ctype, name string) string {
	ct := strings.ToLower(ctype)
	switch {
	case strings.HasPrefix(ct, "image/"):
		return "Image"
	case strings.HasPrefix(ct, "video/"):
		return "Video"
	case strings.HasPrefix(ct, "audio/"):
		return "Audio"
	case strings.HasPrefix(ct, "text/"):
		return "Text"
	case strings.Contains(ct, "pdf"):
		return "PDF"
	case strings.Contains(ct, "zip") || strings.Contains(ct, "tar") || strings.Contains(ct, "gzip") || strings.Contains(ct, "compressed") || strings.Contains(ct, "7z") || strings.Contains(ct, "rar"):
		return "Archive"
	case strings.Contains(ct, "iso") || strings.HasSuffix(strings.ToLower(name), ".iso") || strings.HasSuffix(strings.ToLower(name), ".img"):
		return "ISO/Image"
	case strings.Contains(ct, "json") || strings.Contains(ct, "xml") || strings.Contains(ct, "yaml"):
		return "Data"
	case strings.Contains(ct, "executable") || strings.Contains(ct, "msdownload") || strings.HasSuffix(strings.ToLower(name), ".exe") || strings.HasSuffix(strings.ToLower(name), ".deb") || strings.HasSuffix(strings.ToLower(name), ".rpm"):
		return "Binary"
	default:
		return "Other"
	}
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func humanDur(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func isZeroTime(t time.Time) bool {
	return t.IsZero() || t.Equal(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC))
}

func expiryStr(m Metadata, uploadedAt time.Time, gcPurgeDays int) string {
	if !isZeroTime(m.MaxDate) {
		d := time.Until(m.MaxDate)
		if d <= 0 {
			return "expired"
		}
		return humanDur(d) + " left"
	}
	if gcPurgeDays > 0 {
		exp := uploadedAt.Add(time.Duration(gcPurgeDays) * 24 * time.Hour)
		d := time.Until(exp)
		if d <= 0 {
			return "expired (global)"
		}
		return humanDur(d) + " left (global)"
	}
	if m.MaxDownloads > 0 {
		rem := m.MaxDownloads - m.Downloads
		if rem <= 0 {
			return "consumed"
		}
		return fmt.Sprintf("%d dl left", rem)
	}
	return "never"
}

// envKey returns the variable name of an env-file line, or "" for comments and blank lines.
func envKey(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || !strings.Contains(t, "=") {
		return ""
	}
	k, _, _ := strings.Cut(t, "=")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "export "))
}

func readConfig() GlobalConfig {
	gcfg := GlobalConfig{}
	f, err := os.Open(cfg.EnvFile)
	if err != nil {
		return gcfg
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Text()
		k := envKey(line)
		if k == "" {
			continue
		}
		_, v, _ := strings.Cut(line, "=")
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		n, _ := strconv.Atoi(v)
		switch k {
		case "PURGE_DAYS":
			gcfg.PurgeDays = n
		case "PURGE_INTERVAL":
			gcfg.PurgeInterval = n
		}
	}
	return gcfg
}

func validateGlobalConfig(g GlobalConfig) error {
	if g.PurgeDays < 0 || g.PurgeDays > maxPurgeDays {
		return fmt.Errorf("purge days must be between 0 and %d", maxPurgeDays)
	}
	if g.PurgeDays > 0 && (g.PurgeInterval < 1 || g.PurgeInterval > maxPurgeInterval) {
		return fmt.Errorf("purge interval must be between 1 and %d hours when purging is enabled", maxPurgeInterval)
	}
	if g.PurgeInterval < 0 || g.PurgeInterval > maxPurgeInterval {
		return fmt.Errorf("purge interval must be between 0 and %d hours", maxPurgeInterval)
	}
	return nil
}

// renderConfig rewrites PURGE_DAYS and PURGE_INTERVAL in an env file and keeps every other line
// (comments, TC_PORT_UPLOAD, operator-added settings) exactly as it was.
func renderConfig(old []byte, g GlobalConfig) []byte {
	want := map[string]string{
		"PURGE_DAYS":     strconv.Itoa(g.PurgeDays),
		"PURGE_INTERVAL": strconv.Itoa(g.PurgeInterval),
	}
	var out bytes.Buffer
	done := map[string]bool{}
	s := bufio.NewScanner(bytes.NewReader(old))
	for s.Scan() {
		line := s.Text()
		k := envKey(line)
		if v, ok := want[k]; ok {
			if !done[k] {
				fmt.Fprintf(&out, "%s=%s\n", k, v)
				done[k] = true
			}
			continue // drop duplicates of managed keys
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	for _, k := range []string{"PURGE_DAYS", "PURGE_INTERVAL"} {
		if !done[k] {
			fmt.Fprintf(&out, "%s=%s\n", k, want[k])
		}
	}
	return out.Bytes()
}

// writeConfig updates the env file in place (the directory is not writable for this service, so
// a rename is not possible). transfer.sh is restarted by transfercli-config.path, which watches the file.
func writeConfig(g GlobalConfig) error {
	old, err := os.ReadFile(cfg.EnvFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(cfg.EnvFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(renderConfig(old, g)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// fileURLPath builds the public path of an upload. Both parts come from the file system, which
// anonymous uploaders control, so they are always percent-encoded.
func fileURLPath(id, name string) string {
	return "/" + url.PathEscape(id) + "/" + url.PathEscape(name)
}

func listFiles() ([]FileEntry, error) {
	entries, err := os.ReadDir(cfg.UploadsDir)
	if err != nil {
		return nil, err
	}
	gc := readConfig()
	var files []FileEntry
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		id := e.Name()
		subDir := filepath.Join(cfg.UploadsDir, id)
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() || strings.HasSuffix(se.Name(), ".metadata") {
				continue
			}
			name := se.Name()
			metaPath := filepath.Join(subDir, name+".metadata")
			metaBytes, err := os.ReadFile(metaPath)
			if err != nil {
				continue
			}
			var m Metadata
			if err := json.Unmarshal(metaBytes, &m); err != nil {
				continue
			}
			st, err := os.Stat(filepath.Join(subDir, name))
			if err != nil {
				continue
			}
			mtime := st.ModTime()
			fe := FileEntry{
				ID:           id,
				Filename:     name,
				Size:         m.ContentLength,
				SizeHuman:    humanSize(m.ContentLength),
				ContentType:  m.ContentType,
				UploadedAt:   mtime,
				UploadedAgo:  humanAgo(mtime),
				Downloads:    m.Downloads,
				MaxDownloads: m.MaxDownloads,
				MaxDate:      m.MaxDate,
				ExpiresIn:    expiryStr(m, mtime, gc.PurgeDays),
				Encrypted:    m.Encrypted,
				RelPath:      fileURLPath(id, name),
			}
			files = append(files, fe)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].UploadedAt.After(files[j].UploadedAt)
	})
	return files, nil
}

//go:embed templates/*.html
var tmplFS embed.FS
var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"fmtTime": func(t time.Time) string { return t.Format("2006-01-02 15:04") },
	"minus":   func(a, b float64) float64 { return a - b },
}).ParseFS(tmplFS, "templates/*.html"))

func requestScheme(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "https"
	}
	return "http"
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listFiles()
	if err != nil {
		log.Printf("list uploads: %v", err)
		http.Error(w, "cannot read the uploads directory (see the service log)", http.StatusInternalServerError)
		return
	}
	gc := readConfig()
	stats := computeStats(files)
	base := cfg.BaseURL
	if base == "" {
		base = requestScheme(r) + "://" + r.Host
	}
	data := struct {
		Files   []FileEntry
		Config  GlobalConfig
		Stats   Stats
		Title   string
		BaseURL string
	}{files, gc, stats, cfg.Title, base}
	if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Println(err)
	}
}

// Upload directories are named by transfer.sh's random token (alphanumeric).
var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validID(s string) bool {
	return idRe.MatchString(s)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validID(id) {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(filepath.Join(cfg.UploadsDir, id)); err != nil {
		log.Printf("delete %s: %v", id, err)
		http.Error(w, "delete failed (see the service log)", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func formInt(r *http.Request, key string, min, max int) (int, bool, error) {
	v := strings.TrimSpace(r.FormValue(key))
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return 0, false, fmt.Errorf("%s must be a whole number between %d and %d", key, min, max)
	}
	return n, true, nil
}

func handleExpiry(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !validID(id) {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	maxDays, setDays, err := formInt(r, "max_days", 0, maxExpiryDays)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// transfer.sh: -1 = unlimited; 0 would make the file unavailable immediately (Downloads >= 0).
	maxDownloads, setDownloads, err := formInt(r, "max_downloads", -1, maxDownloadLimit)
	if err == nil && setDownloads && maxDownloads == 0 {
		err = errors.New("max_downloads must be -1 (unlimited) or at least 1")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	subDir := filepath.Join(cfg.UploadsDir, id)
	subEntries, err := os.ReadDir(subDir)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	for _, se := range subEntries {
		if !strings.HasSuffix(se.Name(), ".metadata") {
			continue
		}
		metaPath := filepath.Join(subDir, se.Name())
		b, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m map[string]interface{}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // keep ContentLength and counters exact
		if err := dec.Decode(&m); err != nil {
			continue
		}
		if setDays {
			if maxDays > 0 {
				m["MaxDate"] = time.Now().Add(time.Duration(maxDays) * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
			} else {
				m["MaxDate"] = "0001-01-01T00:00:00Z"
			}
		}
		if setDownloads {
			m["MaxDownloads"] = maxDownloads
		}
		out, err := json.Marshal(m)
		if err != nil {
			continue
		}
		if err := os.WriteFile(metaPath, out, 0o640); err != nil {
			log.Printf("write %s: %v", metaPath, err)
			http.Error(w, "update failed (see the service log)", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	for _, id := range r.Form["ids"] {
		if !validID(id) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(cfg.UploadsDir, id)); err != nil {
			log.Printf("delete %s: %v", id, err)
		}
	}
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func renderSettings(w http.ResponseWriter, gc GlobalConfig, msg, errMsg string, status int) {
	data := struct {
		PurgeDays     int
		PurgeInterval int
		Title         string
		Message       string
		Error         string
	}{gc.PurgeDays, gc.PurgeInterval, cfg.Title, msg, errMsg}
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "settings.html", data); err != nil {
		log.Println(err)
	}
}

func handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	msg := ""
	if r.URL.Query().Get("saved") == "1" {
		msg = "Saved. transfer.sh restarts automatically within a few seconds."
	}
	renderSettings(w, readConfig(), msg, "", http.StatusOK)
}

func handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	gc := GlobalConfig{}
	var err error
	if gc.PurgeDays, _, err = formInt(r, "purge_days", 0, maxPurgeDays); err == nil {
		gc.PurgeInterval, _, err = formInt(r, "purge_interval", 0, maxPurgeInterval)
	}
	if err == nil {
		err = validateGlobalConfig(gc)
	}
	if err != nil {
		renderSettings(w, gc, "", err.Error(), http.StatusBadRequest)
		return
	}
	if err := writeConfig(gc); err != nil {
		log.Printf("write %s: %v", cfg.EnvFile, err)
		renderSettings(w, gc, "", "Could not save the settings file (see the service log).", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/settings?saved=1", http.StatusSeeOther)
}

// ---------- security middleware ----------

// sameOrigin reports whether a state-changing request was sent by a page of this admin panel.
// The panel relies on HTTP Basic credentials, which browsers attach to cross-site requests too,
// so without this check any web page could make an admin's browser delete files (CSRF).
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			return false
		}
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" {
			return false
		}
		src = u.Scheme + "://" + u.Host
	}
	o, err := url.Parse(src)
	if err != nil || o.Host == "" {
		return false
	}
	for _, a := range cfg.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(src, "/"), a) {
			return true
		}
	}
	return strings.EqualFold(o.Host, r.Host)
}

type htpasswdCache struct {
	mu    sync.Mutex
	path  string
	mtime time.Time
	size  int64
	users map[string][]byte
}

var htcache htpasswdCache

// A bcrypt hash of a random value: compared when the user does not exist, so that a wrong
// user name costs as much time as a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("transfercli-no-such-user"), bcrypt.DefaultCost)

func (c *htpasswdCache) load(path string) (map[string][]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if c.users != nil && c.path == path && st.ModTime().Equal(c.mtime) && st.Size() == c.size {
		return c.users, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	users := map[string][]byte{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		u, h, ok := strings.Cut(line, ":")
		if !ok || u == "" {
			return nil, fmt.Errorf("%s:%d: not a user:hash line", path, i+1)
		}
		if !strings.HasPrefix(h, "$2a$") && !strings.HasPrefix(h, "$2b$") && !strings.HasPrefix(h, "$2y$") {
			return nil, fmt.Errorf("%s:%d: only bcrypt hashes are accepted (create the file with htpasswd -B)", path, i+1)
		}
		users[u] = []byte(h)
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("%s: no users", path)
	}
	c.path, c.mtime, c.size, c.users = path, st.ModTime(), st.Size(), users
	return users, nil
}

// failure limiter: at most authFailBurst failed logins per authFailWindow for the whole process.
// It only slows guessing down; it never locks the panel for longer than one window.
const (
	authFailBurst  = 20
	authFailWindow = time.Minute
)

var authFails struct {
	mu    sync.Mutex
	start time.Time
	n     int
}

func authThrottled() bool {
	authFails.mu.Lock()
	defer authFails.mu.Unlock()
	if time.Since(authFails.start) > authFailWindow {
		authFails.start, authFails.n = time.Now(), 0
	}
	return authFails.n >= authFailBurst
}

func authFailed() {
	authFails.mu.Lock()
	defer authFails.mu.Unlock()
	if time.Since(authFails.start) > authFailWindow {
		authFails.start, authFails.n = time.Now(), 0
	}
	authFails.n++
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return xr
		}
	}
	return host
}

func checkBasicAuth(r *http.Request) (ok bool, configErr error) {
	users, err := htcache.load(cfg.Htpasswd)
	if err != nil {
		return false, err
	}
	u, p, has := r.BasicAuth()
	if !has {
		return false, nil
	}
	hash, known := users[u]
	if !known {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(p))
		return false, nil
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(p)) == nil, nil
}

func securityHeaders(w http.ResponseWriter) {
	extra := ""
	if cfg.BaseURL != "" {
		if u, err := url.Parse(cfg.BaseURL); err == nil && u.Host != "" {
			extra = " " + u.Scheme + "://" + u.Host
		}
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
		"img-src 'self' data: blob:"+extra+"; media-src 'self'"+extra+"; frame-src 'self'"+extra+"; connect-src 'self'"+extra+"; "+
		"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cache-Control", "no-store")
}

// admin wraps every /admin handler: security headers, authentication, method and origin checks.
func admin(methods string, next http.HandlerFunc) http.HandlerFunc {
	allowed := strings.Split(methods, ",")
	return func(w http.ResponseWriter, r *http.Request) {
		securityHeaders(w)
		if cfg.Htpasswd != "" {
			if authThrottled() {
				http.Error(w, "too many failed logins, try again in a minute", http.StatusTooManyRequests)
				return
			}
			ok, err := checkBasicAuth(r)
			if err != nil {
				log.Printf("admin auth is misconfigured: %v", err)
				http.Error(w, "admin authentication is misconfigured (see the service log)", http.StatusInternalServerError)
				return
			}
			if !ok {
				if _, _, sent := r.BasicAuth(); sent {
					authFailed()
					log.Printf("failed admin login from %s", clientAddr(r))
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="`+strings.ReplaceAll(cfg.Title, `"`, "")+` admin", charset="UTF-8"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
		}
		okMethod := false
		for _, m := range allowed {
			if r.Method == m {
				okMethod = true
			}
		}
		if !okMethod {
			w.Header().Set("Allow", methods)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodPost {
			if !sameOrigin(r) {
				log.Printf("rejected cross-origin %s %s (Origin=%q Referer=%q)", r.Method, r.URL.Path, r.Header.Get("Origin"), r.Header.Get("Referer"))
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		}
		next(w, r)
	}
}

func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/admin/delete", admin("POST", handleDelete))
	mux.HandleFunc("/admin/bulk-delete", admin("POST", handleBulkDelete))
	mux.HandleFunc("/admin/expiry", admin("POST", handleExpiry))
	mux.HandleFunc("/admin/settings", admin("GET,HEAD,POST", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleSettingsPost(w, r)
		} else {
			handleSettingsGet(w, r)
		}
	}))
	mux.HandleFunc("/admin/", admin("GET,HEAD", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/" {
			http.NotFound(w, r)
			return
		}
		handleIndex(w, r)
	}))
	mux.HandleFunc("/admin", admin("GET,HEAD", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	}))
	return mux
}

func main() {
	if cfg.Htpasswd == "" {
		log.Println("WARNING: TC_ADMIN_HTPASSWD is not set; the admin panel does not authenticate requests itself. " +
			"Only expose it through a reverse proxy that does.")
	} else if _, err := htcache.load(cfg.Htpasswd); err != nil {
		log.Fatalf("TC_ADMIN_HTPASSWD: %v", err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	log.Println("listening on " + cfg.Listen)
	log.Fatal(srv.ListenAndServe())
}
