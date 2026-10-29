package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// serve.go is a page for the founder: read, edit and confirm items, and give feedback, without the command line.
// It writes only through the same calls the commands use, so the record is the same either way. It listens on loopback only.

//go:embed ui.html
var uiHTML []byte

// Serve runs the page until the process stops. Requests are handled one at a time, each after a scan, like a command.
func (s *Store) Serve(addr string, out io.Writer) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("serve listens on loopback only (the data holds prospects' names), not %q", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "worklog: http://%s\n", ln.Addr())
	return http.Serve(ln, s.handler())
}

type api struct {
	s  *Store
	mu sync.Mutex
}

func (s *Store) handler() http.Handler {
	a := &api{s: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(uiHTML)
	})
	mux.HandleFunc("GET /api/items", a.wrap(a.items))
	mux.HandleFunc("GET /api/items/{id}", a.wrap(a.item))
	mux.HandleFunc("GET /api/items/{id}/diff", a.wrap(a.diff))
	mux.HandleFunc("POST /api/items/{id}/{action}", a.wrap(a.act))
	return guard(mux)
}

// guard refuses requests from any other site: a page elsewhere must not read or write the record through the browser.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin write refused", http.StatusForbidden)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "send JSON", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

type httpErr struct {
	code int
	msg  string
}

func (e httpErr) Error() string { return e.msg }

func (a *api) wrap(f func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, err := a.s.Scan(); err != nil {
			a.s.logError("serve scan", err)
		}
		v, err := f(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			code := http.StatusBadRequest
			var he httpErr
			if errors.As(err, &he) {
				code = he.code
			}
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(v)
	}
}

