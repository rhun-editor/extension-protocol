package rhunext

import (
	"encoding/binary"
	"sort"
)

type snippetRegexSetValue struct {
	ranges []snippetRuneRange
	words  map[string]struct{}
}

func (c *snippetRegexCompiler) setValue(n *snippetRegexNode) snippetRegexSetValue {
	v := snippetRegexSetValue{}
	if !c.spend(n, 1) {
		return v
	}
	if n.kind == rxStrings || n.kind == rxProperty && snippetEmojiProperties[n.text] != nil {
		words := n.strings
		if n.kind == rxProperty {
			if n.negative {
				c.fail(n, "cannot complement a Unicode string property")
				return v
			}
			words = snippetEmojiProperties[n.text]
		}
		v.words = make(map[string]struct{})
		for _, word := range words {
			if !c.spend(n, 2*len(word)+1) {
				return snippetRegexSetValue{}
			}
			runes := []rune(word)
			for i, cp := range runes {
				runes[i] = snippetRegexCanonical(cp, c.p.flags)
			}
			if len(runes) == 1 {
				v.ranges = c.one(n, v.ranges, runes[0], runes[0])
			} else {
				v.words[string(runes)] = struct{}{}
			}
		}
		v.ranges = c.closeSetCase(n, c.normalize(n, v.ranges))
		return v
	}
	if n.kind != rxSetUnion && n.kind != rxSetIntersection && n.kind != rxSetSubtraction {
		// Leaf complements in v apply after case equivalence closure.
		leaf := *n
		leaf.negative = false
		v.ranges = c.predicate(&leaf)
		if n.negative {
			v.ranges = c.complement(n, v.ranges)
		}
		return v
	}
	for i, child := range n.children {
		right := c.setValue(child)
		if c.err != nil {
			return snippetRegexSetValue{}
		}
		if i == 0 {
			v = right
			continue
		}
		switch n.kind {
		case rxSetUnion:
			v.ranges = c.normalize(n, c.add(n, v.ranges, right.ranges))
			if !c.spend(n, len(right.words)) {
				return snippetRegexSetValue{}
			}
			if v.words == nil {
				v.words = make(map[string]struct{})
			}
			for word := range right.words {
				if !c.spend(n, len(word)) {
					return snippetRegexSetValue{}
				}
				v.words[word] = struct{}{}
			}
		case rxSetIntersection, rxSetSubtraction:
			v.ranges = c.setRanges(n, v.ranges, right.ranges, n.kind == rxSetSubtraction)
			if !c.spend(n, len(v.words)) {
				return snippetRegexSetValue{}
			}
			for word := range v.words {
				if !c.spend(n, len(word)) {
					return snippetRegexSetValue{}
				}
				_, found := right.words[word]
				if found == (n.kind == rxSetSubtraction) {
					delete(v.words, word)
				}
			}
		}
	}
	if n.negative {
		v.ranges = c.complement(n, v.ranges)
	}
	return v
}

func (c *snippetRegexCompiler) setRanges(n *snippetRegexNode, left, right []snippetRuneRange, subtract bool) []snippetRuneRange {
	var out []snippetRuneRange
	j := 0
	for _, a := range left {
		if !c.spend(n, 1) {
			return nil
		}
		for j < len(right) && right[j].hi < a.lo {
			if !c.spend(n, 1) {
				return nil
			}
			j++
		}
		next := a.lo
		for k := j; k < len(right) && right[k].lo <= a.hi; k++ {
			if !c.spend(n, 1) {
				return nil
			}
			b := right[k]
			if subtract {
				out = c.one(n, out, next, min(a.hi, b.lo-1))
				next = max(next, b.hi+1)
			} else {
				out = c.one(n, out, max(a.lo, b.lo), min(a.hi, b.hi))
			}
		}
		if subtract {
			out = c.one(n, out, next, a.hi)
		}
	}
	return out
}

type snippetSetTrie struct {
	edges map[rune]int
	end   bool
}

func (c *snippetRegexCompiler) closeSetCase(n *snippetRegexNode, ranges []snippetRuneRange) []snippetRuneRange {
	if c.p.flags&uint32(rxIgnoreCase) == 0 || len(ranges) == 0 {
		return ranges
	}
	count := 0
	for _, span := range ranges {
		count += int(span.hi-span.lo) + 1
		if count > 128 {
			return c.closeCase(n, ranges)
		}
	}
	if c.setFolds == nil {
		folds := c.mappings(n)
		if !c.spend(n, 2*len(folds)) {
			return nil
		}
		c.setFolds = make(map[rune][]rune)
		for _, fold := range folds {
			if c.setFolds[fold.to] == nil {
				c.setFolds[fold.to] = []rune{fold.to}
			}
			c.setFolds[fold.to] = append(c.setFolds[fold.to], fold.from)
		}
	}
	var out []snippetRuneRange
	for _, span := range ranges {
		for cp := span.lo; cp <= span.hi; cp++ {
			if !c.spend(n, 16) {
				return nil
			}
			rep := snippetRegexCanonical(cp, c.p.flags)
			if equivalents := c.setFolds[rep]; equivalents != nil {
				for _, equivalent := range equivalents {
					out = c.one(n, out, equivalent, equivalent)
				}
			} else {
				out = c.one(n, out, cp, cp)
			}
		}
	}
	return c.normalize(n, out)
}

