// Copyright IBM Corp. 2015, 2025
// SPDX-License-Identifier: MPL-2.0

package iradix

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
)

func collectDiff[T any](t *testing.T, old, new *Tree[T]) []DiffEntry[T] {
	t.Helper()
	var out []DiffEntry[T]
	complete := old.Diff(new, func(e DiffEntry[T]) bool {
		out = append(out, e)
		return true
	})
	if !complete {
		t.Fatal("Diff reported an early stop but the callback never returned false")
	}
	return out
}

func diffKeys[T any](entries []DiffEntry[T]) []string {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = string(e.Key)
	}
	return keys
}

func TestDiffKindString(t *testing.T) {
	cases := map[DiffKind]string{
		DiffAdded:   "added",
		DiffRemoved: "removed",
		DiffUpdated: "updated",
		DiffKind(9): "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Fatalf("DiffKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestDiffEmptyTrees(t *testing.T) {
	t.Run("both empty", func(t *testing.T) {
		if got := collectDiff(t, New[int](), New[int]()); len(got) != 0 {
			t.Fatalf("expected no entries, got %v", got)
		}
	})

	t.Run("same tree", func(t *testing.T) {
		tree := New[int]()
		tree, _, _ = tree.Insert([]byte("a"), 1)
		if got := collectDiff(t, tree, tree); len(got) != 0 {
			t.Fatalf("expected no entries for identical trees, got %v", got)
		}
	})

	t.Run("empty to populated", func(t *testing.T) {
		old := New[int]()
		new, _, _ := old.Insert([]byte("a"), 1)
		new, _, _ = new.Insert([]byte("b"), 2)

		got := collectDiff(t, old, new)
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %v", got)
		}
		for i, want := range []string{"a", "b"} {
			if got[i].Kind != DiffAdded || string(got[i].Key) != want {
				t.Fatalf("entry %d = %#v, want added %q", i, got[i], want)
			}
			if got[i].New != i+1 {
				t.Fatalf("entry %d new value = %d, want %d", i, got[i].New, i+1)
			}
			if got[i].Old != 0 {
				t.Fatalf("entry %d old value = %d, want zero value", i, got[i].Old)
			}
		}
	})

	t.Run("populated to empty", func(t *testing.T) {
		old := New[int]()
		old, _, _ = old.Insert([]byte("a"), 1)
		old, _, _ = old.Insert([]byte("b"), 2)
		got := collectDiff(t, old, New[int]())
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %v", got)
		}
		for i, want := range []string{"a", "b"} {
			if got[i].Kind != DiffRemoved || string(got[i].Key) != want {
				t.Fatalf("entry %d = %#v, want removed %q", i, got[i], want)
			}
			if got[i].Old != i+1 {
				t.Fatalf("entry %d old value = %d, want %d", i, got[i].Old, i+1)
			}
			if got[i].New != 0 {
				t.Fatalf("entry %d new value = %d, want zero value", i, got[i].New)
			}
		}
	})

	t.Run("nil trees", func(t *testing.T) {
		var nilTree *Tree[int]
		populated, _, _ := New[int]().Insert([]byte("a"), 1)

		if got := collectDiff(t, nilTree, nilTree); len(got) != 0 {
			t.Fatalf("expected no entries, got %v", got)
		}
		got := collectDiff(t, nilTree, populated)
		if len(got) != 1 || got[0].Kind != DiffAdded || string(got[0].Key) != "a" {
			t.Fatalf("unexpected entries for nil -> populated: %v", got)
		}
		got = collectDiff(t, populated, nilTree)
		if len(got) != 1 || got[0].Kind != DiffRemoved || string(got[0].Key) != "a" {
			t.Fatalf("unexpected entries for populated -> nil: %v", got)
		}
	})
}