// itemView is what the page shows about an item. Waiting says whose move it is: "you" (a proposal to read), "assistant"
// (an instruction not yet answered), "send" (confirmed, not sent), "reply" (sent, no outcome), or "".
type itemView struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Task     string            `json:"task,omitempty"`
	Org      string            `json:"org,omitempty"`
	People   []string          `json:"people,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Status   string            `json:"status"`
	Waiting  string            `json:"waiting"`
	Last     string            `json:"last"`
	Versions int               `json:"versions"`
	Edited   int               `json:"edited"`
}

func view(it *Item) itemView {
	v := itemView{ID: it.ID, Kind: it.Kind, Title: it.Title, Task: it.Task, Org: it.Org, People: it.People, Status: it.Status,
		Versions: len(it.Versions), Last: it.Events[len(it.Events)-1].TS}
	if v.Title == "" {
		v.Title = it.ID
	}
	if len(it.Tags) > 0 {
		v.Tags = it.Tags
	}
	for _, e := range it.Versions {
		if e.Actor == "user" {
			v.Edited++
		}
	}
	v.Waiting = waiting(it)
	return v
}

func waiting(it *Item) string {
	switch it.Status {
	case "done", "dropped":
		return ""
	case "sent", "waiting":
		if len(it.OfType("outcome")) == 0 {
			return "reply"
		}
		return ""
	case "ready":
		return "send"
	}
	// Drafting: an instruction after the assistant's last version is the assistant's move; otherwise a proposal is the founder's.
	lastAsst, lastInstr := "", ""
	for _, e := range it.Events {
		switch {
		case e.Type == "version" && e.Actor == "assistant":
			lastAsst = e.TS
		case e.Type == "instruction":
			lastInstr = e.TS
		}
	}
	if lastInstr != "" && lastInstr > lastAsst {
		return "assistant"
	}
	if lastAsst != "" {
		return "you"
	}
	return ""
}

func (a *api) items(r *http.Request) (any, error) {
	events, err := a.s.Events()
	if err != nil {
		return nil, err
	}
	all := r.URL.Query().Get("all") != ""
	var out []itemView
	for _, it := range Fold(events) {
		if len(it.Events) == 0 || (!all && (it.Status == "done" || it.Status == "dropped")) {
			continue
		}
		out = append(out, view(it))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last > out[j].Last })
	return out, nil
}

type eventView struct {
	ID      string         `json:"id"`
	TS      string         `json:"ts"`
	Type    string         `json:"type"`
	Actor   string         `json:"actor"`
	Summary string         `json:"summary"`
	Data    map[string]any `json:"data,omitempty"`
	Text    string         `json:"text,omitempty"`
}

func (a *api) item(r *http.Request) (any, error) {
	it, err := a.find(r)
	if err != nil {
		return nil, err
	}
	work, _ := os.ReadFile(it.MainFile(a.s))
	res := map[string]any{"item": view(it), "file": it.MainFile(a.s), "text": string(work), "hash": hashOf(work)}
	var evs []eventView
	for _, e := range it.Events {
		ev := eventView{ID: e.ID, TS: e.TS, Type: e.Type, Actor: e.Actor, Summary: a.s.summary(e), Data: e.Data}
		// Replies and long notes are blobs; the page shows them in full.
		if e.Type == "outcome" || e.Type == "message" || e.Type == "front-door-check" {
			if h := e.Str("blob"); h != "" {
				b, _ := a.s.Blob(h)
				ev.Text = string(b)
			}
		}
		evs = append(evs, ev)
	}
	res["events"] = evs
	if p := it.LastBy("assistant"); p != nil {
		b, _ := a.s.Blob(p.Str("blob"))
		res["proposal"] = map[string]any{"id": p.ID, "ts": p.TS, "stage": p.Str("stage"), "note": p.Str("note"), "text": string(b)}
		res["stats"] = editStats(string(b), string(work))
	}
	if f := it.Final(); f != nil {
		b, _ := a.s.Blob(f.Str("blob"))
		res["final"] = map[string]any{"id": f.ID, "ts": f.TS, "via": f.Str("via"), "text": string(b)}
	}
	return res, nil
}

// diff compares two versions word by word: by default the assistant's last proposal against the working copy.
func (a *api) diff(r *http.Request) (any, error) {
	it, err := a.find(r)
	if err != nil {
		return nil, err
	}
	text := func(id string) (string, error) {
		for _, v := range it.Versions {
			if v.ID == id {
				b, err := a.s.Blob(v.Str("blob"))
				return string(b), err
			}
		}
		return "", httpErr{http.StatusNotFound, "no version " + id + " in " + it.ID}
	}
	var before, after string
	if from := r.URL.Query().Get("from"); from != "" {
		if before, err = text(from); err != nil {
			return nil, err
		}
	} else if p := it.LastBy("assistant"); p != nil {
		b, _ := a.s.Blob(p.Str("blob"))
		before = string(b)
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if after, err = text(to); err != nil {
			return nil, err
		}
	} else {
		b, _ := os.ReadFile(it.MainFile(a.s))
		after = string(b)
	}
	return map[string]any{"ops": wordDiff(before, after), "stats": editStats(before, after)}, nil
}

var tokenRE = regexp.MustCompile(`\s+|[\p{L}\p{N}'’]+|.`)

// wordDiff aligns words and the spaces between them, then merges runs of the same kind for display.
func wordDiff(before, after string) [][2]string {
	a, b := tokenRE.FindAllString(before, -1), tokenRE.FindAllString(after, -1)
	var ops []op
	if len(a)*len(b) > 16_000_000 {
		ops = lcs(strings.SplitAfter(before, "\n"), strings.SplitAfter(after, "\n"))
	} else {
		ops = lcs(a, b)
	}
	var out [][2]string
	for _, o := range ops {
		k := string(o.kind)
		if n := len(out); n > 0 && out[n-1][0] == k {
			out[n-1][1] += o.text
			continue
		}
		out = append(out, [2]string{k, o.text})
	}
	return out
}

func (a *api) find(r *http.Request) (*Item, error) {
	it, err := a.s.Item(r.PathValue("id"))
	if err != nil {
		return nil, httpErr{http.StatusNotFound, err.Error()}
	}
	return it, nil
}

// actBody is every field any action takes; each action reads the ones it needs.
type actBody struct {
	Text    string         `json:"text"`
	Base    string         `json:"base"` // hash of the working copy the page loaded, to refuse saving over a newer proposal
	Status  string         `json:"status"`
	Channel string         `json:"channel"`
	To      string         `json:"to"`
	Note    string         `json:"note"`
	Result  string         `json:"result"`
	Reply   string         `json:"reply"`
	Target  string         `json:"target"`
	Labels  map[string]any `json:"labels"`
	Paste   bool           `json:"paste"`
}

func (a *api) act(r *http.Request) (any, error) {
	it, err := a.find(r)
	if err != nil {
		return nil, err
	}
	var b actBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&b); err != nil {
		return nil, fmt.Errorf("reading the request: %w", err)
	}
	s := a.s
	target := it.MainFile(s)
	switch r.PathValue("action") {
	case "save":
		// The founder's edit. Written like any editor save, then recorded at once, so no scan has to find it.
		cur, _ := os.ReadFile(target)
		if b.Base != "" && b.Base != hashOf(cur) {
			return nil, httpErr{http.StatusConflict, "the draft changed since you opened it (a new proposal, or a save elsewhere); reload to see it"}
		}
		e, wrote, err := s.RecordVersion(VersionInput{Item: it.ID, Path: it.File, Content: []byte(b.Text), Actor: "user", Via: "ui", Note: b.Note})
		if err != nil {
			return nil, err
		}
		if err := writeAtomic(target, []byte(b.Text)); err != nil {
			return nil, err
		}
		return map[string]any{"event": e.ID, "saved": wrote, "hash": hashOf([]byte(b.Text))}, nil

	case "final":
		// Without pasted text, the working copy as it is now: what the founder confirmed on the page.
		content, via := []byte(b.Text), "paste"
		if !b.Paste {
			if content, err = os.ReadFile(target); err != nil {
				return nil, fmt.Errorf("no working copy to confirm: %w", err)
			}
			via = "workcopy"
		}
		e, _, err := s.RecordVersion(VersionInput{Item: it.ID, Path: it.File, Content: content, Actor: "user", Via: via, Stage: "final", Note: b.Note})
		if err != nil {
			return nil, err
		}
		if b.Paste {
			if err := writeAtomic(target, content); err != nil {
				return nil, err
			}
		}
		return map[string]any{"event": e.ID}, nil

	case "instruct":
		return a.append(it, "instruction", map[string]any{"text": need(b.Text)})
	case "note":
		return a.append(it, "note", map[string]any{"text": need(b.Text)})
	case "status":
		data := map[string]any{"status": need(b.Status)}
		for k, v := range map[string]string{"channel": b.Channel, "to": b.To, "note": b.Note} {
			if v != "" {
				data[k] = v
			}
		}
		return a.append(it, "status", data)
	case "outcome":
		data := map[string]any{"result": need(b.Result)}
		if b.Note != "" {
			data["note"] = b.Note
		}
		if strings.TrimSpace(b.Reply) != "" {
			h, err := s.PutBlob([]byte(b.Reply))
			if err != nil {
				return nil, err
			}
			data["blob"], data["bytes"] = h, len(b.Reply)
		}
		return a.append(it, "outcome", data)
	case "annotate":
		// Feedback on a version (or the item): labels such as a rating and what was wrong, kept apart from the facts.
		t := b.Target
		if t == "" {
			t = it.ID
		}
		data := map[string]any{"target": t, "labels": b.Labels}
		if b.Note != "" {
			data["note"] = b.Note
		}
		return a.append(it, "annotation", data)
	}
	return nil, httpErr{http.StatusNotFound, "no action " + r.PathValue("action")}
}

