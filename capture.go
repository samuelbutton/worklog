package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// Watch scans the work folder every interval until interrupted, so each save is recorded close to when it happened.
func (s *Store) Watch(every time.Duration, out io.Writer) error {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(every)
	defer tick.Stop()
	fmt.Fprintf(out, "watching %s every %s\n", s.path("work"), every)
	for {
		events, err := s.Scan()
		if err != nil {
			s.logError("watch", err)
		}
		for _, e := range events {
			fmt.Fprintf(out, "%s  %s  %s\n", e.Time().Local().Format("15:04:05"), e.Item, s.summary(e))
		}
		select {
		case <-stop:
			return nil
		case <-tick.C:
		}
	}
}

type archived struct {
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// Archive keeps a compressed copy of every file under dir in archive/<name>/, refreshed when a file changes. It never removes
// a copy: a source that disappears keeps its last copy, and one that shrinks keeps the larger copy under a dated name.
func (s *Store) Archive(name, dir string, out io.Writer) error {
	name = slug(name)
	if name == "" {
		return fmt.Errorf("archive needs a name")
	}
	statePath := s.path("index", "archive-"+name+".json")
	state := map[string]archived{}
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &state)
	}
	save := func() {
		b, _ := json.MarshalIndent(state, "", " ")
		_ = os.WriteFile(statePath, b, 0o600)
	}
	copied, kept := 0, 0
	var written int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		prev, had := state[rel]
		if had && prev.Size == info.Size() && prev.MTime.Equal(info.ModTime()) {
			kept++
			return nil
		}
		dest := s.path("archive", name, rel+".gz")
		if had && info.Size() < prev.Size && exists(dest) {
			_ = os.Rename(dest, s.path("archive", name, rel+"."+prev.MTime.UTC().Format("20060102T150405")+".gz"))
		}
		n, err := gzipFile(p, dest)
		if err != nil {
			s.logError("archive "+rel, err)
			return nil
		}
		state[rel] = archived{Size: info.Size(), MTime: info.ModTime()}
		copied++
		written += n
		if copied%50 == 0 {
			save()
		}
		return nil
	})
	save()
	if err != nil {
		return err
	}
	_, err = s.Append(Event{Type: "archive", Actor: "system", Data: map[string]any{"name": name, "dir": dir, "copied": copied, "unchanged": kept, "bytes_written": written}})
	fmt.Fprintf(out, "%s: %d files copied (%.1f MB compressed), %d unchanged → %s\n", name, copied, float64(written)/1e6, kept, s.path("archive", name))
	return err
}

func gzipFile(src, dest string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if _, err := io.Copy(zw, in); err != nil {
		f.Close()
		return 0, err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	info, _ := f.Stat()
	if err := f.Close(); err != nil {
		return 0, err
	}
	return info.Size(), os.Rename(tmp, dest)
}