func TestDiffKindsAndStableOrder(t *testing.T) {
	old := New[int]()
	for _, kv := range []struct {
		k string
		v int
	}{{"a", 1}, {"b", 2}, {"c", 3}, {"foo", 4}, {"foobar", 5}, {"qux", 6}} {
		old, _, _ = old.Insert([]byte(kv.k), kv.v)
	}

	txn := old.Txn()
	txn.Delete([]byte("b"))
	txn.Insert([]byte("a"), 100)
	txn.Insert([]byte("c"), 3) // unchanged value, no update expected
	txn.Insert([]byte("bba"), 7)
	txn.Delete([]byte("foo"))
	txn.Insert([]byte("foobar"), 50)
	txn.Insert([]byte("zzz"), 9)
	new := txn.Commit()

	got := collectDiff(t, old, new)
	want := []struct {
		kind DiffKind
		key  string
		old  int
		new  int
	}{
		{DiffUpdated, "a", 1, 100},
		{DiffRemoved, "b", 2, 0},
		{DiffAdded, "bba", 0, 7},
		{DiffRemoved, "foo", 4, 0},
		{DiffUpdated, "foobar", 5, 50},
		{DiffAdded, "zzz", 0, 9},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d entries (%v), want %d", len(got), got, len(want))
	}
	for i, w := range want {
		e := got[i]
		if e.Kind != w.kind || string(e.Key) != w.key || e.Old != w.old || e.New != w.new {
			t.Fatalf("entry %d = %#v, want %#v", i, e, w)
		}
	}
}

func TestDiffPrefixInsertion(t *testing.T) {
	old := New[string]()
	for _, k := range []string{"foo", "foobar", "foobaz", "quux"} {
		old, _, _ = old.Insert([]byte(k), k)
	}

	new := old
	new, _, _ = new.Insert([]byte("foob"), "foob") // new key is a prefix of existing keys
	new, _, _ = new.Insert([]byte("fox"), "fox")   // new branch splits the "fo" subtree

	got := collectDiff(t, old, new)
	if keys := diffKeys(got); !sort.StringsAreSorted(keys) {
		t.Fatalf("entries are not sorted by key: %v", keys)
	}
	want := []string{"foob", "fox"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", diffKeys(got), want)
	}
	for i, key := range want {
		if got[i].Kind != DiffAdded || string(got[i].Key) != key {
			t.Fatalf("entry %d = %#v, want added %q", i, got[i], key)
		}
	}
	if got[0].New != "foob" || got[1].New != "fox" {
		t.Fatalf("values not preserved: %#v", got)
	}
}

func TestDiffPrefixRemoval(t *testing.T) {
	old := New[int]()
	for _, kv := range []struct {
		k string
		v int
	}{{"foo", 1}, {"foobar", 2}, {"foobaz", 3}, {"quux", 4}} {
		old, _, _ = old.Insert([]byte(kv.k), kv.v)
	}

	new := old
	new, _, _ = new.Delete([]byte("foo"))
	new, _, _ = new.Delete([]byte("foobaz"))

	got := collectDiff(t, old, new)
	want := []string{"foo", "foobaz"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", diffKeys(got), want)
	}
	for i, key := range want {
		if got[i].Kind != DiffRemoved || string(got[i].Key) != key {
			t.Fatalf("entry %d = %#v, want removed %q", i, got[i], key)
		}
	}
}

func TestDiffUniqueKeys(t *testing.T) {
	old := New[int]()
	for i := 0; i < 200; i++ {
		old, _, _ = old.Insert([]byte(fmt.Sprintf("key-%04d", i)), i)
	}
	new, _ := old.DeletePrefix([]byte("key-01"))
	new, _, _ = new.Insert([]byte("key-0100"), -1)
	new, _, _ = new.Insert([]byte("key-0199"), -1)

	got := collectDiff(t, old, new)
	seen := make(map[string]bool, len(got))
	prev := ""
	for _, e := range got {
		key := string(e.Key)
		if seen[key] {
			t.Fatalf("duplicate key %q in diff output", key)
		}
		if key <= prev && prev != "" {
			t.Fatalf("keys out of order: %q after %q", key, prev)
		}
		seen[key] = true
		prev = key
	}
	// Deleting "key-01" removes 100 keys; two of them are re-added with new
	// values, so they are reported once each as updates.
	if len(seen) != 100 {
		t.Fatalf("expected 100 changed keys (98 removed, 2 updated), got %d", len(seen))
	}
}