// need marks a required field; an empty one is caught by append.
func need(v string) string { return strings.TrimSpace(v) }

func (a *api) append(it *Item, typ string, data map[string]any) (any, error) {
	for _, k := range []string{"text", "status", "result"} {
		if v, ok := data[k]; ok && v == "" {
			return nil, fmt.Errorf("%s needs %s", typ, k)
		}
	}
	e, err := a.s.Append(Event{Type: typ, Actor: "user", Item: it.ID, Data: data})
	if err != nil {
		return nil, err
	}
	return map[string]any{"event": e.ID}, nil
}

// Pending lists instructions the assistant has not answered with a new version yet, oldest first: the assistant's queue.
func (s *Store) PrintPending(out io.Writer) error {
	events, err := s.Events()
	if err != nil {
		return err
	}
	type row struct {
		it *Item
		e  Event
	}
	var rows []row
	for _, it := range Fold(events) {
		if waiting(it) != "assistant" {
			continue
		}
		lastAsst := ""
		for _, e := range it.Events {
			if e.Type == "version" && e.Actor == "assistant" {
				lastAsst = e.TS
			}
		}
		for _, e := range it.OfType("instruction") {
			if e.TS > lastAsst {
				rows = append(rows, row{it, e})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].e.TS < rows[j].e.TS })
	if len(rows) == 0 {
		fmt.Fprintln(out, "no instructions waiting")
		return nil
	}
	for _, r := range rows {
		fmt.Fprintf(out, "%s  %s  [%s]\n  %s\n", local(r.e), r.it.ID, r.e.ID, r.e.Str("text"))
		// The founder's feedback labels on the proposal travel with the instruction.
		for _, an := range r.it.OfType("annotation") {
			if an.TS <= r.e.TS && an.TS > lastVersionBefore(r.it, r.e.TS) {
				lb, _ := json.Marshal(an.Data["labels"])
				fmt.Fprintf(out, "  feedback %s %s\n", lb, an.Str("note"))
			}
		}
		fmt.Fprintf(out, "  working copy: %s\n", filepath.Join(r.it.Dir(s), r.it.File))
	}
	return nil
}

func lastVersionBefore(it *Item, ts string) string {
	last := ""
	for _, v := range it.Versions {
		if v.Actor == "assistant" && v.TS < ts {
			last = v.TS
		}
	}
	return last
}
