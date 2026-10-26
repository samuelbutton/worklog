package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Item is one piece of work, folded from its events.
type Item struct {
	ID       string
	Kind     string
	Title    string
	Task     string
	Org      string
	People   []string
	Tags     map[string]string
	File     string
	Status   string
	Created  Event
	Events   []Event
	Versions []Event
}

func (it *Item) Dir(s *Store) string { return s.path("work", it.ID) }

// MainFile is the working copy the assistant drafts into and the founder edits.
func (it *Item) MainFile(s *Store) string { return filepath.Join(it.Dir(s), it.File) }

// LastBy returns the latest version of the main file by an author ("" for anyone), skipping deletions.
func (it *Item) LastBy(author string) *Event {
	for i := len(it.Versions) - 1; i >= 0; i-- {
		v := it.Versions[i]
		if v.Str("path") != it.File || v.Data["deleted"] == true {
			continue
		}
		if author == "" || v.Actor == author {
			return &it.Versions[i]
		}
	}
	return nil
}

// FirstBy returns the earliest version of the main file by an author.
func (it *Item) FirstBy(author string) *Event {
	for i := range it.Versions {
		v := it.Versions[i]
		if v.Str("path") == it.File && v.Data["deleted"] != true && v.Actor == author {
			return &it.Versions[i]
		}
	}
	return nil
}

func (it *Item) Final() *Event {
	for i := len(it.Versions) - 1; i >= 0; i-- {
		if it.Versions[i].Str("stage") == "final" {
			return &it.Versions[i]
		}
	}
	return nil
}

func (it *Item) OfType(t string) []Event {
	var out []Event
	for _, e := range it.Events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Fold builds every item from the record. An item event may come after versions (a folder made by hand, named later).
func Fold(events []Event) map[string]*Item {
	items := map[string]*Item{}
	get := func(id string) *Item {
		it, ok := items[id]
		if !ok {
			it = &Item{ID: id, File: "draft.md", Status: "drafting", Tags: map[string]string{}}
			items[id] = it
		}
		return it
	}
	for _, e := range events {
		if e.Item == "" {
			continue
		}
		it := get(e.Item)
		it.Events = append(it.Events, e)
		switch e.Type {
		case "item":
			it.Created = e
			it.Kind, it.Title, it.Task, it.Org = e.Str("kind"), e.Str("title"), e.Str("task"), e.Str("org")
			if f := e.Str("file"); f != "" {
				it.File = f
			}
			it.People = strs(e.Data["people"])
			if tags, ok := e.Data["tags"].(map[string]any); ok {
				for k, v := range tags {
					it.Tags[k] = fmt.Sprint(v)
				}
			}
		case "version":
			it.Versions = append(it.Versions, e)
			if e.Str("stage") == "final" && it.Status == "drafting" {
				it.Status = "ready"
			}
		case "status":
			it.Status = e.Str("status")
		}
	}
	return items
}

func strs(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}

// NewItem records an item and makes its work folder.
func (s *Store) NewItem(id, kind, title, task, org, file string, people []string, tags map[string]string, actor string) (*Item, error) {
	events, err := s.Events()
	if err != nil {
		return nil, err
	}
	items := Fold(events)
	if id == "" {
		id = slug(kind + "-" + title)
		base := id
		for n := 2; items[id] != nil || exists(s.path("work", id)); n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
	} else if it := items[id]; it != nil && it.Created.ID != "" {
		return nil, fmt.Errorf("item %s already exists", id)
	}
	if file == "" {
		file = "draft.md"
	}
	data := map[string]any{"kind": kind, "title": title, "file": file}
	if task != "" {
		data["task"] = task
	}
	if org != "" {
		data["org"] = org
	}
	if len(people) > 0 {
		data["people"] = people
	}
	if len(tags) > 0 {
		data["tags"] = tags
	}
	if err := os.MkdirAll(s.path("work", id), 0o700); err != nil {
		return nil, err
	}
	if _, err := s.Append(Event{Type: "item", Actor: actor, Item: id, Data: data}); err != nil {
		return nil, err
	}
	return s.Item(id)
}

func (s *Store) Item(id string) (*Item, error) {
	events, err := s.Events()
	if err != nil {
		return nil, err
	}
	it := Fold(events)[id]
	if it == nil {
		return nil, fmt.Errorf("no item %q (worklog items lists them)", id)
	}
	return it, nil
}

// fileIndex remembers the last recorded hash and version event of every working file, keyed "<item>/<path>".
// It is a cache of the version events and is rebuilt from them when missing.
type fileState struct {
	Hash  string `json:"hash"`
	Event string `json:"event"`
}

func (s *Store) loadIndex() (map[string]fileState, error) {
	idx := map[string]fileState{}
	b, err := os.ReadFile(s.path("index", "files.json"))
	if err == nil && json.Unmarshal(b, &idx) == nil {
		return idx, nil
	}
	return s.Reindex()
}

func (s *Store) saveIndex(idx map[string]fileState) error {
	b, _ := json.MarshalIndent(idx, "", " ")
	tmp := s.path("index", fmt.Sprintf("files.json.%d", os.Getpid()))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path("index", "files.json"))
}