func TestDiffStructuralPruning(t *testing.T) {
	const n = 5000
	old := New[int]()
	for i := 0; i < n; i++ {
		old, _, _ = old.Insert([]byte(fmt.Sprintf("prefix/%05d", i)), i)
	}

	// Only a single key changes. The txn copies the nodes on that key's path
	// and shares every other subtree with old.
	txn := old.Txn()
	txn.Insert([]byte("prefix/02500"), -1)
	new := txn.Commit()

	equalCalls := 0
	var entries []DiffEntry[int]
	complete := old.DiffWith(new, func(a, b int) bool {
		equalCalls++
		return a == b
	}, func(e DiffEntry[int]) bool {
		entries = append(entries, e)
		return true
	})
	if !complete {
		t.Fatal("expected a complete comparison")
	}
	if len(entries) != 1 || entries[0].Kind != DiffUpdated || string(entries[0].Key) != "prefix/02500" {
		t.Fatalf("unexpected entries: %v", entries)
	}

	// The untouched subtree must be skipped by pointer identity, so equal is
	// only consulted for the one leaf on the changed path.
	if equalCalls > 2 {
		t.Fatalf("expected structural pruning to avoid comparing unchanged leaves, equal called %d times", equalCalls)
	}
}

func TestDiffRootSharedSkipsEverything(t *testing.T) {
	tree := New[int]()
	for i := 0; i < 1000; i++ {
		tree, _, _ = tree.Insert([]byte(fmt.Sprintf("k%04d", i)), i)
	}

	equalCalls := 0
	complete := tree.DiffWith(tree, func(a, b int) bool {
		equalCalls++
		return a == b
	}, func(DiffEntry[int]) bool {
		t.Fatal("callback must not be invoked for identical trees")
		return true
	})
	if !complete {
		t.Fatal("expected a complete comparison")
	}
	if equalCalls != 0 {
		t.Fatalf("expected identical roots to be skipped entirely, equal called %d times", equalCalls)
	}
}

func TestDiffEarlyStop(t *testing.T) {
	old := New[int]()
	new := New[int]()
	for i := 0; i < 100; i++ {
		new, _, _ = new.Insert([]byte(fmt.Sprintf("k%03d", i)), i)
	}

	var got []string
	complete := old.Diff(new, func(e DiffEntry[int]) bool {
		got = append(got, string(e.Key))
		return len(got) < 3
	})
	if complete {
		t.Fatal("expected Diff to report an incomplete comparison")
	}
	if len(got) != 3 {
		t.Fatalf("callback should stop after 3 entries, got %v", got)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("partial output is not ordered: %v", got)
	}
}

