// Command worklog keeps a local, append-only record of work done with an assistant: drafts proposed, the founder's edits,
// what was finally sent or decided, and what came of it. See DESIGN.md.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usage = `worklog: a local record of work done with an assistant (data in $WORKLOG_HOME, default ~/worklog)

Items and versions:
  new <kind> <title> [--id ID] [--task M1.1] [--org NAME] [--person P]... [--tag k=v]... [--file draft.md]
  path <item>                          print the working file to edit
  propose <item> (--file F | --stdin) [--note N] [--path P]
                                       the assistant's draft or revision; writes the working copy
  edit <item> (--file F | --stdin)     a version by the founder, given directly rather than saved in the working copy
  instruct <item> <text>               what the founder asked the assistant to change
  final <item> [--file F | --stdin] [--via paste] [--note N]
                                       the form actually sent or adopted (default: the working copy as it is now)
  status <item> <status> [--channel C] [--to T] [--note N]
                                       drafting, ready, sent, waiting, done, dropped, or any other word
  outcome <item> <result> [--note N] [--stdin]
                                       replied, positive, negative, meeting, no-reply, bounced, ...; --stdin for the reply text

The rest of the work:
  note <text> [--item ID]              an observation
  decide <question> --choice C [--options a,b] [--why W] [--evidence E]... [--item ID]
  message (--stdin | <text>) --role user|assistant [--item ID]
                                       a message from a conversation with any assistant
  annotate <event-or-item> <k=v>... [--note N]
                                       a judgement about something recorded, kept apart from the facts
  log <type> [--item ID] [--json '{...}'] [--stdin]
                                       any other event; --stdin stores the text as a blob

