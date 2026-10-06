package main

// Chunked uploads.
//
// A single HTTP request cannot carry a large file through proxies that cap request bodies (Cloudflare's
// Free and Pro plans: 100 MB). The chunked API lets a client create an upload session, send fixed-size
// chunks (in any order, in parallel, resumable after a disconnect) and then complete it. The server writes
// every chunk at its offset of a preallocated file and, on completion, moves the file into transfer.sh's
// storage layout (<uploads>/<token>/<name> + <name>.metadata), so it is downloaded, expired and purged
// exactly like a file uploaded with curl -T.
//
//   POST   /api/uploads                     {"filename","size","content_type"?,"max_days"?,"max_downloads"?}
//   PUT    /api/uploads/<id>/chunks/<n>     chunk bytes; optional X-Chunk-SHA256: <hex>
//   GET    /api/uploads/<id>                status: received / missing chunks
//   POST   /api/uploads/<id>/complete       → {"url","delete_url",...}
//   DELETE /api/uploads/<id>                abort
//
// Every request needs the upload credentials (TC_UPLOAD_HTPASSWD, the file transfer.sh uses).
// The upload client (tc-upload) speaks this protocol; see client/.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

type chunkConfig struct {
	Htpasswd    string        // upload credentials; empty = API disabled
	Dir         string        // session storage; should be on the same file system as the uploads
	ChunkSize   int64         // bytes per chunk (must stay below proxy body limits)
	MaxSize     int64         // largest accepted file
	Reserve     int64         // free disk space that must remain after all pending uploads
	TTL         time.Duration // sessions without activity for this long are deleted
	MaxSessions int
	ClientDir   string // directory with tc-upload binaries served under /client/
}

const mib = 1 << 20

func envInt(key string, def, min, max int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < min || n > max {
		log.Printf("%s=%q is invalid (allowed %d..%d); using %d", key, v, min, max, def)
		return def
	}
	return n
}

func loadChunkConfig() chunkConfig {
	return chunkConfig{
		Htpasswd:    getenv("TC_UPLOAD_HTPASSWD", ""),
		Dir:         getenv("TC_CHUNK_DIR", "/var/lib/transfercli/temp/chunked"),
		ChunkSize:   envInt("TC_CHUNK_SIZE_MB", 64, 1, 95) * mib,
		MaxSize:     envInt("TC_CHUNKED_MAX_GB", 200, 1, 100000) * 1024 * mib,
		Reserve:     envInt("TC_DISK_RESERVE_MB", 2048, 0, 1<<30) * mib,
		TTL:         time.Duration(envInt("TC_CHUNK_TTL_HOURS", 24, 1, 720)) * time.Hour,
		MaxSessions: int(envInt("TC_CHUNK_MAX_SESSIONS", 50, 1, 10000)),
		ClientDir:   getenv("TC_CLIENT_DIR", "/opt/transfercli/clients"),
	}
}

var ccfg = loadChunkConfig()

type session struct {
	ID           string    `json:"id"`
	Filename     string    `json:"filename"`
	Size         int64     `json:"size"`
	ChunkSize    int64     `json:"chunk_size"`
	Chunks       int       `json:"chunks"`
	ContentType  string    `json:"content_type"`
	MaxDays      int       `json:"max_days"`
	MaxDownloads int       `json:"max_downloads"`
	Created      time.Time `json:"created"`

	mu       sync.Mutex
	data     *os.File
	recv     *os.File
	received []bool
	done     bool
}

var sessions = struct {
	sync.Mutex
	m map[string]*session
}{m: map[string]*session{}}

var uploadHT htpasswdCache

var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *session) dir() string { return filepath.Join(ccfg.Dir, s.ID) }

func (s *session) chunkLen(i int) int64 {
	start := int64(i) * s.ChunkSize
	if rest := s.Size - start; rest < s.ChunkSize {
		return rest
	}
	return s.ChunkSize
}

func (s *session) missing() []int {
	var out []int
	for i, ok := range s.received {
		if !ok {
			out = append(out, i)
		}
	}
	return out
}