// TestDiffEarlyStopEverywhere checks that stopping at any point, on any code
// path, yields exactly the prefix of the full result and reports the stop.
func TestDiffEarlyStopEverywhere(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	for round := 0; round < 40; round++ {
		oldKeys := make(map[string]int)
		newKeys := make(map[string]int)
		for i := 0; i < 60; i++ {
			k := randomKey(rng)
			oldKeys[k] = rng.Intn(5)
			if rng.Intn(2) == 0 {
				newKeys[k] = oldKeys[k]
			} else {
				newKeys[k] = rng.Intn(5)
			}
		}
		for i := 0; i < 20; i++ {
			k := randomKey(rng)
			if rng.Intn(2) == 0 {
				delete(oldKeys, k)
			} else {
				delete(newKeys, k)
			}
			// Also exercise prefix divergence between the two trees.
			if rng.Intn(3) == 0 {
				delete(oldKeys, k[:1])
				delete(newKeys, k[:1])
				oldKeys[k] = rng.Intn(5)
				newKeys[k+"x"] = rng.Intn(5)
			}
		}

		var oldTree, newTree *Tree[int]
		if rng.Intn(2) == 0 {
			// Share structure: derive one tree from the other.
			oldTree = buildTree(oldKeys)
			txn := oldTree.Txn()
			for k := range oldKeys {
				if _, ok := newKeys[k]; !ok {
					txn.Delete([]byte(k))
				}
			}
			for k, v := range newKeys {
				txn.Insert([]byte(k), v)
			}
			newTree = txn.Commit()
		} else {
			oldTree = buildTree(oldKeys)
			newTree = buildTree(newKeys)
		}

		full := collectDiff(t, oldTree, newTree)

		for stop := 0; stop <= len(full); stop++ {
			var got []DiffEntry[int]
			callsAfterStop := 0
			stopped := false
			complete := oldTree.Diff(newTree, func(e DiffEntry[int]) bool {
				if stopped {
					callsAfterStop++
					return false
				}
				if len(got) >= stop {
					stopped = true
					return false
				}
				got = append(got, e)
				return true
			})
			wantComplete := stop >= len(full)
			if complete != wantComplete {
				t.Fatalf("round %d stop %d: complete = %v, want %v", round, stop, complete, wantComplete)
			}
			if callsAfterStop != 0 {
				t.Fatalf("round %d stop %d: callback invoked %d times after returning false", round, stop, callsAfterStop)
			}
			wantLen := stop
			if stop > len(full) {
				wantLen = len(full)
			}
			if len(got) != wantLen {
				t.Fatalf("round %d stop %d: got %d entries, want %d", round, stop, len(got), wantLen)
			}
			for i := range got {
				if got[i].Kind != full[i].Kind || !bytes.Equal(got[i].Key, full[i].Key) ||
					got[i].Old != full[i].Old || got[i].New != full[i].New {
					t.Fatalf("round %d stop %d: entry %d = %#v, want %#v", round, stop, i, got[i], full[i])
				}
			}
		}
	}
}

// TestDiffEarlyStopAtStructuralBoundaries drives the callback to stop in the
// middle of every structural branch: divergence at a shared branch byte, and
// the virtual edge produced when one tree has a node where the other has a
// deeper chain.
func TestDiffEarlyStopAtStructuralBoundaries(t *testing.T) {
	cases := []struct {
		name string
		old  []string
		new  []string
	}{
		{"divergence removes first", []string{"ab"}, []string{"ac"}},
		{"divergence adds first", []string{"ac"}, []string{"ab"}},
		{"old ancestor, lower old branch", []string{"a", "aa"}, []string{"ab"}},
		{"old ancestor, new virtual edge", []string{"a", "az"}, []string{"ab"}},
		{"old ancestor, higher old branch", []string{"a", "az", "c"}, []string{"ab"}},
		{"new ancestor, lower new branch", []string{"ab"}, []string{"a", "aa"}},
		{"new ancestor, old virtual edge", []string{"ab"}, []string{"a", "az"}},
		{"new ancestor, higher new branch", []string{"ab"}, []string{"a", "az", "c"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldKeys := make(map[string]int)
			for i, k := range tc.old {
				oldKeys[k] = i + 1
			}
			newKeys := make(map[string]int)
			for i, k := range tc.new {
				newKeys[k] = i + 1
			}

			oldTree := buildTree(oldKeys)
			newTree := buildTree(newKeys)

			full := collectDiff(t, oldTree, newTree)
			assertDiffMatches(t, 0, oldKeys, newKeys, full)

			// Stop at every possible point, including before the first entry,
			// so each early-return path is exercised.
			for stop := 0; stop < len(full); stop++ {
				var got []DiffEntry[int]
				complete := oldTree.Diff(newTree, func(e DiffEntry[int]) bool {
					if len(got) >= stop {
						return false
					}
					got = append(got, e)
					return true
				})
				if complete {
					t.Fatalf("stop %d: expected Diff to report an early stop", stop)
				}
				if len(got) != stop {
					t.Fatalf("stop %d: got %d entries, want %d", stop, len(got), stop)
				}
				for i := range got {
					if got[i].Kind != full[i].Kind || !bytes.Equal(got[i].Key, full[i].Key) {
						t.Fatalf("stop %d: entry %d = %#v, want %#v", stop, i, got[i], full[i])
					}
				}
			}
		})
	}
}

