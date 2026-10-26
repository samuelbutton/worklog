package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
)

func local(e Event) string { return e.Time().Local().Format("Jan 02 15:04") }

// summary is one line describing an event, for timelines.
func (s *Store) summary(e Event) string {
	switch e.Type {
	case "item":
		return fmt.Sprintf("new %s: %s", e.Str("kind"), e.Str("title"))
	case "version":
		if e.Data["deleted"] == true {
			return fmt.Sprintf("%s deleted", e.Str("path"))
		}
		s := fmt.Sprintf("%s %s (%s, %s bytes)", e.Str("stage"), e.Str("path"), e.Str("via"), e.Str("bytes"))
		if n := e.Str("note"); n != "" {
			s += ": " + n
		}
		return s
	case "instruction", "note":
		return e.Type + ": " + clip(e.Str("text"), 100)
	case "status":
		s := "status " + e.Str("status")
		for _, k := range []string{"channel", "to", "note"} {
			if v := e.Str(k); v != "" {
				s += " · " + v
			}
		}
		return s
	case "outcome":
		return strings.TrimSpace("outcome " + e.Str("result") + " " + e.Str("note"))
	case "decision":
		return fmt.Sprintf("decided %q → %s", clip(e.Str("question"), 60), e.Str("choice"))
	case "message":
		t := e.Str("text")
		if t == "" {
			t = e.Str("preview")
		}
		return e.Str("role") + " said: " + clip(t, 100)
	case "annotation":
		b, _ := json.Marshal(e.Data["labels"])
		return "annotated " + e.Str("target") + " " + string(b)
	}
	b, _ := json.Marshal(e.Data)
	return e.Type + " " + clip(string(b), 100)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (s *Store) PrintItems(out io.Writer, all bool) error {
	events, err := s.Events()
	if err != nil {
		return err
	}
	var items []*Item
	for _, it := range Fold(events) {
		if all || (it.Status != "done" && it.Status != "dropped") {
			items = append(items, it)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Events[len(items[i].Events)-1].TS > items[j].Events[len(items[j].Events)-1].TS
	})
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ITEM\tKIND\tSTATUS\tVERSIONS\tLAST\tTITLE")
	for _, it := range items {
		asst, user := 0, 0
		for _, v := range it.Versions {
			if v.Actor == "assistant" {
				asst++
			} else {
				user++
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d asst, %d you\t%s\t%s\n", it.ID, it.Kind, it.Status, asst, user, local(it.Events[len(it.Events)-1]), it.Title)
	}
	return w.Flush()
}

func (s *Store) PrintItem(out io.Writer, id string) error {
	it, err := s.Item(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s  (%s, %s)\n%s\n", it.ID, it.Kind, it.Status, it.Title)
	if it.Task != "" || it.Org != "" || len(it.People) > 0 {
		fmt.Fprintf(out, "task %s · org %s · people %s\n", orDefault(it.Task, "-"), orDefault(it.Org, "-"), orDefault(strings.Join(it.People, "; "), "-"))
	}
	fmt.Fprintf(out, "working copy: %s\n\n", it.MainFile(s))
	for _, e := range it.Events {
		fmt.Fprintf(out, "%s  %-9s %s  [%s]\n", local(e), e.Actor, s.summary(e), e.ID)
	}
	if p := s.pair(it); p != nil && p.Final != "" {
		st := p.Edit
		fmt.Fprintf(out, "\nfrom first proposal to final: %d words removed, %d added, similarity %.2f\n", st.Removed, st.Added, st.Similarity)
	}
	return nil
}

// PrintDiff shows what changed between two versions; by default the assistant's last proposal against the latest version.
func (s *Store) PrintDiff(out io.Writer, id, from, to string) error {
	it, err := s.Item(id)
	if err != nil {
		return err
	}
	byID := map[string]*Event{}
	for i := range it.Versions {
		byID[it.Versions[i].ID] = &it.Versions[i]
	}
	a, b := byID[from], byID[to]
	if from == "" {
		a = it.LastBy("assistant")
	}
	if to == "" {
		b = it.LastBy("")
	}
	if a == nil || b == nil {
		return fmt.Errorf("%s has no versions to compare (needs a proposal and a later version)", id)
	}
	if a.ID == b.ID {
		fmt.Fprintln(out, "no change since the assistant's last proposal")
		return nil
	}
	before, err := s.Blob(a.Str("blob"))
	if err != nil {
		return err
	}
	after, err := s.Blob(b.Str("blob"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "--- %s %s %s (%s)\n+++ %s %s %s (%s)\n", a.Actor, a.Str("stage"), local(*a), a.ID, b.Actor, b.Str("stage"), local(*b), b.ID)
	writeDiff(out, string(before), string(after))
	st := editStats(string(before), string(after))
	fmt.Fprintf(out, "\n%d words removed, %d added, similarity %.2f\n", st.Removed, st.Added, st.Similarity)
	return nil
}

// PrintDay is the day as a timeline, in local time.
func (s *Store) PrintDay(out io.Writer, day string) error {
	events, err := s.Events()
	if err != nil {
		return err
	}
	n := 0
	for _, e := range events {
		if e.Time().Local().Format("2006-01-02") != day {
			continue
		}
		n++
		item := e.Item
		if item == "" {
			item = "-"
		}
		fmt.Fprintf(out, "%s  %-9s %-28s %s\n", e.Time().Local().Format("15:04"), e.Actor, clip(item, 28), s.summary(e))
	}
	if n == 0 {
		fmt.Fprintf(out, "nothing recorded on %s\n", day)
	}
	return nil
}

// Pair is one item's proposal and what it became: the unit of the edit data.
type Pair struct {
	Item         string            `json:"item"`
	Kind         string            `json:"kind"`
	Title        string            `json:"title"`
	Task         string            `json:"task,omitempty"`
	Org          string            `json:"org,omitempty"`
	People       []string          `json:"people,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Status       string            `json:"status"`
	Proposed     string            `json:"proposed"`
	ProposedBy   map[string]string `json:"proposed_by,omitempty"`
	LastProposed string            `json:"last_proposed"`
	Final        string            `json:"final,omitempty"`
	FinalVia     string            `json:"final_via,omitempty"`
	Instructions []string          `json:"instructions,omitempty"`
	Versions     int               `json:"versions"`
	Edit         EditStats         `json:"edit"`
	Outcomes     []map[string]any  `json:"outcomes,omitempty"`
	Annotations  []map[string]any  `json:"annotations,omitempty"`
	Started      string            `json:"started"`
	Finished     string            `json:"finished,omitempty"`
}

func (s *Store) pair(it *Item) *Pair {
	first, last := it.FirstBy("assistant"), it.LastBy("assistant")
	if first == nil {
		return nil
	}
	text := func(e *Event) string {
		if e == nil {
			return ""
		}
		b, _ := s.Blob(e.Str("blob"))
		return string(b)
	}
	p := &Pair{Item: it.ID, Kind: it.Kind, Title: it.Title, Task: it.Task, Org: it.Org, People: it.People, Status: it.Status,
		Proposed: text(first), LastProposed: text(last), Versions: len(it.Versions), Started: first.TS}
	if len(it.Tags) > 0 {
		p.Tags = it.Tags
	}
	if a := first.Str("agent"); a != "" {
		p.ProposedBy = map[string]string{"agent": a, "model": first.Str("model")}
	}
	if f := it.Final(); f != nil {
		p.Final, p.FinalVia, p.Finished = text(f), f.Str("via"), f.TS
	} else if u := it.LastBy(""); u != nil && u.Actor == "user" {
		p.Final = text(u) // not finalized yet: the founder's latest edit
	}
	if p.Final != "" {
		p.Edit = editStats(p.Proposed, p.Final)
	}
	for _, e := range it.Events {
		switch e.Type {
		case "instruction":
			p.Instructions = append(p.Instructions, e.Str("text"))
		case "outcome":
			o := map[string]any{"result": e.Str("result"), "at": e.TS}
			if n := e.Str("note"); n != "" {
				o["note"] = n
			}
			if h := e.Str("blob"); h != "" {
				b, _ := s.Blob(h)
				o["text"] = string(b)
			}
			p.Outcomes = append(p.Outcomes, o)
		case "annotation":
			p.Annotations = append(p.Annotations, map[string]any{"labels": e.Data["labels"], "by": e.Actor, "at": e.TS})
		}
	}
	return p
}

func (s *Store) Export(out io.Writer, what string) error {
	events, err := s.Events()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	switch what {
	case "events":
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	case "pairs":
		items := Fold(events)
		ids := make([]string, 0, len(items))
		for id := range items {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if p := s.pair(items[id]); p != nil {
				if err := enc.Encode(p); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return fmt.Errorf("export pairs or events, not %q", what)
}
