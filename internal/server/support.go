package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/Matiaspnyhus/Local-media-project/internal/ids"
)

func newID() string { return ids.New() }

// logger writes plain lines to a file in the data folder and to the console.
// Handlers log paths, never query strings, so signed links and tokens do not
// end up in the log.
type logger struct {
	mu   sync.Mutex
	file *os.File
	out  io.Writer
	path string
}

func newLogger(dataDir string, out io.Writer) (*logger, error) {
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "localmedia.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &logger{file: f, out: out, path: path}, nil
}

func (l *logger) Printf(format string, args ...any) {
	line := time.Now().UTC().Format("2006-01-02T15:04:05Z") + " " + fmt.Sprintf(format, args...) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	l.file.WriteString(line)
	if l.out != nil {
		io.WriteString(l.out, line)
	}
}

var (
	reIPv4  = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
	reToken = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
)

// redact removes addresses and anything long enough to be a secret.
func redact(b []byte) []byte {
	b = reIPv4.ReplaceAll(b, []byte("[address]"))
	return reToken.ReplaceAll(b, []byte("[removed]"))
}

// tail returns the last max bytes of the log, redacted.
func (l *logger) tail(max int64) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.Open(l.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > max {
		f.Seek(-max, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	return redact(b)
}

// clientEvent is something a player reported, usually a file that would not
// play. These are what the support bundle is mostly for.
type clientEvent struct {
	Time      string `json:"time"`
	Kind      string `json:"kind"`
	File      string `json:"file,omitempty"`
	Detail    string `json:"detail,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`
}

type eventLog struct {
	mu     sync.Mutex
	events []clientEvent
}

func (e *eventLog) add(ev clientEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, ev)
	if len(e.events) > 500 {
		e.events = e.events[len(e.events)-500:]
	}
}

func (e *eventLog) all() []clientEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]clientEvent{}, e.events...)
}

// loginLimiter slows down password guessing. After 5 wrong tries from one
// address within 10 minutes, further tries are refused until they age out.
type loginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	now      func() time.Time
}

const (
	loginWindow   = 10 * time.Minute
	loginMaxFails = 5
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{failures: map[string][]time.Time{}, now: time.Now}
}

func (l *loginLimiter) recent(ip string) []time.Time {
	cut := l.now().Add(-loginWindow)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = kept
	}
	return kept
}

func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) >= loginMaxFails
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recent(ip), l.now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

var mediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
	".mkv": "video/x-matroska", ".avi": "video/x-msvideo", ".ts": "video/mp2t", ".m2ts": "video/mp2t",
	".wmv": "video/x-ms-wmv", ".mpg": "video/mpeg", ".mpeg": "video/mpeg",
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "private, no-store") }
