package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("WORKLOG_HOME", home)
	t.Setenv("WORKLOG_AGENT", "test-agent")
	t.Setenv("WORKLOG_MODEL", "test-model")
	t.Setenv("WORKLOG_SESSION", "s1")
	return home
}

func wl(t *testing.T, stdin string, argv ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(argv, strings.NewReader(stdin), &out); err != nil {
		t.Fatalf("worklog %v: %v", argv, err)
	}
	return out.String()
}

func TestADraftFromProposalToReply(t *testing.T) {
	home := setup(t)
	out := wl(t, "", "new", "email", "Opener: Acme", "--task", "M1.1", "--org", "Acme", "--person", "Jane Doe <jane@acme.com>", "--tag", "segment=sales")
	id := strings.SplitN(out, "\n", 2)[0]
	if id != "email-opener-acme" {
		t.Fatalf("id = %q", id)
	}
	work := filepath.Join(home, "work", id, "draft.md")

	wl(t, "Hi Jane,\n\nHow do you test a new release of your agent?\n\nSam\n", "propose", id, "--stdin")
	if b, _ := os.ReadFile(work); !strings.Contains(string(b), "new release") {
		t.Fatalf("propose did not write the working copy: %q", b)
	}
	// The founder edits in an editor, then asks for a change; the edit must be recorded before the revision.
	os.WriteFile(work, []byte("Hi Jane,\n\nHow does Acme test a new release of its agent?\n\nSam\n"), 0o600)
	wl(t, "", "instruct", id, "add the offer to review their agent")
	wl(t, "Hi Jane,\n\nHow does Acme test a new release of its agent? Happy to review yours.\n\nSam\n", "propose", id, "--stdin")
	os.WriteFile(work, []byte("Hi Jane,\n\nHow does Acme test a new release of its agent? I'd be glad to review yours, free.\n\nSam\n"), 0o600)
	wl(t, "", "final", id)
	wl(t, "", "status", id, "sent", "--channel", "gmail", "--to", "jane@acme.com")
	wl(t, "Sure, let's talk Thursday.", "outcome", id, "positive", "--stdin")

	it, err := mustStore(t).Item(id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range it.Versions {
		got = append(got, v.Actor+":"+v.Str("stage")+":"+v.Str("via"))
	}
	want := "assistant:proposed:paste user:edited:scan assistant:revised:paste user:edited:scan user:final:workcopy"
	if strings.Join(got, " ") != want {
		t.Fatalf("versions\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if it.Status != "sent" {
		t.Fatalf("status = %s", it.Status)
	}
	if it.Versions[0].Str("agent") != "test-agent" || it.Versions[0].Session != "s1" {
		t.Fatalf("assistant version lacks agent or session: %+v", it.Versions[0])
	}
	if it.Versions[1].Str("parent") != it.Versions[0].ID {
		t.Fatalf("edit's parent is not the proposal")
	}

	var p Pair
	if err := json.Unmarshal([]byte(wl(t, "", "export", "pairs")), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Proposed, "How do you test") || !strings.Contains(p.Final, "free.") || p.FinalVia != "workcopy" {
		t.Fatalf("pair: %+v", p)
	}
	if len(p.Instructions) != 1 || len(p.Outcomes) != 1 || p.Outcomes[0]["text"] != "Sure, let's talk Thursday." {
		t.Fatalf("pair instructions/outcomes: %+v", p)
	}
	if p.Edit.Similarity <= 0.3 || p.Edit.Similarity >= 1 || p.Edit.Added == 0 {
		t.Fatalf("edit stats: %+v", p.Edit)
	}
	if d := wl(t, "", "diff", id); !strings.Contains(d, "+ How does Acme test a new release of its agent? I'd be glad") {
		t.Fatalf("diff:\n%s", d)
	}
	for _, cmd := range [][]string{{"show", id}, {"items"}, {"today"}} {
		if out := wl(t, "", cmd...); !strings.Contains(out, id) {
			t.Fatalf("%v does not mention the item:\n%s", cmd, out)
		}
	}
}

func TestAProposalOverwritingAnUnrecordedSaveKeepsTheSave(t *testing.T) {
	home := setup(t)
	wl(t, "", "new", "doc", "One pager", "--id", "doc-1")
	wl(t, "v1", "propose", "doc-1", "--stdin")
	os.WriteFile(filepath.Join(home, "work", "doc-1", "draft.md"), []byte("v1 edited by hand"), 0o600)
	wl(t, "v2", "propose", "doc-1", "--stdin") // scans first, so the hand edit is kept
	it, _ := mustStore(t).Item("doc-1")
	if len(it.Versions) != 3 || it.Versions[1].Actor != "user" {
		t.Fatalf("the hand edit was lost: %+v", it.Versions)
	}
	b, _ := mustStore(t).Blob(it.Versions[1].Str("blob"))
	if string(b) != "v1 edited by hand" {
		t.Fatalf("blob = %q", b)
	}
}

func TestScanRecordsNewFilesAndDeletionsOnce(t *testing.T) {
	home := setup(t)
	wl(t, "", "new", "call", "Acme call", "--id", "call-acme")
	notes := filepath.Join(home, "work", "call-acme", "notes.md")
	os.WriteFile(notes, []byte("they test by hand"), 0o600)
	os.WriteFile(filepath.Join(home, "work", "call-acme", ".draft.md.swp"), []byte("x"), 0o600)
	wl(t, "", "scan")
	wl(t, "", "scan")
	os.Remove(notes)
	wl(t, "", "scan")
	wl(t, "", "scan")
	it, _ := mustStore(t).Item("call-acme")
	if len(it.Versions) != 2 || it.Versions[0].Str("path") != "notes.md" || it.Versions[1].Data["deleted"] != true {
		t.Fatalf("versions: %+v", it.Versions)
	}
	// The index is only a cache: without it the same state is rebuilt from the record.
	os.Remove(filepath.Join(home, "index", "files.json"))
	wl(t, "", "scan")
	it, _ = mustStore(t).Item("call-acme")
	if len(it.Versions) != 2 {
		t.Fatalf("a rebuilt index recorded again: %d versions", len(it.Versions))
	}
}

func TestConcurrentWritersNeverInterleave(t *testing.T) {
	setup(t)
	s := mustStore(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := s.Append(Event{Type: "note", Data: map[string]any{"text": strings.Repeat("x", 3000)}}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	events, err := s.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 400 {
		t.Fatalf("%d events, want 400", len(events))
	}
}

func TestEventsAndOptionsAreRecordedAsGiven(t *testing.T) {
	setup(t)
	wl(t, "", "decide", "Which segment first?", "--choice", "sales", "--options", "sales, front office", "--why", "more replies", "--evidence", "friday review")
	wl(t, "", "note", "openers that name a failure get read", "--by", "assistant")
	wl(t, "", "message", "--role", "user", "draft the Acme opener")
	wl(t, "", "annotate", "e_x", "edit=tone", "--note", "softer")
	wl(t, "", "log", "call-booked", "--json", `{"when":"2026-10-29T15:00"}`)
	events, _ := mustStore(t).Events()
	types := []string{}
	for _, e := range events {
		types = append(types, e.Type+":"+e.Actor)
	}
	if got := strings.Join(types, " "); got != "decision:user note:assistant message:user annotation:user call-booked:user" {
		t.Fatalf("got %s", got)
	}
	if opts := strs(events[0].Data["options"]); len(opts) != 2 || opts[1] != "front office" {
		t.Fatalf("options: %v", opts)
	}
	if events[1].Str("agent") != "test-agent" {
		t.Fatalf("assistant note lacks its agent")
	}
	var out bytes.Buffer
	if err := run([]string{"note", "x", "--by", "claude"}, strings.NewReader(""), &out); err == nil {
		t.Fatalf("--by accepted a provider name; it is user or assistant")
	}
}

func TestArchiveCopiesChangesAndNeverLosesALargerCopy(t *testing.T) {
	setup(t)
	src := t.TempDir()
	f := filepath.Join(src, "sub", "t.jsonl")
	os.MkdirAll(filepath.Dir(f), 0o700)
	os.WriteFile(f, []byte(strings.Repeat("line\n", 1000)), 0o600)
	s := mustStore(t)
	if err := s.Archive("tool", src, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s.Archive("tool", src, &out)
	if !strings.Contains(out.String(), "0 files copied") {
		t.Fatalf("unchanged file copied again: %s", out.String())
	}
	os.WriteFile(f, []byte("short\n"), 0o600)
	os.Chtimes(f, s.now().Add(1e9), s.now().Add(1e9))
	s.Archive("tool", src, io.Discard)
	matches, _ := filepath.Glob(filepath.Join(s.Home, "archive", "tool", "sub", "t.jsonl*.gz"))
	if len(matches) != 2 {
		t.Fatalf("archive copies: %v", matches)
	}
	r, _ := os.Open(filepath.Join(s.Home, "archive", "tool", "sub", "t.jsonl.gz"))
	zr, _ := gzip.NewReader(r)
	b, _ := io.ReadAll(zr)
	if string(b) != "short\n" {
		t.Fatalf("current copy = %q", b)
	}
}

func mustStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(DefaultHome())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStrReadsNumbersBeforeAndAfterARoundTrip(t *testing.T) {
	e := Event{Data: map[string]any{"bytes": 12, "f": 1.5}}
	if e.Str("bytes") != "12" || e.Str("f") != "1.5" {
		t.Fatalf("got %q %q", e.Str("bytes"), e.Str("f"))
	}
}