func TestDiffEntriesHelper(t *testing.T) {
	old := New[int]()
	old, _, _ = old.Insert([]byte("a"), 1)
	new, _, _ := old.Insert([]byte("b"), 2)

	entries := old.DiffEntries(new)
	if len(entries) != 1 || entries[0].Kind != DiffAdded || string(entries[0].Key) != "b" {
		t.Fatalf("unexpected entries: %v", entries)
	}
	if got := old.DiffEntries(old); len(got) != 0 {
		t.Fatalf("identical trees should yield no entries, got %v", got)
	}
}

func TestDiffNilCallback(t *testing.T) {
	old := New[int]()
	new, _, _ := old.Insert([]byte("a"), 1)
	if !old.Diff(new, nil) {
		t.Fatal("nil callback should be a completed no-op")
	}
}

func TestDiffCustomEquality(t *testing.T) {
	type value struct {
		ID      int
		Version int
	}

	old := New[value]()
	old, _, _ = old.Insert([]byte("a"), value{ID: 1, Version: 1})
	new, _, _ := old.Insert([]byte("a"), value{ID: 1, Version: 2})

	if got := collectDiff(t, old, new); len(got) != 1 || got[0].Kind != DiffUpdated {
		t.Fatalf("expected the version bump to be reported as one update: %v", got)
	}

	var got []DiffEntry[value]
	complete := old.DiffWith(new, func(a, b value) bool {
		return a.ID == b.ID
	}, func(e DiffEntry[value]) bool {
		got = append(got, e)
		return true
	})
	if !complete {
		t.Fatal("expected a complete comparison")
	}
	if len(got) != 0 {
		t.Fatalf("custom equality should treat IDs as equal, got %v", got)
	}
}

// TestDiffExhaustivePrefixes compares every pair of subsets of a small key
// universe that is dense in prefix relationships. Both sides get an extra
// character that diverges at several depths so the prefix-split paths are
// exercised as well.
func TestDiffExhaustivePrefixes(t *testing.T) {
	universe := []string{"a", "ab", "abc", "abd", "ac", "b", "ba"}

	for oldMask := 0; oldMask < 1<<len(universe); oldMask++ {
		oldKeys := make(map[string]int)
		for i, k := range universe {
			if oldMask&(1<<i) != 0 {
				if i%5 == 0 {
					// Restructure the key so one side has a branch the other
					// does not, at a different depth.
					k += "zz"
				}
				oldKeys[k] = i + 1
			}
		}

		for newMask := 0; newMask < 1<<len(universe); newMask++ {
			newKeys := make(map[string]int)
			for i, k := range universe {
				if newMask&(1<<i) != 0 {
					if i%3 == 0 {
						k += "y"
					}
					// Every other shared key keeps its value so updates and
					// unchanged entries both occur.
					if oldMask&(1<<i) != 0 && i%2 == 0 && !(i%5 == 0) {
						newKeys[k] = i + 1
					} else {
						newKeys[k] = 100 + i
					}
				}
			}

			oldTree := buildTree(oldKeys)
			newTree := buildTree(newKeys)
			entries := collectDiff(t, oldTree, newTree)
			assertDiffMatches(t, oldMask<<8|newMask, oldKeys, newKeys, entries)
		}
	}
}

