// Copyright IBM Corp. 2015, 2025
// SPDX-License-Identifier: MPL-2.0

package iradix

import "reflect"

// DiffKind identifies how a key differs between two trees.
type DiffKind uint8

const (
	// DiffAdded indicates a key that is present only in the other (newer)
	// tree.
	DiffAdded DiffKind = iota

	// DiffRemoved indicates a key that is present only in the receiver
	// (older) tree.
	DiffRemoved

	// DiffUpdated indicates a key that is present in both trees but whose
	// values differ.
	DiffUpdated
)

// String returns a human-readable name for the change kind.
func (k DiffKind) String() string {
	switch k {
	case DiffAdded:
		return "added"
	case DiffRemoved:
		return "removed"
	case DiffUpdated:
		return "updated"
	default:
		return "unknown"
	}
}

// DiffEntry describes a single key difference reported by Tree.Diff.
type DiffEntry[T any] struct {
	// Kind is the kind of change.
	Kind DiffKind

	// Key is the key that changed. It aliases the key stored in one of the
	// compared trees, just like the keys returned by the iterators, and must
	// not be modified.
	Key []byte

	// Old is the value in the receiver (older) tree. It is the zero value of
	// T when Kind is DiffAdded.
	Old T

	// New is the value in the other (newer) tree. It is the zero value of T
	// when Kind is DiffRemoved.
	New T
}

// DiffFn is called for every difference found by Tree.Diff. Returning false
// stops the comparison immediately.
type DiffFn[T any] func(entry DiffEntry[T]) bool

// Diff reports the differences between the receiver and other, treating the
// receiver as the older tree and other as the newer one.
//
// Keys present only in other are reported as DiffAdded, keys present only in
// the receiver are reported as DiffRemoved, and keys present in both trees
// whose values are not deeply equal are reported as DiffUpdated. Values are
// compared with reflect.DeepEqual; use DiffWith to supply a custom comparison.
//
// Entries are delivered in ascending lexicographic key order and each key is
// reported at most once. The comparison performs structural pruning: subtrees
// that are shared by both trees (which is what immutable transactions leave
// behind for the untouched parts of a tree) are skipped without visiting any
// of their leaves.
//
// Neither tree is modified, and Diff only reads immutable state. It is safe to
// run concurrently with reads of either tree, and with transactions that
// derive new trees from them.
//
// Diff returns true if the comparison completed, or false if fn asked it to
// stop early. A nil receiver or other is treated as an empty tree. A nil fn
// causes Diff to return true without doing any work.
func (t *Tree[T]) Diff(other *Tree[T], fn DiffFn[T]) bool {
	return t.DiffWith(other, nil, fn)
}

// DiffWith is like Diff, but uses equal to decide whether a key present in
// both trees has changed. This avoids reflection for types with an efficient
// equality check, and lets callers ignore parts of a value. A nil equal is
// equivalent to reflect.DeepEqual.
func (t *Tree[T]) DiffWith(other *Tree[T], equal func(a, b T) bool, fn DiffFn[T]) bool {
	if fn == nil {
		return true
	}

	var oldRoot, newRoot *Node[T]
	if t != nil {
		oldRoot = t.root
	}
	if other != nil {
		newRoot = other.root
	}

	// A nil tree behaves like an empty one. The roots of trees returned by
	// New are never nil, but treating nil as empty keeps Diff total.
	switch {
	case oldRoot == nil && newRoot == nil:
		return true
	case oldRoot == nil:
		return walkSubtreeDiff(newRoot, DiffAdded, fn)
	case newRoot == nil:
		return walkSubtreeDiff(oldRoot, DiffRemoved, fn)
	}

	if equal == nil {
		equal = func(a, b T) bool {
			return reflect.DeepEqual(a, b)
		}
	}

	d := differ[T]{equal: equal, fn: fn}
	return d.compareNodes(oldRoot, nil, newRoot, nil)
}