Capture:
  scan                                 record the founder's saves in the work folder now (every command does this first)
  watch [--every 15s]                  keep scanning; run it in a terminal or as a login item
  archive <name> <dir>                 compressed copy of a folder of files (for example an assistant's transcripts)

Reading:
  items [--all]    show <item>    diff <item> [--from ID] [--to ID]    today [--date YYYY-MM-DD]
  export pairs|events                  JSON lines on stdout
  pending                              instructions the assistant has not answered yet, with the feedback given

A page instead of commands:
  serve [--addr 127.0.0.1:7777]        read, edit, confirm and give feedback in a browser; loopback only
  reindex                              rebuild caches from the record

Every command takes --by user|assistant (who is acting; default user, propose defaults to assistant).
An assistant also sets WORKLOG_AGENT (its tool), WORKLOG_MODEL and WORKLOG_SESSION so its events say which.
`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "worklog:", err)
		os.Exit(1)
	}
}

// args holds positionals and --flags in any order. Repeated flags accumulate.
type args struct {
	pos   []string
	flags map[string][]string
}

var boolFlags = map[string]bool{"stdin": true, "all": true}

func parse(in []string) args {
	a := args{flags: map[string][]string{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if !strings.HasPrefix(s, "--") || s == "--" {
			a.pos = append(a.pos, s)
			continue
		}
		k := strings.TrimPrefix(s, "--")
		if eq := strings.Index(k, "="); eq >= 0 {
			a.flags[k[:eq]] = append(a.flags[k[:eq]], k[eq+1:])
			continue
		}
		if boolFlags[k] || i+1 >= len(in) {
			a.flags[k] = append(a.flags[k], "true")
			continue
		}
		a.flags[k] = append(a.flags[k], in[i+1])
		i++
	}
	return a
}

func (a args) get(k string) string {
	if v := a.flags[k]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func (a args) has(k string) bool { return len(a.flags[k]) > 0 }

func (a args) need(n int, what string) error {
	if len(a.pos) < n {
		return fmt.Errorf("usage: worklog %s", what)
	}
	return nil
}

// content reads --stdin or --file; ok is false when neither was given.
func (a args) content(stdin io.Reader) (b []byte, via string, ok bool, err error) {
	if a.has("stdin") {
		b, err = io.ReadAll(stdin)
		via = "paste"
		if v := a.get("via"); v != "" {
			via = v
		}
		return b, via, true, err
	}
	if f := a.get("file"); f != "" {
		b, err = os.ReadFile(f)
		via = "cli"
		if v := a.get("via"); v != "" {
			via = v
		}
		return b, via, true, err
	}
	return nil, "", false, nil
}

func (a args) by(def string) (string, error) {
	by := a.get("by")
	if by == "" {
		return def, nil
	}
	if by != "user" && by != "assistant" {
		return "", fmt.Errorf("--by is user or assistant, not %q", by)
	}
	return by, nil
}

func run(argv []string, stdin io.Reader, out io.Writer) error {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "-h" || argv[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, a := argv[0], parse(argv[1:])
	s, err := Open(DefaultHome())
	if err != nil {
		return err
	}
	// The founder's saves come first, so the record keeps the order things happened in.
	if cmd != "reindex" && cmd != "watch" && cmd != "serve" {
		if _, err := s.Scan(); err != nil {
			return fmt.Errorf("scanning the work folder: %w", err)
		}
	}
	switch cmd {
	case "new":
		if err := a.need(2, "new <kind> <title>"); err != nil {
			return err
		}
		by, err := a.by("user")
		if err != nil {
			return err
		}
		tags := map[string]string{}
		for _, t := range a.flags["tag"] {
			k, v, ok := strings.Cut(t, "=")
			if !ok {
				return fmt.Errorf("--tag wants key=value, got %q", t)
			}
			tags[k] = v
		}
		it, err := s.NewItem(a.get("id"), a.pos[0], strings.Join(a.pos[1:], " "), a.get("task"), a.get("org"), a.get("file"), a.flags["person"], tags, by)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\n%s\n", it.ID, it.MainFile(s))
		return nil

	case "path":
		if err := a.need(1, "path <item>"); err != nil {
			return err
		}
		it, err := s.Item(a.pos[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(out, it.MainFile(s))
		return nil

	case "propose", "edit", "final":
		if err := a.need(1, cmd+" <item>"); err != nil {
			return err
		}
		def := "user"
		if cmd == "propose" {
			def = "assistant"
		}
		by, err := a.by(def)
		if err != nil {
			return err
		}
		it, err := s.Item(a.pos[0])
		if err != nil {
			return err
		}
		rel := a.get("path")
		if rel == "" {
			rel = it.File
		}
		b, via, given, err := a.content(stdin)
		if err != nil {
			return err
		}
		target := filepath.Join(it.Dir(s), rel)
		if !given {
			if cmd != "final" {
				return fmt.Errorf("%s needs --file or --stdin", cmd)
			}
			if b, err = os.ReadFile(target); err != nil {
				return fmt.Errorf("no working copy to finalize: %w", err)
			}
			via = "workcopy"
		}
		stage := ""
		if cmd == "final" {
			stage = "final"
		}
		// Record before writing the working copy, so a scan never mistakes this write for the founder's save.
		e, wrote, err := s.RecordVersion(VersionInput{Item: it.ID, Path: rel, Content: b, Actor: by, Via: via, Stage: stage, Note: a.get("note")})
		if err != nil {
			return err
		}
		if given {
			if err := writeAtomic(target, b); err != nil {
				return err
			}
		}
		if !wrote {
			fmt.Fprintf(out, "unchanged: %s is already this text\n", target)
			return nil
		}
		fmt.Fprintf(out, "%s %s %s (%d bytes)\n%s\n", e.ID, e.Str("stage"), it.ID, len(b), target)
		return nil

	case "instruct":
		if err := a.need(2, "instruct <item> <text>"); err != nil {
			return err
		}
		return s.simple(out, a, "instruction", "user", a.pos[0], map[string]any{"text": strings.Join(a.pos[1:], " ")})

	case "status":
		if err := a.need(2, "status <item> <status>"); err != nil {
			return err
		}
		data := map[string]any{"status": a.pos[1]}
		for _, k := range []string{"channel", "to", "note", "at"} {
			if v := a.get(k); v != "" {
				data[k] = v
			}
		}
		return s.simple(out, a, "status", "user", a.pos[0], data)

	case "outcome":
		if err := a.need(2, "outcome <item> <result>"); err != nil {
			return err
		}
		data := map[string]any{"result": a.pos[1]}
		if v := a.get("note"); v != "" {
			data["note"] = v
		}
		if a.has("stdin") {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			h, err := s.PutBlob(b)
			if err != nil {
				return err
			}
			data["blob"], data["bytes"] = h, len(b)
		}
		return s.simple(out, a, "outcome", "user", a.pos[0], data)

	case "note":
		if err := a.need(1, "note <text>"); err != nil {
			return err
		}
		return s.simple(out, a, "note", "user", a.get("item"), map[string]any{"text": strings.Join(a.pos, " ")})

	case "decide":
		if err := a.need(1, "decide <question> --choice C"); err != nil {
			return err
		}
		if a.get("choice") == "" {
			return errors.New("decide needs --choice")
		}
		data := map[string]any{"question": strings.Join(a.pos, " "), "choice": a.get("choice")}
		if v := a.get("options"); v != "" {
			data["options"] = splitList(v)
		}
		if v := a.get("why"); v != "" {
			data["why"] = v
		}
		if v := a.flags["evidence"]; len(v) > 0 {
			data["evidence"] = v
		}
		return s.simple(out, a, "decision", "user", a.get("item"), data)

	case "message":
		role := a.get("role")
		if role != "user" && role != "assistant" {
			return errors.New("message needs --role user or --role assistant")
		}
		text := strings.Join(a.pos, " ")
		if a.has("stdin") {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			text = string(b)
		}
		data := map[string]any{"role": role}
		if len(text) > 4000 {
			h, err := s.PutBlob([]byte(text))
			if err != nil {
				return err
			}
			data["blob"], data["bytes"], data["preview"] = h, len(text), text[:400]
		} else {
			data["text"] = text
		}
		if a.get("by") == "" {
			a.flags["by"] = []string{role}
		}
		return s.simple(out, a, "message", role, a.get("item"), data)

	case "annotate":
		if err := a.need(2, "annotate <event-or-item> <k=v>..."); err != nil {
			return err
		}
		labels := map[string]any{}
		for _, kv := range a.pos[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("labels are key=value, got %q", kv)
			}
			labels[k] = v
		}
		data := map[string]any{"target": a.pos[0], "labels": labels}
		if v := a.get("note"); v != "" {
			data["note"] = v
		}
		item := ""
		if !strings.HasPrefix(a.pos[0], "e_") {
			item = a.pos[0]
		}
		return s.simple(out, a, "annotation", "user", item, data)

	case "log":
		if err := a.need(1, "log <type>"); err != nil {
			return err
		}
		data := map[string]any{}
		if j := a.get("json"); j != "" {
			if err := json.Unmarshal([]byte(j), &data); err != nil {
				return fmt.Errorf("--json: %w", err)
			}
		}
		if a.has("stdin") {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			h, err := s.PutBlob(b)
			if err != nil {
				return err
			}
			data["blob"], data["bytes"] = h, len(b)
		}
		return s.simple(out, a, a.pos[0], "user", a.get("item"), data)

	case "scan":
		return nil // done above

	case "watch":
		every, err := time.ParseDuration(orDefault(a.get("every"), "15s"))
		if err != nil {
			return err
		}
		return s.Watch(every, out)

	case "archive":
		if err := a.need(2, "archive <name> <dir>"); err != nil {
			return err
		}
		return s.Archive(a.pos[0], a.pos[1], out)

	case "items":
		return s.PrintItems(out, a.has("all"))
	case "show":
		if err := a.need(1, "show <item>"); err != nil {
			return err
		}
		return s.PrintItem(out, a.pos[0])
	case "diff":
		if err := a.need(1, "diff <item>"); err != nil {
			return err
		}
		return s.PrintDiff(out, a.pos[0], a.get("from"), a.get("to"))
	case "today":
		day := a.get("date")
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		return s.PrintDay(out, day)
	case "export":
		if err := a.need(1, "export pairs|events"); err != nil {
			return err
		}
		return s.Export(out, a.pos[0])
	case "reindex":
		_, err := s.Reindex()
		return err
	case "pending":
		return s.PrintPending(out)
	case "serve":
		return s.Serve(orDefault(a.get("addr"), "127.0.0.1:7777"), out)
	}
	return fmt.Errorf("unknown command %q (worklog help)", cmd)
}

// simple appends one event and prints its id.
func (s *Store) simple(out io.Writer, a args, typ, def, item string, data map[string]any) error {
	by, err := a.by(def)
	if err != nil {
		return err
	}
	if item != "" && typ != "annotation" {
		if _, err := s.Item(item); err != nil {
			return err
		}
	}
	e, err := s.Append(Event{Type: typ, Actor: by, Item: item, Data: data})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, e.ID)
	return nil
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func writeAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(p), "."+filepath.Base(p)+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