func TestDiffEmptyKey(t *testing.T) {
	old := New[int]()
	old, _, _ = old.Insert([]byte(""), 1)
	old, _, _ = old.Insert([]byte("a"), 2)

	got := collectDiff(t, old, New[int]())
	if len(got) != 2 {
		t.Fatalf("expected 2 removals, got %v", got)
	}
	if got[0].Kind != DiffRemoved || len(got[0].Key) != 0 || got[0].Old != 1 {
		t.Fatalf("empty key should sort first and be reported as removed, got %#v", got[0])
	}

	new, _, _ := New[int]().Insert([]byte(""), 42)
	got = collectDiff(t, New[int](), new)
	if len(got) != 1 || got[0].Kind != DiffAdded || len(got[0].Key) != 0 || got[0].New != 42 {
		t.Fatalf("unexpected entries for empty key addition: %#v", got)
	}

	got = collectDiff(t, old, new)
	if len(got) != 2 {
		t.Fatalf("expected 1 removal and 1 update, got %v", got)
	}
	if got[0].Kind != DiffUpdated || len(got[0].Key) != 0 || got[0].Old != 1 || got[0].New != 42 {
		t.Fatalf("empty key should be reported as updated first, got %#v", got[0])
	}
	if got[1].Kind != DiffRemoved || string(got[1].Key) != "a" {
		t.Fatalf("unexpected second entry: %#v", got[1])
	}
}

func TestDiffNonComparableValues(t *testing.T) {
	type value struct {
		Name string
		Tags []string
	}

	old := New[value]()
	old, _, _ = old.Insert([]byte("a"), value{Name: "a", Tags: []string{"x"}})
	old, _, _ = old.Insert([]byte("b"), value{Name: "b", Tags: []string{"y"}})

	txn := old.Txn()
	txn.Insert([]byte("a"), value{Name: "a", Tags: []string{"x"}}) // equal contents, distinct slice
	txn.Delete([]byte("b"))
	txn.Insert([]byte("c"), value{Name: "c"})
	new := txn.Commit()

	got := collectDiff(t, old, new)
	if len(got) != 2 {
		t.Fatalf("expected one removal and one addition, got %v", got)
	}
	if got[0].Kind != DiffRemoved || string(got[0].Key) != "b" {
		t.Fatalf("unexpected first entry: %#v", got[0])
	}
	if got[1].Kind != DiffAdded || string(got[1].Key) != "c" || got[1].New.Name != "c" {
		t.Fatalf("unexpected second entry: %#v", got[1])
	}
}

func TestDiffResultIsStable(t *testing.T) {
	old := New[int]()
	for i := 0; i < 50; i++ {
		old, _, _ = old.Insert([]byte(fmt.Sprintf("key-%02d", i)), i)
	}
	new, _ := old.DeletePrefix([]byte("key-0"))
	new, _, _ = new.Insert([]byte("key-00"), -1)
	new, _, _ = new.Insert([]byte("key-99"), 99)

	first := collectDiff(t, old, new)
	firstKeys := make([]string, len(first))
	for i, e := range first {
		firstKeys[i] = string(e.Key)
	}

	// Running another comparison must not alter previously returned entries:
	// results reference immutable tree storage, not a scratch path buffer.
	second := collectDiff(t, old, new)
	secondKeys := diffKeys(second)
	if !reflect.DeepEqual(firstKeys, secondKeys) {
		t.Fatalf("comparison output is not stable: %v vs %v", firstKeys, secondKeys)
	}
	for i, e := range first {
		if string(e.Key) != firstKeys[i] {
			t.Fatalf("entry %d key changed from %q to %q", i, firstKeys[i], e.Key)
		}
	}
}

func TestDiffRandomizedAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for round := 0; round < 50; round++ {
		keys := make(map[string]int)
		old := New[int]()
		for i := 0; i < 200; i++ {
			k := randomKey(rng)
			v := rng.Intn(10)
			keys[k] = v
			old, _, _ = old.Insert([]byte(k), v)
		}

		newKeys := make(map[string]int, len(keys))
		for k, v := range keys {
			newKeys[k] = v
		}

		new := old
		for i := 0; i < 50; i++ {
			k := randomKey(rng)
			switch rng.Intn(3) {
			case 0:
				v := rng.Intn(10)
				newKeys[k] = v
				new, _, _ = new.Insert([]byte(k), v)
			case 1:
				delete(newKeys, k)
				new, _, _ = new.Delete([]byte(k))
			case 2:
				new, _ = new.DeletePrefix([]byte(k[:1]))
				for existing := range newKeys {
					if len(existing) > 0 && existing[0] == k[0] {
						delete(newKeys, existing)
					}
				}
			}
		}

		gotEntries := collectDiff(t, old, new)
		assertDiffMatches(t, round, keys, newKeys, gotEntries)
	}
}

func randomKey(rng *rand.Rand) string {
	alphabet := "abcdef"
	n := 1 + rng.Intn(8)
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(buf)
}

// buildTree inserts every key/value pair into a fresh tree, so the result
// shares no nodes with any other tree.
func buildTree(kv map[string]int) *Tree[int] {
	tree := New[int]()
	for k, v := range kv {
		tree, _, _ = tree.Insert([]byte(k), v)
	}
	return tree
}

func TestDiffIndependentTrees(t *testing.T) {
	rng := rand.New(rand.NewSource(7))

	for round := 0; round < 50; round++ {
		oldKeys := make(map[string]int)
		newKeys := make(map[string]int)
		for i := 0; i < 100; i++ {
			k := randomKey(rng)
			if rng.Intn(2) == 0 {
				oldKeys[k] = rng.Intn(10)
			} else {
				newKeys[k] = rng.Intn(10)
			}
		}
		// Give both trees some keys in common with a mix of equal and
		// different values so the comparison sees updates as well.
		for i := 0; i < 40; i++ {
			k := randomKey(rng)
			oldKeys[k] = rng.Intn(10)
			if rng.Intn(2) == 0 {
				newKeys[k] = oldKeys[k]
			} else {
				newKeys[k] = rng.Intn(10)
			}
		}

		oldTree := buildTree(oldKeys)
		newTree := buildTree(newKeys)

		entries := collectDiff(t, oldTree, newTree)
		assertDiffMatches(t, round, oldKeys, newKeys, entries)
	}
}

// assertDiffMatches compares a diff result against a straightforward map
// based reference computation.
func assertDiffMatches(t *testing.T, round int, oldKeys, newKeys map[string]int, entries []DiffEntry[int]) {
	t.Helper()

	got := make(map[string]DiffEntry[int], len(entries))
	prev := ""
	for _, e := range entries {
		key := string(e.Key)
		if _, dup := got[key]; dup {
			t.Fatalf("round %d: duplicate key %q", round, key)
		}
		if prev != "" && key <= prev {
			t.Fatalf("round %d: keys not ordered: %q after %q", round, key, prev)
		}
		prev = key
		got[key] = e
	}

	expected := make(map[string]DiffKind)
	for k, v := range newKeys {
		if oldV, ok := oldKeys[k]; !ok {
			expected[k] = DiffAdded
		} else if oldV != v {
			expected[k] = DiffUpdated
		}
	}
	for k := range oldKeys {
		if _, ok := newKeys[k]; !ok {
			expected[k] = DiffRemoved
		}
	}

	if len(got) != len(expected) {
		t.Fatalf("round %d: got %d entries, want %d", round, len(got), len(expected))
	}
	for k, kind := range expected {
		e, ok := got[k]
		if !ok {
			t.Fatalf("round %d: missing entry for key %q (want %v)", round, k, kind)
		}
		if e.Kind != kind {
			t.Fatalf("round %d: key %q kind = %v, want %v", round, k, e.Kind, kind)
		}
		switch kind {
		case DiffAdded:
			if e.New != newKeys[k] || e.Old != 0 {
				t.Fatalf("round %d: added %q values = (%v,%v)", round, k, e.Old, e.New)
			}
		case DiffRemoved:
			if e.Old != oldKeys[k] || e.New != 0 {
				t.Fatalf("round %d: removed %q values = (%v,%v)", round, k, e.Old, e.New)
			}
		case DiffUpdated:
			if e.Old != oldKeys[k] || e.New != newKeys[k] {
				t.Fatalf("round %d: updated %q values = (%v,%v), want (%v,%v)",
					round, k, e.Old, e.New, oldKeys[k], newKeys[k])
			}
		}
	}
}

