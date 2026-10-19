package rhunext

import "strings"

// Snapshot whitespace belongs to the unresolved template layout. Variable values
// are deliberately absent from this first pass, including values with linebreaks.
type snippetLayoutNode struct {
	source   *stNode
	text     string
	choice   *stNode
	children []snippetLayoutNode
	failure  error
}
type snippetLayoutBuilder struct {
	e       *snippetExpander
	virtual snippetExpander
	active  map[int]bool
	count   int
}

func (e *snippetExpander) renderFormatted(nodes []stNode) error {
	b := snippetLayoutBuilder{e: e, virtual: snippetExpander{format: newSnippetFormatter(e.variables.snapshot, e.variables.keepWhitespace)}, active: map[int]bool{}}
	layout, err := b.build(nodes)
	if err != nil {
		return err
	}
	r := snippetLayoutRenderer{e: e, lastTextLeading: e.variables.snapshot.Indent}
	return r.render(layout, 0, 0, true)
}
func (b *snippetLayoutBuilder) build(nodes []stNode) ([]snippetLayoutNode, error) {
	var out []snippetLayoutNode
	for i := range nodes {
		n := &nodes[i]
		if b.count == MaxSnippetFormatParts {
			return nil, expansionError(n.from, "format layout parts exceed limit")
		}
		b.count++
		node := snippetLayoutNode{source: n}
		var err error
		switch {
		case n.kind == stText:
			from := b.virtual.out.Len()
			err = b.virtual.format.literal(&b.virtual, snippetPart{text: n.text, at: n.from})
			node.text = b.virtual.out.String()[from:]
		case numericNode(n) && n.index > 0:
			v := b.e.groups[n.index]
			if b.active[n.index] {
				node.failure = expansionError(v.offset, "snippet default dependency cycle")
				break
			}
			if len(b.active) == MaxSnippetDepth {
				node.failure = expansionError(v.offset, "default dependency depth exceeds its limit")
				break
			}
			b.active[n.index] = true
			if definition := v.definition; definition != nil {
				if definition.kind == stChoice {
					node.choice = definition
					node.text = definition.choices[0]
					err = b.virtual.appendRaw(node.text, n.from)
				} else {
					node.children, err = b.build(definition.children)
				}
			}
			delete(b.active, n.index)
		case n.kind == stChoice:
			node.choice = n
			node.text = n.choices[0]
			err = b.virtual.appendRaw(node.text, n.from)
		default:
			node.children, err = b.build(n.children)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, nil
}

type snippetLayoutRenderer struct {
	e               *snippetExpander
	lastTextLeading string
	markers         int
}

func (r *snippetLayoutRenderer) marker(text string, at int) error {
	if err := r.e.appendRaw(text, at); err != nil {
		return err
	}
	if end := strings.LastIndexAny(text, "\r\n"); end >= 0 {
		text = text[end+1:]
	}
	end := 0
	for end < len(text) && (text[end] == ' ' || text[end] == '\t') {
		end++
	}
	// Empty resolved Text markers matter, even though they write no bytes.
	r.lastTextLeading = text[:end]
	return nil
}
func (r *snippetLayoutRenderer) selected(name, input string, at int) (string, error) {
	if name != "SELECTION" && name != "TM_SELECTED_TEXT" {
		return input, nil
	}
	if _, override := r.e.context.Variables[name]; override {
		return input, nil
	}
	base := r.e.variables.snapshot.Indent
	common := 0
	for common < len(base) && common < len(r.lastTextLeading) && base[common] == r.lastTextLeading[common] {
		common++
	}
	return r.e.adjustSelectionInput(input, r.lastTextLeading[common:], at)
}
func (r *snippetLayoutRenderer) render(nodes []snippetLayoutNode, parent, parentDepth int, fields bool) error {
	e := r.e
	for i := range nodes {
		node := &nodes[i]
		n := node.source
		e.lastOffset = n.from
		if r.markers == MaxSnippetFormatParts {
			return expansionError(n.from, "rendered format parts exceed limit")
		}
		r.markers++
		if node.failure != nil {
			return node.failure
		}
		switch {
		case n.kind == stText:
			if err := r.marker(node.text, n.from); err != nil {
				return err
			}
		case n.kind == stTransform && n.name != "":
			compiled, err := e.compiledTransform(n)
			if err != nil {
				return err
			}
			input, _ := e.variable(n.name)
			input, err = r.selected(n.name, input, n.from)
			if err != nil {
				return err
			}
			text, err := e.transformText(compiled, input, n.from)
			if err != nil {
				return err
			}
			if err = r.marker(text, n.from); err != nil {
				return err
			}
		case n.kind == stVariable:
			text, provided := e.variable(n.name)
			if provided {
				text, err := r.selected(n.name, text, n.from)
				if err != nil {
					return err
				}
				if err = r.marker(text, n.from); err != nil {
					return err
				}
			} else if n.hasDefault {
				if err := r.render(node.children, parent, parentDepth, fields); err != nil {
					return err
				}
			} else if !knownSnippetVariable(n.name) {
				if err := r.unknown(e.unknown[n.name], parent, parentDepth, n.from, fields); err != nil {
					return err
				}
			}
		default:
			if err := r.placeholder(node, parent, parentDepth, fields); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *snippetLayoutRenderer) unknown(v *snippetValue, parent, depth, at int, fields bool) error {
	e := r.e
	index := -1
	if fields {
		if !e.emitted[v.id] {
			if e.groupCount == MaxSnippetGroups {
				return expansionError(at, "too many expanded groups")
			}
			e.emitted[v.id] = true
			e.groupCount++
		}
		var err error
		index, err = e.field(v.id, parent, depth, at)
		if err != nil {
			return err
		}
	}
	if err := r.marker(v.name, at); err != nil {
		return err
	}
	if index >= 0 {
		e.fields[index].End = e.out.Len()
	}
	return nil
}
func (r *snippetLayoutRenderer) placeholder(node *snippetLayoutNode, parent, depth int, fields bool) error {
	e := r.e
	n := node.source
	if n.index == 0 {
		if n.kind == stChoice {
			return expansionError(n.from, "choices require a positive editable group")
		}
		if n.kind == stTransform {
			return expansionError(n.from, "transforms require a positive editable group")
		}
		index := -1
		if fields {
			if e.finalSeen {
				return expansionError(n.from, "multiple final selections")
			}
			e.finalSeen = true
			var err error
			index, err = e.field(0, parent, depth, n.from)
			if err != nil {
				return err
			}
		}
		if err := r.render(node.children, parent, depth, fields); err != nil {
			return err
		}
		if index >= 0 {
			e.fields[index].End = e.out.Len()
		}
		return nil
	}
	var transform *snippetPreparedTransform
	var err error
	if n.kind == stTransform {
		transform, err = e.compiledTransform(n)
		if err != nil {
			return err
		}
	}
	group := e.groups[n.index].id
	first := fields && !e.emitted[group]
	index := -1
	if fields {
		if first {
			if e.groupCount == MaxSnippetGroups {
				return expansionError(n.from, "too many expanded groups")
			}
			e.emitted[group] = true
			e.groupCount++
		}
		index, err = e.field(group, parent, depth, n.from)
		if err != nil {
			return err
		}
	}
	if node.choice != nil {
		// Choices are opaque options, outside the ordinary Text marker walk.
		if err = e.appendRaw(node.text, n.from); err != nil {
			return err
		}
		if first {
			e.choices = append(e.choices, SnippetChoice{group, append([]string(nil), node.choice.choices...)})
		}
	} else {
		childParent, childDepth := parent, depth
		if first {
			childParent, childDepth = index+1, depth+1
		}
		if err = r.render(node.children, childParent, childDepth, first); err != nil {
			return err
		}
	}
	if index >= 0 {
		e.fields[index].End = e.out.Len()
		if transform != nil {
			e.transforms = append(e.transforms, SnippetFieldTransform{index + 1, transform.compiled})
		}
	}
	return nil
}
