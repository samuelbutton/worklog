package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SchemaVersion is written on every event. Changes are additive: a new field or type bumps it, an old one is never repurposed.
const SchemaVersion = 1

// Event is one line of the record. Data is free-form so a new kind of work needs no code change.
type Event struct {
	V       int            `json:"v"`
	ID      string         `json:"id"`
	TS      string         `json:"ts"`
	Type    string         `json:"type"`
	Actor   string         `json:"actor"`
	Item    string         `json:"item,omitempty"`
	Session string         `json:"session,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func (e Event) Time() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.TS)
	return t
}

func (e Event) Str(key string) string {
	if v, ok := e.Data[key]; ok {
		switch s := v.(type) {
		case string:
			return s
		case float64:
			return strconv.FormatFloat(s, 'f', -1, 64)
		case int:
			return strconv.Itoa(s)
		case int64:
			return strconv.FormatInt(s, 10)
		case bool:
			return strconv.FormatBool(s)
		}
	}
	return ""
}

// Store is the data folder: events, blobs, working copies and caches.
type Store struct {
	Home string
	now  func() time.Time
}

func DefaultHome() string {
	if h := os.Getenv("WORKLOG_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "worklog")
}

func Open(home string) (*Store, error) {
	s := &Store{Home: home, now: time.Now}
	for _, d := range []string{"", "events", "blobs/sha256", "work", "index", "archive"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.Home}, parts...)...)
}

func newID(t time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "e_" + strconv.FormatInt(t.UnixMilli(), 36) + "_" + hex.EncodeToString(b)
}

// Append writes one event to today's file under an exclusive lock, so concurrent sessions never interleave a line.
func (s *Store) Append(e Event) (Event, error) {
	t := s.now().UTC()
	e.V = SchemaVersion
	if e.ID == "" {
		e.ID = newID(t)
	}
	if e.TS == "" {
		e.TS = t.Format("2006-01-02T15:04:05.000Z07:00")
	}
	if e.Actor == "" {
		e.Actor = "user"
	}
	if e.Session == "" {
		e.Session = os.Getenv("WORKLOG_SESSION")
	}
	if e.Actor == "assistant" {
		if e.Data == nil {
			e.Data = map[string]any{}
		}
		for key, env := range map[string]string{"agent": "WORKLOG_AGENT", "model": "WORKLOG_MODEL"} {
			if _, set := e.Data[key]; !set && os.Getenv(env) != "" {
				e.Data[key] = os.Getenv(env)
			}
		}
	}
	line, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	f, err := os.OpenFile(s.path("events", t.Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return e, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return e, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if _, err := f.Write(append(line, '\n')); err != nil {
		return e, err
	}
	return e, f.Sync()
}

// Events reads every event in time order. A line that does not parse is reported, never dropped silently.
func (s *Store) Events() ([]Event, error) {
	files, _ := filepath.Glob(s.path("events", "*.jsonl"))
	sort.Strings(files)
	var out []Event
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		n := 0
		for sc.Scan() {
			n++
			if len(strings.TrimSpace(sc.Text())) == 0 {
				continue
			}
			var e Event
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s:%d: %w", name, n, err)
			}
			out = append(out, e)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}

// PutBlob stores content once by its sha256 and returns the hash.
func (s *Store) PutBlob(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	p := s.blobPath(h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp := p + ".tmp" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, b, 0o400); err != nil {
		return "", err
	}
	return h, os.Rename(tmp, p)
}

func (s *Store) Blob(h string) ([]byte, error) { return os.ReadFile(s.blobPath(h)) }

func (s *Store) blobPath(h string) string {
	if len(h) < 3 {
		return s.path("blobs", "sha256", h)
	}
	return s.path("blobs", "sha256", h[:2], h[2:])
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) logError(context string, err error) {
	f, ferr := os.OpenFile(s.path("errors.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %v\n", time.Now().UTC().Format(time.RFC3339), context, err)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return !errors.Is(err, fs.ErrNotExist)
}
