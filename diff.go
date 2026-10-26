package main

import (
	"fmt"
	"io"
	"strings"
)

// lcs returns the longest common subsequence table walk of a and b as aligned operations: ' ' kept, '-' only in a, '+' only in b.
func lcs(a, b []string) []op {
	n, m := len(a), len(b)
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

type op struct {
	kind byte
	text string
}

// EditStats measures how much the founder changed: words kept, removed and added, and similarity (1 is untouched).
type EditStats struct {
	WordsBefore int     `json:"words_before"`
	WordsAfter  int     `json:"words_after"`
	Removed     int     `json:"words_removed"`
	Added       int     `json:"words_added"`
	Similarity  float64 `json:"similarity"`
}

func editStats(before, after string) EditStats {
	a, b := strings.Fields(before), strings.Fields(after)
	st := EditStats{WordsBefore: len(a), WordsAfter: len(b)}
	if len(a)*len(b) > 25_000_000 { // too long to align word by word; compare lines instead
		a, b = strings.Split(before, "\n"), strings.Split(after, "\n")
	}
	kept := 0
	for _, o := range lcs(a, b) {
		switch o.kind {
		case ' ':
			kept++
		case '-':
			st.Removed++
		case '+':
			st.Added++
		}
	}
	if len(a)+len(b) == 0 {
		st.Similarity = 1
	} else {
		st.Similarity = float64(2*kept) / float64(len(a)+len(b))
	}
	return st
}

// writeDiff prints a line diff with three lines of context around each change.
func writeDiff(w io.Writer, before, after string) {
	ops := lcs(strings.Split(before, "\n"), strings.Split(after, "\n"))
	show := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind != ' ' {
			for k := max(0, i-3); k <= min(len(ops)-1, i+3); k++ {
				show[k] = true
			}
		}
	}
	gap := false
	for i, o := range ops {
		if !show[i] {
			gap = true
			continue
		}
		if gap {
			fmt.Fprintln(w, "  …")
			gap = false
		}
		fmt.Fprintf(w, "%c %s\n", o.kind, o.text)
	}
}