// DiffEntries reports the differences between the receiver and other and
// returns them in ascending lexicographic key order. It is a convenience
// wrapper around Diff for callers that want to keep the whole result;
// memory use is proportional to the number of differences.
func (t *Tree[T]) DiffEntries(other *Tree[T]) []DiffEntry[T] {
	var entries []DiffEntry[T]
	t.Diff(other, func(entry DiffEntry[T]) bool {
		entries = append(entries, entry)
		return true
	})
	return entries
}

// differ holds the state needed to compare two trees.
type differ[T any] struct {
	equal func(a, b T) bool
	fn    DiffFn[T]
}

// compareNodes compares the subtree rooted at oldN against the subtree rooted at
// newN. oldP and newP are the remaining path segments from the current
// comparison position to each node, so both nodes sit at or below the same
// position in key space. They are slices of tree-owned node prefixes; no
// temporary path is ever assembled.
//
// It returns false if the callback asked to stop.
func (d *differ[T]) compareNodes(oldN *Node[T], oldP []byte, newN *Node[T], newP []byte) bool {
	// Immutable trees share every node that a later transaction did not
	// touch. If both sides reached the same node, the entire subtree below
	// it is unchanged, so skip it without comparing paths or descending.
	if oldN == newN {
		return true
	}

	// The path is spanned by both node prefixes as long as they match. How
	// much they share determines whether the two nodes line up at the same
	// position, whether one is an ancestor of the other, or whether the
	// subtrees diverge entirely.
	common := longestPrefix(oldP, newP)

	switch {
	case common == len(oldP) && common == len(newP):
		// Both nodes are at the current position.
		return d.samePosition(oldN, newN)

	case common == len(oldP):
		// The old node is at the current position and the new node sits
		// further down the path.
		return d.oldAncestor(oldN, newN, newP[common:])

	case common == len(newP):
		// The new node is at the current position and the old node sits
		// further down the path.
		return d.newAncestor(oldN, oldP[common:], newN)

	default:
		// The pending paths diverge below the current position, so no key
		// can exist in both subtrees: everything below one side is removed
		// and everything below the other is added. Report the side with the
		// smaller branching byte first so the output stays in key order.
		if oldP[common] < newP[common] {
			if !walkSubtreeDiff(oldN, DiffRemoved, d.fn) {
				return false
			}
			return walkSubtreeDiff(newN, DiffAdded, d.fn)
		}
		if !walkSubtreeDiff(newN, DiffAdded, d.fn) {
			return false
		}
		return walkSubtreeDiff(oldN, DiffRemoved, d.fn)
	}
}

// samePosition compares two nodes that cover exactly the same key path.
// views has already established that the nodes are not the same pointer.
func (d *differ[T]) samePosition(oldN, newN *Node[T]) bool {
	switch {
	case oldN.leaf != nil && newN.leaf != nil:
		// A shared leaf is immutable, so its value cannot have changed.
		if oldN.leaf != newN.leaf && !d.equal(oldN.leaf.val, newN.leaf.val) {
			if !d.fn(DiffEntry[T]{
				Kind: DiffUpdated,
				Key:  oldN.leaf.key,
				Old:  oldN.leaf.val,
				New:  newN.leaf.val,
			}) {
				return false
			}
		}
	case oldN.leaf != nil:
		if !d.fn(DiffEntry[T]{Kind: DiffRemoved, Key: oldN.leaf.key, Old: oldN.leaf.val}) {
			return false
		}
	case newN.leaf != nil:
		if !d.fn(DiffEntry[T]{Kind: DiffAdded, Key: newN.leaf.key, New: newN.leaf.val}) {
			return false
		}
	}

	oldEdges, newEdges := oldN.edges, newN.edges
	i, j := 0, 0
	for i < len(oldEdges) || j < len(newEdges) {
		switch {
		case j == len(newEdges) || (i < len(oldEdges) && oldEdges[i].label < newEdges[j].label):
			// Only the old side has this branch.
			if !walkSubtreeDiff(oldEdges[i].node, DiffRemoved, d.fn) {
				return false
			}
			i++
		case i == len(oldEdges) || newEdges[j].label < oldEdges[i].label:
			// Only the new side has this branch.
			if !walkSubtreeDiff(newEdges[j].node, DiffAdded, d.fn) {
				return false
			}
			j++
		default:
			// Both sides have this branch; align the nodes below it.
			if !d.compareNodes(oldEdges[i].node, oldEdges[i].node.prefix,
				newEdges[j].node, newEdges[j].node.prefix) {
				return false
			}
			i++
			j++
		}
	}
	return true
}

