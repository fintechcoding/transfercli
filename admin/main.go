package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var cfg = loadConfig()

type Config struct {
	Listen      string
	UploadsDir  string
	EnvFile     string
	ServiceName string
	BaseURL     string
	Title       string
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	return Config{
		Listen:      getenv("TC_LISTEN", "127.0.0.1:8082"),
		UploadsDir:  getenv("TC_UPLOADS_DIR", "/var/lib/transfercli/uploads"),
		EnvFile:     getenv("TC_ENV_FILE", "/etc/transfercli.env"),
		ServiceName: getenv("TC_SERVICE_NAME", "transfercli"),
		BaseURL:     strings.TrimRight(getenv("TC_BASE_URL", ""), "/"),
		Title:       getenv("TC_TITLE", "TransferCLI"),
	}
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

func readConfig() GlobalConfig {
	gcfg := GlobalConfig{}
	f, err := os.Open(cfg.EnvFile)
	if err != nil {
		return gcfg
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		v = strings.Trim(v, `"' `)
		n, _ := strconv.Atoi(v)
		switch strings.TrimSpace(k) {
		case "PURGE_DAYS":
			gcfg.PurgeDays = n
		case "PURGE_INTERVAL":
			gcfg.PurgeInterval = n
		}
	}
	return gcfg
}

func writeConfig(gcfg GlobalConfig) error {
	content := fmt.Sprintf("PURGE_DAYS=%d\nPURGE_INTERVAL=%d\n", gcfg.PurgeDays, gcfg.PurgeInterval)
	return os.WriteFile(cfg.EnvFile, []byte(content), 0644)
}

func listFiles() ([]FileEntry, error) {
	entries, err := os.ReadDir(cfg.UploadsDir)
	if err != nil {
		return nil, err
	}
	gc := readConfig()
	var files []FileEntry
	for _, e := range entries {
		if !e.IsDir() {
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
				RelPath:      "/" + id + "/" + name,
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

func handleIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listFiles()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gc := readConfig()
	stats := computeStats(files)
	base := cfg.BaseURL
	if base == "" {
		scheme := "http"
		if r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
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

func validID(s string) bool {
	if s == "" || strings.ContainsAny(s, "/\\.") {
		return false
	}
	return true
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if !validID(id) {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := os.RemoveAll(filepath.Join(cfg.UploadsDir, id)); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/admin/", 303)
}

func handleExpiry(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	id := r.URL.Query().Get("id")
	if !validID(id) {
		http.Error(w, "invalid id", 400)
		return
	}
	maxDays := r.FormValue("max_days")
	maxDownloads := r.FormValue("max_downloads")
	subDir := filepath.Join(cfg.UploadsDir, id)
	subEntries, err := os.ReadDir(subDir)
	if err != nil {
		http.Error(w, err.Error(), 500)
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
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if maxDays != "" {
			if n, err := strconv.Atoi(maxDays); err == nil {
				if n > 0 {
					m["MaxDate"] = time.Now().Add(time.Duration(n) * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
				} else {
					m["MaxDate"] = "0001-01-01T00:00:00Z"
				}
			}
		}
		if maxDownloads != "" {
			if n, err := strconv.Atoi(maxDownloads); err == nil {
				m["MaxDownloads"] = n
			}
		}
		out, _ := json.Marshal(m)
		os.WriteFile(metaPath, out, 0644)
	}
	http.Redirect(w, r, "/admin/", 303)
}


func handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	r.ParseForm()
	ids := r.Form["ids"]
	for _, id := range ids {
		if validID(id) {
			os.RemoveAll(filepath.Join(cfg.UploadsDir, id))
		}
	}
	http.Redirect(w, r, "/admin/", 303)
}

func handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	gc := readConfig()
	data := struct {
		PurgeDays     int
		PurgeInterval int
		Title         string
	}{gc.PurgeDays, gc.PurgeInterval, cfg.Title}
	tmpl.ExecuteTemplate(w, "settings.html", data)
}

func handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	pd, _ := strconv.Atoi(r.FormValue("purge_days"))
	pi, _ := strconv.Atoi(r.FormValue("purge_interval"))
	if err := writeConfig(GlobalConfig{PurgeDays: pd, PurgeInterval: pi}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out, err := exec.Command("sudo", "-n", "/bin/systemctl", "restart", cfg.ServiceName).CombinedOutput()
	if err != nil {
		http.Error(w, "restart failed: "+string(out)+" ("+err.Error()+")", 500)
		return
	}
	http.Redirect(w, r, "/admin/settings", 303)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/admin/delete", handleDelete)
	mux.HandleFunc("/admin/bulk-delete", handleBulkDelete)
	mux.HandleFunc("/admin/expiry", handleExpiry)
	mux.HandleFunc("/admin/settings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			handleSettingsPost(w, r)
		} else {
			handleSettingsGet(w, r)
		}
	})
	mux.HandleFunc("/admin/", handleIndex)
	mux.HandleFunc("/admin", handleIndex)
	log.Println("listening on " + cfg.Listen)
	log.Fatal(http.ListenAndServe(cfg.Listen, mux))
}