func (s *session) touch() {
	now := time.Now()
	_ = os.Chtimes(filepath.Join(s.dir(), "session.json"), now, now)
}

func (s *session) closeFiles() {
	if s.data != nil {
		s.data.Close()
		s.data = nil
	}
	if s.recv != nil {
		s.recv.Close()
		s.recv = nil
	}
}

// open loads a session from disk (sessions survive restarts of the admin service).
func openSession(id string) (*session, error) {
	if !sessionIDRe.MatchString(id) {
		return nil, os.ErrNotExist
	}
	sessions.Lock()
	defer sessions.Unlock()
	if s, ok := sessions.m[id]; ok {
		return s, nil
	}
	d := filepath.Join(ccfg.Dir, id)
	b, err := os.ReadFile(filepath.Join(d, "session.json"))
	if err != nil {
		return nil, err
	}
	s := &session{}
	if err := json.Unmarshal(b, s); err != nil || s.ID != id || s.Chunks < 1 || s.ChunkSize < 1 {
		return nil, fmt.Errorf("corrupt session %s", id)
	}
	if s.data, err = os.OpenFile(filepath.Join(d, "data"), os.O_RDWR, 0); err != nil {
		return nil, err
	}
	if s.recv, err = os.OpenFile(filepath.Join(d, "received"), os.O_RDWR, 0); err != nil {
		s.closeFiles()
		return nil, err
	}
	marks := make([]byte, s.Chunks)
	if _, err := s.recv.ReadAt(marks, 0); err != nil && !errors.Is(err, io.EOF) {
		s.closeFiles()
		return nil, err
	}
	s.received = make([]bool, s.Chunks)
	for i, m := range marks {
		s.received[i] = m == 1
	}
	sessions.m[id] = s
	return s, nil
}

func dropSession(s *session) {
	sessions.Lock()
	delete(sessions.m, s.ID)
	sessions.Unlock()
	s.closeFiles()
	if err := os.RemoveAll(s.dir()); err != nil {
		log.Printf("remove session %s: %v", s.ID, err)
	}
}

// pendingBytes is the space still reserved by unfinished sessions on disk.
func pendingBytes() int64 {
	var total int64
	entries, _ := os.ReadDir(ccfg.Dir)
	for _, e := range entries {
		if !sessionIDRe.MatchString(e.Name()) {
			continue
		}
		if st, err := os.Stat(filepath.Join(ccfg.Dir, e.Name(), "data")); err == nil {
			total += st.Size()
		}
	}
	return total
}

func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// sweepSessions deletes sessions without activity for longer than the TTL.
func sweepSessions() {
	entries, err := os.ReadDir(ccfg.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !sessionIDRe.MatchString(e.Name()) {
			continue
		}
		st, err := os.Stat(filepath.Join(ccfg.Dir, e.Name(), "session.json"))
		if err == nil && time.Since(st.ModTime()) < ccfg.TTL {
			continue
		}
		if err != nil {
			if dst, derr := os.Stat(filepath.Join(ccfg.Dir, e.Name())); derr == nil && time.Since(dst.ModTime()) < time.Hour {
				continue // being created right now
			}
		}
		sessions.Lock()
		s := sessions.m[e.Name()]
		delete(sessions.m, e.Name())
		sessions.Unlock()
		if s != nil {
			s.mu.Lock()
			s.closeFiles()
			s.done = true
			s.mu.Unlock()
		}
		log.Printf("expired chunked upload session %s", e.Name())
		os.RemoveAll(filepath.Join(ccfg.Dir, e.Name()))
	}
}

// ---------- validation ----------

func cleanFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "", errors.New("filename is empty")
	}
	if len(name) > 200 {
		return "", errors.New("filename is longer than 200 bytes")
	}
	if !utf8.ValidString(name) || strings.HasSuffix(name, ".metadata") {
		return "", errors.New("filename is not allowed")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("filename contains control characters")
		}
	}
	return name, nil
}

var mediaTypeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,126}/[a-z0-9][a-z0-9!#$&^_.+-]{0,126}$`)

func cleanContentType(ct, filename string) (string, error) {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		if t := mime.TypeByExtension(strings.ToLower(path.Ext(filename))); t != "" {
			return strings.ToLower(t), nil
		}
		return "application/octet-stream", nil
	}
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || !mediaTypeRe.MatchString(mt) || len(ct) > 200 {
		return "", errors.New("content_type is not a valid media type")
	}
	return mime.FormatMediaType(mt, params), nil
}

const tokenSymbols = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func randomToken(n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(tokenSymbols)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = tokenSymbols[v.Int64()]
	}
	return string(b)
}

func randomHex(nbytes int) string {
	b := make([]byte, nbytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---------- HTTP ----------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string, extra map[string]interface{}) {
	body := map[string]interface{}{"error": msg}
	for k, v := range extra {
		body[k] = v
	}
	writeJSON(w, status, body)
}

// extendDeadlines lets a slow client send one chunk (or download a client binary) without hitting the
// server-wide timeouts, which are kept short for everything else.
func extendDeadlines(w http.ResponseWriter, d time.Duration) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(d))
	_ = rc.SetWriteDeadline(time.Now().Add(d))
}

func uploadAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if ccfg.Htpasswd == "" {
			apiError(w, http.StatusNotFound, "chunked uploads are disabled (TC_UPLOAD_HTPASSWD is not set)", nil)
			return
		}
		if authThrottled(r) {
			apiError(w, http.StatusTooManyRequests, "too many failed logins, try again in a minute", nil)
			return
		}
		users, err := uploadHT.load(ccfg.Htpasswd)
		if err != nil {
			log.Printf("upload auth is misconfigured: %v", err)
			apiError(w, http.StatusInternalServerError, "upload authentication is misconfigured", nil)
			return
		}
		u, p, has := r.BasicAuth()
		ok := false
		if has {
			if hash, known := users[u]; known {
				ok = bcryptOK(hash, p)
			} else {
				bcryptOK(dummyHash, p)
			}
		}
		if !ok {
			if has {
				authFailed(r)
				log.Printf("failed upload login from %s", clientAddr(r))
			}
			h.Set("WWW-Authenticate", `Basic realm="TransferCLI upload", charset="UTF-8"`)
			apiError(w, http.StatusUnauthorized, "upload credentials required", nil)
			return
		}
		next(w, r)
	}
}

func publicBase(r *http.Request) string {
	if cfg.BaseURL != "" {
		return cfg.BaseURL
	}
	return requestScheme(r) + "://" + r.Host
}

type createRequest struct {
	Filename     string `json:"filename"`
	Size         int64  `json:"size"`
	ContentType  string `json:"content_type"`
	MaxDays      int    `json:"max_days"`
	MaxDownloads *int   `json:"max_downloads"`
}

func handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		apiError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
		return
	}
	// Requiring JSON makes the request non-"simple": a browser cannot send it cross-site (CSRF).
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		apiError(w, http.StatusUnsupportedMediaType, "send a JSON body with Content-Type: application/json", nil)
		return
	}
	var req createRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), nil)
		return
	}
	name, err := cleanFilename(req.Filename)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if req.Size < 1 || req.Size > ccfg.MaxSize {
		apiError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("size must be between 1 and %d bytes", ccfg.MaxSize), nil)
		return
	}
	ct, err := cleanContentType(req.ContentType, name)
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if req.MaxDays < 0 || req.MaxDays > maxExpiryDays {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("max_days must be between 0 and %d", maxExpiryDays), nil)
		return
	}
	maxDownloads := -1
	if req.MaxDownloads != nil {
		maxDownloads = *req.MaxDownloads
		if maxDownloads == 0 || maxDownloads < -1 || maxDownloads > maxDownloadLimit {
			apiError(w, http.StatusBadRequest, "max_downloads must be -1 (unlimited) or 1..1000000", nil)
			return
		}
	}

	if err := os.MkdirAll(ccfg.Dir, 0o750); err != nil {
		log.Printf("chunk dir: %v", err)
		apiError(w, http.StatusInternalServerError, "cannot create the upload directory", nil)
		return
	}
	sessions.Lock()
	active := len(sessions.m)
	sessions.Unlock()
	if entries, err := os.ReadDir(ccfg.Dir); err == nil && len(entries) > active {
		active = len(entries)
	}
	if active >= ccfg.MaxSessions {
		apiError(w, http.StatusServiceUnavailable, "too many unfinished uploads; try again later", nil)
		return
	}
	free, err := freeBytes(ccfg.Dir)
	if err != nil || free-ccfg.Reserve-pendingBytes() < req.Size {
		apiError(w, http.StatusInsufficientStorage, "not enough free disk space for this file", nil)
		return
	}

	s := &session{
		ID: randomHex(16), Filename: name, Size: req.Size, ChunkSize: ccfg.ChunkSize,
		ContentType: ct, MaxDays: req.MaxDays, MaxDownloads: maxDownloads, Created: time.Now().UTC(),
	}
	s.Chunks = int((s.Size + s.ChunkSize - 1) / s.ChunkSize)
	if err := createSessionFiles(s); err != nil {
		log.Printf("create session: %v", err)
		os.RemoveAll(s.dir())
		if errors.Is(err, syscall.ENOSPC) {
			apiError(w, http.StatusInsufficientStorage, "not enough free disk space for this file", nil)
			return
		}
		apiError(w, http.StatusInternalServerError, "cannot create the upload session", nil)
		return
	}
	sessions.Lock()
	sessions.m[s.ID] = s
	sessions.Unlock()
	log.Printf("chunked upload %s started: %q, %d bytes, %d chunks", s.ID, s.Filename, s.Size, s.Chunks)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id": s.ID, "filename": s.Filename, "size": s.Size, "chunk_size": s.ChunkSize, "chunks": s.Chunks,
		"expires_after_seconds": int(ccfg.TTL.Seconds()),
	})
}

func createSessionFiles(s *session) error {
	d := s.dir()
	if err := os.Mkdir(d, 0o750); err != nil {
		return err
	}
	data, err := os.OpenFile(filepath.Join(d, "data"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	s.data = data
	// Reserve the blocks up front so a full disk is reported now, not after gigabytes were sent.
	if err := syscall.Fallocate(int(data.Fd()), 0, 0, s.Size); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			s.closeFiles()
			return err
		}
		if err := data.Truncate(s.Size); err != nil {
			s.closeFiles()
			return err
		}
	}
	recv, err := os.OpenFile(filepath.Join(d, "received"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		s.closeFiles()
		return err
	}
	s.recv = recv
	if err := recv.Truncate(int64(s.Chunks)); err != nil {
		s.closeFiles()
		return err
	}
	s.received = make([]bool, s.Chunks)
	meta, err := json.Marshal(s)
	if err != nil {
		s.closeFiles()
		return err
	}
	if err := os.WriteFile(filepath.Join(d, "session.json"), meta, 0o640); err != nil {
		s.closeFiles()
		return err
	}
	return nil
}

// offsetWriter writes sequentially into a file starting at an offset (WriteAt is safe for parallel chunks).
type offsetWriter struct {
	f   *os.File
	off int64
}

func (o *offsetWriter) Write(p []byte) (int, error) {
	n, err := o.f.WriteAt(p, o.off)
	o.off += int64(n)
	return n, err
}

var chunkPathRe = regexp.MustCompile(`^/api/uploads/([0-9a-f]{32})(?:/(complete|chunks/([0-9]{1,9})))?$`)

func handleUploadSession(w http.ResponseWriter, r *http.Request) {
	m := chunkPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		apiError(w, http.StatusNotFound, "not found", nil)
		return
	}
	id, action, idx := m[1], m[2], m[3]
	s, err := openSession(id)
	if err != nil {
		apiError(w, http.StatusNotFound, "upload session not found (finished, aborted or expired)", nil)
		return
	}
	switch {
	case action == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		uploadStatus(w, s)
	case action == "" && r.Method == http.MethodDelete:
		s.mu.Lock()
		s.done = true
		s.mu.Unlock()
		dropSession(s)
		log.Printf("chunked upload %s aborted", s.ID)
		w.WriteHeader(http.StatusNoContent)
	case action == "complete" && r.Method == http.MethodPost:
		completeUpload(w, r, s)
	case strings.HasPrefix(action, "chunks/") && r.Method == http.MethodPut:
		n, _ := strconv.Atoi(idx)
		putChunk(w, r, s, n)
	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
	}
}

