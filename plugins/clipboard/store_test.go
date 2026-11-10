package main

import (
	"strconv"
	"testing"
)

func texts(items []item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

func equalTexts(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStoreAddIgnoresEmptyAndWhitespace(t *testing.T) {
	s := newStore()
	s.Add("")
	s.Add("   ")
	s.Add("\t\n")
	if got := s.Snapshot(""); len(got) != 0 {
		t.Fatalf("Snapshot = %v, want empty", got)
	}
}

func TestStoreAddIgnoresConsecutiveDuplicate(t *testing.T) {
	s := newStore()
	s.Add("a")
	first := s.Snapshot("")[0].At
	s.Add("a")
	s.Add("  a  ") // 修剪后与队首相同，同样忽略。
	s.Add("b")
	s.Add("b")
	snapshot := s.Snapshot("")
	if got, want := texts(snapshot), []string{"b", "a"}; !equalTexts(got, want) {
		t.Fatalf("Snapshot = %v, want %v", got, want)
	}
	for _, it := range snapshot {
		if it.Text == "a" && !it.At.Equal(first) {
			t.Fatalf("duplicate Add refreshed the oldest entry's time: %v", it.At)
		}
	}
}

func TestStoreAddMovesNonConsecutiveDuplicateToFront(t *testing.T) {
	s := newStore()
	s.Add("a")
	s.Add("b")
	s.Add("c")
	s.Add("a")
	snapshot := s.Snapshot("")
	if got, want := texts(snapshot), []string{"a", "c", "b"}; !equalTexts(got, want) {
		t.Fatalf("Snapshot = %v, want %v", got, want)
	}
	if snapshot[0].At.Before(snapshot[1].At) {
		t.Fatalf("moved item time %v not newer than next %v", snapshot[0].At, snapshot[1].At)
	}
}

func TestStoreAddDropsOldestBeyondLimit(t *testing.T) {
	s := newStore()
	for i := range maxItems + 5 {
		s.Add(strconv.Itoa(i))
	}
	snapshot := s.Snapshot("")
	if len(snapshot) != maxItems {
		t.Fatalf("len(Snapshot) = %d, want %d", len(snapshot), maxItems)
	}
	want := make([]string, 0, maxItems)
	for i := maxItems + 4; i >= 5; i-- {
		want = append(want, strconv.Itoa(i))
	}
	if got := texts(snapshot); !equalTexts(got, want) {
		t.Fatalf("Snapshot = %v, want %v", got, want)
	}
}

func TestStoreSnapshotFiltersCaseInsensitive(t *testing.T) {
	s := newStore()
	for _, text := range []string{"apple", "Banana", "cherry pie", "APPLE pie"} {
		s.Add(text)
	}
	got := texts(s.Snapshot("pie"))
	want := []string{"APPLE pie", "cherry pie"}
	if !equalTexts(got, want) {
		t.Fatalf("Snapshot(pie) = %v, want %v", got, want)
	}
	got = texts(s.Snapshot("APPLE"))
	want = []string{"APPLE pie", "apple"}
	if !equalTexts(got, want) {
		t.Fatalf("Snapshot(APPLE) = %v, want %v", got, want)
	}
	if got := s.Snapshot("zzz"); len(got) != 0 {
		t.Fatalf("Snapshot(zzz) = %v, want empty", got)
	}
	if got := texts(s.Snapshot("")); len(got) != 4 || got[0] != "APPLE pie" {
		t.Fatalf("Snapshot empty filter = %v, want newest first", got)
	}
}

func TestStoreSnapshotReturnsCopy(t *testing.T) {
	s := newStore()
	s.Add("a")
	s.Add("b")
	snapshot := s.Snapshot("")
	snapshot[0].Text = "mutated"
	snapshot[1].At = snapshot[1].At.Add(1e9)
	fresh := s.Snapshot("")
	if got, want := texts(fresh), []string{"b", "a"}; !equalTexts(got, want) {
		t.Fatalf("Snapshot mutation leaked: %v, want %v", got, want)
	}
}