// oldAncestor compares a node that sits at the current position against a node
// that sits further down the same path. newP is the remaining path from the
// current position to newN, so its first byte acts as the label of a virtual
// single edge on the new side.
func (d *differ[T]) oldAncestor(oldN, newN *Node[T], newP []byte) bool {
	// The new side has no leaf at the current position because newN lies
	// strictly below it.
	if oldN.leaf != nil {
		if !d.fn(DiffEntry[T]{Kind: DiffRemoved, Key: oldN.leaf.key, Old: oldN.leaf.val}) {
			return false
		}
	}

	label := newP[0]
	newEmitted := false
	for _, e := range oldN.edges {
		switch {
		case e.label < label:
			if !walkSubtreeDiff(e.node, DiffRemoved, d.fn) {
				return false
			}
		case e.label == label:
			newEmitted = true
			if !d.compareNodes(e.node, e.node.prefix, newN, newP) {
				return false
			}
		default:
			// The new virtual edge sorts before this old edge, so report it
			// now to keep the output ordered.
			if !newEmitted {
				newEmitted = true
				if !walkSubtreeDiff(newN, DiffAdded, d.fn) {
					return false
				}
			}
			if !walkSubtreeDiff(e.node, DiffRemoved, d.fn) {
				return false
			}
		}
	}
	if !newEmitted {
		return walkSubtreeDiff(newN, DiffAdded, d.fn)
	}
	return true
}

// newAncestor compares a node that sits at the current position against a node
// that sits further down the same path. oldP is the remaining path from the
// current position to oldN, so its first byte acts as the label of a virtual
// single edge on the old side.
func (d *differ[T]) newAncestor(oldN *Node[T], oldP []byte, newN *Node[T]) bool {
	// The old side has no leaf at the current position because oldN lies
	// strictly below it.
	if newN.leaf != nil {
		if !d.fn(DiffEntry[T]{Kind: DiffAdded, Key: newN.leaf.key, New: newN.leaf.val}) {
			return false
		}
	}

	label := oldP[0]
	oldEmitted := false
	for _, e := range newN.edges {
		switch {
		case e.label < label:
			if !walkSubtreeDiff(e.node, DiffAdded, d.fn) {
				return false
			}
		case e.label == label:
			oldEmitted = true
			if !d.compareNodes(oldN, oldP, e.node, e.node.prefix) {
				return false
			}
		default:
			// The old virtual edge sorts before this new edge, so report it
			// now to keep the output ordered.
			if !oldEmitted {
				oldEmitted = true
				if !walkSubtreeDiff(oldN, DiffRemoved, d.fn) {
					return false
				}
			}
			if !walkSubtreeDiff(e.node, DiffAdded, d.fn) {
				return false
			}
		}
	}
	if !oldEmitted {
		return walkSubtreeDiff(oldN, DiffRemoved, d.fn)
	}
	return true
}

// walkSubtreeDiff reports every key below n with the given kind, in ascending
// lexicographic key order. It returns false if the callback asked to stop.
func walkSubtreeDiff[T any](n *Node[T], kind DiffKind, fn DiffFn[T]) bool {
	if n.leaf != nil {
		entry := DiffEntry[T]{Kind: kind, Key: n.leaf.key}
		if kind == DiffAdded {
			entry.New = n.leaf.val
		} else {
			entry.Old = n.leaf.val
		}
		if !fn(entry) {
			return false
		}
	}

	for _, e := range n.edges {
		if !walkSubtreeDiff(e.node, kind, fn) {
			return false
		}
	}
	return true
}