func uploadStatus(w http.ResponseWriter, s *session) {
	s.mu.Lock()
	missing := s.missing()
	if missing == nil {
		missing = []int{}
	}
	body := map[string]interface{}{
		"id": s.ID, "filename": s.Filename, "size": s.Size, "chunk_size": s.ChunkSize, "chunks": s.Chunks,
		"received": s.Chunks - len(missing), "missing": missing,
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, body)
}

func putChunk(w http.ResponseWriter, r *http.Request, s *session, i int) {
	if i < 0 || i >= s.Chunks {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("chunk index must be 0..%d", s.Chunks-1), nil)
		return
	}
	want := s.chunkLen(i)
	if r.ContentLength >= 0 && r.ContentLength != want {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("chunk %d must be exactly %d bytes", i, want), map[string]interface{}{"expected": want})
		return
	}
	wantSum := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Chunk-SHA256")))
	if wantSum != "" && (len(wantSum) != 64 || strings.Trim(wantSum, "0123456789abcdef") != "") {
		apiError(w, http.StatusBadRequest, "X-Chunk-SHA256 must be 64 hex characters", nil)
		return
	}
	extendDeadlines(w, 30*time.Minute)

	s.mu.Lock()
	if s.done || s.data == nil {
		s.mu.Unlock()
		apiError(w, http.StatusNotFound, "upload session not found (finished, aborted or expired)", nil)
		return
	}
	data := s.data
	s.mu.Unlock()

	h := sha256.New()
	ow := &offsetWriter{f: data, off: int64(i) * s.ChunkSize}
	n, err := io.CopyN(io.MultiWriter(ow, h), r.Body, want)
	if err != nil || n != want {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("chunk %d ended after %d of %d bytes", i, n, want), nil)
		return
	}
	if extra, _ := io.CopyN(io.Discard, r.Body, 1); extra > 0 {
		apiError(w, http.StatusBadRequest, fmt.Sprintf("chunk %d is longer than %d bytes", i, want), nil)
		return
	}
	got := hex.EncodeToString(h.Sum(nil))
	if wantSum != "" && got != wantSum {
		apiError(w, http.StatusUnprocessableEntity, fmt.Sprintf("chunk %d checksum mismatch", i), map[string]interface{}{"sha256": got})
		return
	}
	if err := data.Sync(); err != nil {
		log.Printf("sync %s: %v", s.ID, err)
		apiError(w, http.StatusInternalServerError, "could not store the chunk", nil)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done || s.recv == nil {
		apiError(w, http.StatusNotFound, "upload session not found (finished, aborted or expired)", nil)
		return
	}
	if !s.received[i] {
		if _, err := s.recv.WriteAt([]byte{1}, int64(i)); err != nil {
			log.Printf("mark %s/%d: %v", s.ID, i, err)
			apiError(w, http.StatusInternalServerError, "could not store the chunk", nil)
			return
		}
		_ = s.recv.Sync()
		s.received[i] = true
	}
	s.touch()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"index": i, "sha256": got, "received": s.Chunks - len(s.missing()), "chunks": s.Chunks,
	})
}