// Reindex rebuilds the file index from the version events.
func (s *Store) Reindex() (map[string]fileState, error) {
	events, err := s.Events()
	if err != nil {
		return nil, err
	}
	idx := map[string]fileState{}
	for _, e := range events {
		if e.Type != "version" {
			continue
		}
		key := e.Item + "/" + e.Str("path")
		if e.Data["deleted"] == true {
			delete(idx, key)
			continue
		}
		idx[key] = fileState{Hash: e.Str("blob"), Event: e.ID}
	}
	return idx, s.saveIndex(idx)
}

// VersionInput is one state of a working file to record.
type VersionInput struct {
	Item    string
	Path    string // relative to the item's work folder
	Content []byte
	Deleted bool
	Actor   string // assistant or user
	Via     string // cli, paste, scan
	Stage   string // proposed, revised, edited, final; inferred when empty
	Note    string
	Extra   map[string]any
}

// RecordVersion appends a version unless the content is what was last recorded for that file. It reports whether it wrote one.
func (s *Store) RecordVersion(in VersionInput) (Event, bool, error) {
	idx, err := s.loadIndex()
	if err != nil {
		return Event{}, false, err
	}
	key := in.Item + "/" + in.Path
	prev, had := idx[key]
	data := map[string]any{"path": in.Path, "via": in.Via}
	for k, v := range in.Extra {
		data[k] = v
	}
	if in.Deleted {
		if !had {
			return Event{}, false, nil
		}
		data["deleted"] = true
	} else {
		h := hashOf(in.Content)
		if had && prev.Hash == h && in.Stage != "final" {
			return Event{}, false, nil
		}
		if _, err := s.PutBlob(in.Content); err != nil {
			return Event{}, false, err
		}
		data["blob"], data["bytes"] = h, len(in.Content)
	}
	if had {
		data["parent"] = prev.Event
	}
	stage := in.Stage
	if stage == "" {
		switch {
		case in.Actor == "assistant" && !had:
			stage = "proposed"
		case in.Actor == "assistant":
			stage = "revised"
		default:
			stage = "edited"
		}
	}
	data["stage"] = stage
	if in.Note != "" {
		data["note"] = in.Note
	}
	e, err := s.Append(Event{Type: "version", Actor: in.Actor, Item: in.Item, Data: data})
	if err != nil {
		return e, false, err
	}
	if in.Deleted {
		delete(idx, key)
	} else {
		idx[key] = fileState{Hash: data["blob"].(string), Event: e.ID}
	}
	return e, true, s.saveIndex(idx)
}

// Scan records every working file that changed since its last version as the founder's edit. Assistants write working copies
// only through the CLI, which records them as it writes, so any other change is a person's save in an ordinary editor.
func (s *Store) Scan() ([]Event, error) {
	actor := "user"
	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Event
	root := s.path("work")
	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p != root && (strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".swp")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() && d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, p := range files {
		rel, _ := filepath.Rel(root, p)
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		if len(parts) != 2 {
			continue // a loose file directly in work/ belongs to no item
		}
		seen[parts[0]+"/"+parts[1]] = true
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if st, ok := idx[parts[0]+"/"+parts[1]]; ok && st.Hash == hashOf(b) {
			continue
		}
		e, wrote, err := s.RecordVersion(VersionInput{Item: parts[0], Path: parts[1], Content: b, Actor: actor, Via: "scan"})
		if err != nil {
			return out, err
		}
		if wrote {
			out = append(out, e)
		}
	}
	keys := make([]string, 0, len(idx))
	for k := range idx {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if seen[k] {
			continue
		}
		parts := strings.SplitN(k, "/", 2)
		if exists(s.path("work", parts[0], parts[1])) {
			continue
		}
		e, wrote, err := s.RecordVersion(VersionInput{Item: parts[0], Path: parts[1], Deleted: true, Actor: actor, Via: "scan"})
		if err != nil {
			return out, err
		}
		if wrote {
			out = append(out, e)
		}
	}
	return out, nil
}
