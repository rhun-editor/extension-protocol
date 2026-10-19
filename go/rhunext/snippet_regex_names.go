package rhunext

type snippetRegexNameChoice struct {
	alternative *snippetRegexNode
	branch      int
}

// Duplicate definitions must occupy distinct branches of a common disjunction.
// Capture count and syntax depth bound both the stored paths and pair checks.
func (p *snippetRegexParser) resolveDuplicateNames(root *snippetRegexNode) error {
	var definitions [maxSnippetRegexCaptures + 1]*snippetRegexNode
	var paths [maxSnippetRegexCaptures + 1][]snippetRegexNameChoice
	var walk func(*snippetRegexNode, []snippetRegexNameChoice)
	walk = func(n *snippetRegexNode, path []snippetRegexNameChoice) {
		if n.kind == rxCapture && n.text != "" {
			definitions[n.group] = n
			paths[n.group] = append([]snippetRegexNameChoice(nil), path...)
		}
		for branch, child := range n.children {
			if n.kind == rxAlternative {
				walk(child, append(path, snippetRegexNameChoice{n, branch}))
			} else {
				walk(child, path)
			}
		}
	}
	walk(root, nil)
	p.aliases = make(map[string][]int)
	for group := 1; group <= p.captures; group++ {
		n := definitions[group]
		if n == nil {
			continue
		}
		for _, previous := range p.aliases[n.text] {
			left, right := paths[previous], paths[group]
			disjoint := false
			for i := 0; i < min(len(left), len(right)); i++ {
				if left[i].alternative != right[i].alternative {
					break
				}
				if left[i].branch != right[i].branch {
					disjoint = true
					break
				}
			}
			if !disjoint {
				return p.fail(n.from, "duplicate capture names may both participate")
			}
		}
		p.aliases[n.text] = append(p.aliases[n.text], group)
	}
	return nil
}