func completeUpload(w http.ResponseWriter, r *http.Request, s *session) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		apiError(w, http.StatusNotFound, "upload session not found (finished, aborted or expired)", nil)
		return
	}
	if missing := s.missing(); len(missing) > 0 {
		s.mu.Unlock()
		apiError(w, http.StatusConflict, fmt.Sprintf("%d chunks are missing", len(missing)), map[string]interface{}{"missing": missing})
		return
	}
	s.done = true
	s.closeFiles()
	s.mu.Unlock()

	var token, dst string
	for attempt := 0; ; attempt++ {
		token = randomToken(10)
		dst = filepath.Join(cfg.UploadsDir, token)
		err := os.Mkdir(dst, 0o750)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) || attempt > 5 {
			log.Printf("complete %s: %v", s.ID, err)
			reopenOnFailure(s)
			apiError(w, http.StatusInternalServerError, "could not store the file", nil)
			return
		}
	}
	maxDate := time.Time{}
	if s.MaxDays > 0 {
		maxDate = time.Now().Add(time.Duration(s.MaxDays) * 24 * time.Hour)
	}
	deletion := randomToken(10) + randomToken(10)
	meta := map[string]interface{}{ // transfer.sh's metadata struct (field names, no JSON tags)
		"ContentType": s.ContentType, "ContentLength": s.Size, "Downloads": 0, "MaxDownloads": s.MaxDownloads,
		"MaxDate": maxDate, "DeletionToken": deletion, "Encrypted": false, "DecryptedContentType": "",
	}
	b, _ := json.Marshal(meta)
	target := filepath.Join(dst, s.Filename)
	// Metadata first: until the data file appears, transfer.sh answers 404 for this token.
	if err := os.WriteFile(target+".metadata", b, 0o640); err != nil {
		log.Printf("complete %s: metadata: %v", s.ID, err)
		os.RemoveAll(dst)
		reopenOnFailure(s)
		apiError(w, http.StatusInternalServerError, "could not store the file", nil)
		return
	}
	if err := moveFile(filepath.Join(s.dir(), "data"), target); err != nil {
		log.Printf("complete %s: %v", s.ID, err)
		os.RemoveAll(dst) // the data is still in the session directory; the client can retry
		reopenOnFailure(s)
		apiError(w, http.StatusInternalServerError, "could not store the file", nil)
		return
	}
	dropSession(s)
	base := publicBase(r)
	fileURL := base + fileURLPath(token, s.Filename)
	deleteURL := fileURL + "/" + url.PathEscape(deletion)
	w.Header().Set("X-Url-Delete", deleteURL)
	log.Printf("chunked upload %s completed: %s (%d bytes)", s.ID, fileURLPath(token, s.Filename), s.Size)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"url": fileURL, "delete_url": deleteURL, "filename": s.Filename, "size": s.Size,
	})
}

// reopenOnFailure makes a session usable again after a failed completion, so the client can retry.
func reopenOnFailure(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = false
	sessions.Lock()
	delete(sessions.m, s.ID) // next request reloads it from disk
	sessions.Unlock()
}

// moveFile renames src to dst; across file systems it copies and removes the source.
func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// ---------- upload client downloads (/client/) ----------

var clientNameRe = regexp.MustCompile(`^tc-upload-(linux|darwin|windows)-(amd64|arm64|arm)(\.exe)?$`)

func handleClient(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/client/")
	if name == "" {
		clientIndex(w, r)
		return
	}
	if !clientNameRe.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(ccfg.ClientDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	extendDeadlines(w, 10*time.Minute)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "public, max-age=300")
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func clientIndex(w http.ResponseWriter, r *http.Request) {
	base := publicBase(r)
	var names []string
	entries, _ := os.ReadDir(ccfg.ClientDir)
	for _, e := range entries {
		if clientNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "tc-upload - upload files of any size to %s (sent in chunks, resumable)\n\n", base)
	if len(names) == 0 {
		fmt.Fprintln(w, "No client binaries are installed on this server.")
		return
	}
	fmt.Fprintln(w, "Downloads:")
	for _, n := range names {
		fmt.Fprintf(w, "  %s/client/%s\n", base, n)
	}
	fmt.Fprintf(w, "\nWindows (cmd):\n  curl -o tc-upload.exe %s/client/tc-upload-windows-amd64.exe\n  tc-upload.exe -s %s C:\\path\\to\\file.iso\n", base, base)
	fmt.Fprintf(w, "\nLinux / macOS:\n  curl -o tc-upload %s/client/tc-upload-linux-amd64 && chmod +x tc-upload\n  ./tc-upload -s %s ./file.iso\n", base, base)
	fmt.Fprintln(w, "\nThe client asks for the upload password (or reads TC_PASSWORD). Checksums: SHA256SUMS of the TransferCLI release.")
}