type snippetSetEdge struct {
	cp rune
	to int
}
type snippetSetState struct {
	edges []snippetSetEdge
	end   bool
}

// Prepay sorting comparisons before sorting retained set or trie data.
func (c *snippetRegexCompiler) setSortWork(n *snippetRegexNode, count, width int) bool {
	levels := 1
	for k := count; k > 1; k = (k + 1) / 2 {
		levels++
	}
	return c.spend(n, count*levels*width)
}

func (c *snippetRegexCompiler) compileSet(n *snippetRegexNode, v snippetRegexSetValue, backward bool) {
	if c.err != nil {
		return
	}
	if len(v.words) == 0 {
		c.emit(n, rxSet, c.class(n, v.ranges), 0, 0)
		return
	}
	trie := []snippetSetTrie{{edges: make(map[rune]int)}}
	bytes := 0
	for word := range v.words {
		bytes += len(word) + 1
	}
	levels := 1
	for k := len(v.words); k > 1; k = (k + 1) / 2 {
		levels++
	}
	if !c.spend(n, bytes*(levels+1)) {
		return
	}
	words := make([]string, 0, len(v.words))
	for word := range v.words {
		words = append(words, word)
	}
	sort.Strings(words)
	for _, word := range words {
		if !c.spend(n, 2*len(word)+1) {
			return
		}
		runes := []rune(word)
		at := 0
		for i := range runes {
			cp := runes[i]
			if backward {
				cp = runes[len(runes)-1-i]
			}
			next, ok := trie[at].edges[cp]
			if !ok {
				if len(trie) == 65536 {
					c.fail(n, "string set trie limit exceeded")
					return
				}
				next = len(trie)
				trie = append(trie, snippetSetTrie{edges: make(map[rune]int)})
				trie[at].edges[cp] = next
			}
			at = next
		}
		trie[at].end = true
	}
	ids := make([]int, len(trie))
	intern := make(map[string]int)
	var states []snippetSetState
	for at := len(trie) - 1; at >= 0; at-- {
		t := trie[at]
		if !c.setSortWork(n, len(t.edges), 1) || !c.spend(n, 1+len(t.edges)*20) {
			return
		}
		edges := make([]snippetSetEdge, 0, len(t.edges))
		for cp, to := range t.edges {
			edges = append(edges, snippetSetEdge{cp, ids[to]})
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].cp < edges[j].cp })
		signature := make([]byte, 1+8*len(edges))
		if t.end {
			signature[0] = 1
		}
		for i, edge := range edges {
			binary.LittleEndian.PutUint32(signature[1+8*i:], uint32(edge.cp))
			binary.LittleEndian.PutUint32(signature[5+8*i:], uint32(edge.to))
		}
		key := string(signature)
		id, ok := intern[key]
		if !ok {
			id = len(states)
			states = append(states, snippetSetState{edges, t.end})
			intern[key] = id
		}
		ids[at] = id
	}
	entry := c.emit(n, rxJump, 0, 0, 0)
	pcs := make([]int, len(states))
	var endings []int
	for id, state := range states {
		if c.err != nil {
			return
		}
		pcs[id] = len(c.p.code)
		// Group transitions by child state. Every group consumes one scalar.
		groups := make(map[int][]snippetRuneRange)
		var order []int
		for _, edge := range state.edges {
			if !c.spend(n, 1) {
				return
			}
			if _, ok := groups[edge.to]; !ok {
				order = append(order, edge.to)
			}
			groups[edge.to] = c.one(n, groups[edge.to], edge.cp, edge.cp)
		}
		root := id == ids[0]
		scalar := root && len(v.ranges) != 0
		alternatives := len(order)
		if scalar {
			alternatives++
		}
		if state.end {
			alternatives++
		}
		branch := func() int {
			alternatives--
			if alternatives == 0 {
				return -1
			}
			return c.emit(n, rxSplit, 0, 0, 0)
		}
		for _, to := range order {
			split := branch()
			body := len(c.p.code)
			ranges := c.closeSetCase(n, c.normalize(n, groups[to]))
			c.emit(n, rxSet, c.class(n, ranges), 0, 0)
			c.emit(n, rxJump, uint32(pcs[to]), 0, 0)
			if split >= 0 {
				c.patch(split, uint32(body), uint32(len(c.p.code)), 0)
			}
		}
		if scalar {
			split := branch()
			body := len(c.p.code)
			c.emit(n, rxSet, c.class(n, v.ranges), 0, 0)
			endings = append(endings, c.emit(n, rxJump, 0, 0, 0))
			if split >= 0 {
				c.patch(split, uint32(body), uint32(len(c.p.code)), 0)
			}
		}
		if state.end {
			endings = append(endings, c.emit(n, rxJump, 0, 0, 0))
		}
	}
	c.patch(entry, uint32(pcs[ids[0]]), 0, 0)
	for _, pc := range endings {
		c.patch(pc, uint32(len(c.p.code)), 0, 0)
	}
}