func TestDiffConcurrentReaders(t *testing.T) {
	base := New[int]()
	for i := 0; i < 500; i++ {
		base, _, _ = base.Insert([]byte(fmt.Sprintf("key/%03d", i)), i)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writers derive new published snapshots from the same base while readers
	// diff base against previously published snapshots.
	published := make(chan *Tree[int], 8)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(published)
		cur := base
		for i := 0; i < 50; i++ {
			cur, _, _ = cur.Insert([]byte(fmt.Sprintf("new/%03d", i)), i)
			published <- cur
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for snap := range published {
			want := 0
			for i := 0; i <= 49; i++ {
				if _, ok := snap.Get([]byte(fmt.Sprintf("new/%03d", i))); ok {
					want++
				}
			}
			count := 0
			if !base.Diff(snap, func(e DiffEntry[int]) bool {
				count++
				return true
			}) {
				t.Error("unexpected early stop")
				return
			}
			if count != want {
				t.Errorf("got %d additions, want %d", count, want)
				return
			}
		}
	}()

	// Simultaneously diff the base tree against itself (shared root) while
	// the writer runs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if !base.Diff(base, func(DiffEntry[int]) bool {
					t.Error("identical trees must not produce entries")
					return false
				}) {
					t.Error("unexpected early stop")
					return
				}
			}
		}
	}()

	wg.Wait()
}

func TestDiffKeysMatchTreeContents(t *testing.T) {
	old := New[int]()
	old, _, _ = old.Insert([]byte("alpha"), 1)
	old, _, _ = old.Insert([]byte("beta"), 2)

	new := old.Txn()
	new.Delete([]byte("alpha"))
	new.Insert([]byte("beta"), 20)
	new.Insert([]byte("gamma"), 3)
	newTree := new.Commit()

	entries := collectDiff(t, old, newTree)
	for _, e := range entries {
		switch e.Kind {
		case DiffRemoved:
			if v, ok := old.Get(e.Key); !ok || v != e.Old {
				t.Fatalf("removed entry %q does not match old tree", e.Key)
			}
		case DiffAdded, DiffUpdated:
			if v, ok := newTree.Get(e.Key); !ok || v != e.New {
				t.Fatalf("entry %q does not match new tree", e.Key)
			}
		}
		if e.Kind == DiffUpdated {
			if v, ok := old.Get(e.Key); !ok || v != e.Old {
				t.Fatalf("updated entry %q old value does not match old tree", e.Key)
			}
		}
	}
	// The entry keys themselves should still be usable for lookups after the
	// comparison, i.e. they are backed by tree storage.
	for _, e := range entries {
		if e.Kind == DiffAdded || e.Kind == DiffUpdated {
			key := append([]byte(nil), e.Key...)
			if _, ok := newTree.Get(key); !ok {
				t.Fatalf("returned key %q no longer resolves", e.Key)
			}
		}
	}
}

func BenchmarkTreeDiffSparse(b *testing.B) {
	old := New[int]()
	for i := 0; i < 100000; i++ {
		old, _, _ = old.Insert([]byte(fmt.Sprintf("key/%08d", i)), i)
	}
	txn := old.Txn()
	txn.Insert([]byte("key/00050000"), -1)
	new := txn.Commit()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		old.Diff(new, func(DiffEntry[int]) bool { return true })
	}
}
