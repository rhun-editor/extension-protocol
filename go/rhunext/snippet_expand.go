package rhunext

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxSnippetContextVariables = 256
	MaxSnippetExpansionCache   = MaxSnippetDepth * MaxSnippetBody
)

// SnippetContext supplies literal variable values without implicit environment or
// clipboard access. A present empty value overrides a template default. Callers
// must not mutate Variables concurrently with Prepare.
type SnippetContext struct{ Variables map[string]string }

// SnippetExpansionError identifies a semantic error at a source byte offset.
// Offset -1 denotes an invalid context or nil template.
type SnippetExpansionError struct {
	Offset  int
	Message string
}

func (e *SnippetExpansionError) Error() string {
	return fmt.Sprintf("snippet expansion byte %d: %s", e.Offset, e.Message)
}

// PrepareSnippetTemplate parses and expands a template into an owned native program.
func PrepareSnippetTemplate(source string, context SnippetContext) (SnippetProgram, error) {
	t, err := ParseSnippetTemplate(source)
	if err != nil {
		return SnippetProgram{}, err
	}
	return t.Prepare(context)
}

// Prepare expands defaults, fields, choices and caller-supplied variables. Output
// group IDs are local: sorted numeric indices precede unknown variable names.
// Missing nonstandard no-default variables become linked editable names. Missing
// standard variables use their defaults or empty text. Variable transforms resolve
// immediately; positive field transforms run on native field exit. Choices and
// transforms on group zero return errors. This raw entry point preserves whitespace.
func (t *SnippetTemplate) Prepare(context SnippetContext) (SnippetProgram, error) {
	return t.prepare(context, nil)
}

func (t *SnippetTemplate) prepare(context SnippetContext, variables *snippetVariables) (SnippetProgram, error) {
	if t == nil {
		return SnippetProgram{}, expansionError(-1, "nil template")
	}
	if err := validateSnippetContext(context); err != nil {
		return SnippetProgram{}, err
	}
	e := snippetExpander{source: t.source, compileWork: maxSnippetRegexMatchWork, matchBudget: snippetRegexMatchBudget{maxSnippetRegexMatchWork}, context: context, variables: variables, groups: map[int]*snippetValue{}, unknown: map[string]*snippetValue{}, emitted: map[int]bool{}}
	e.collect(t.nodes)
	indices := make([]int, 0, len(e.groups))
	for index := range e.groups {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for i, index := range indices {
		e.groups[index].id = i + 1
	}
	for i, value := range e.unknownOrder {
		value.id = len(indices) + i + 1
	}
	var err error
	if variables != nil {
		err = e.renderFormatted(t.nodes)
	} else {
		err = e.render(t.nodes, 0, 0, 0)
	}
	if err != nil {
		return SnippetProgram{}, err
	}
	p := SnippetProgram{Snippet: PreparedSnippet{Text: e.out.String(), Fields: e.fields}, Choices: e.choices, Transforms: e.transforms}
	if variables != nil {
		p.detectInitialVariants()
	}
	if err := p.Validate(); err != nil {
		return SnippetProgram{}, expansionError(e.lastOffset, err.Error())
	}
	return p, nil
}

func expansionError(offset int, message string) error { return &SnippetExpansionError{offset, message} }

func validateSnippetContext(context SnippetContext) error {
	if len(context.Variables) > MaxSnippetContextVariables {
		return expansionError(-1, "too many context variables")
	}
	total := 0
	for name, value := range context.Variables {
		if len(name) > MaxSnippetBody-total {
			return expansionError(-1, "context exceeds byte limit")
		}
		total += len(name)
		if len(value) > MaxSnippetBody-total {
			return expansionError(-1, "context exceeds byte limit")
		}
		total += len(value)
	}
	for name, value := range context.Variables {
		if name == "" || !stNameStart(name[0]) {
			return expansionError(-1, "invalid context variable name")
		}
		for i := 1; i < len(name); i++ {
			if !stNameStart(name[i]) && !stDigit(name[i]) {
				return expansionError(-1, "invalid context variable name")
			}
		}
		if !utf8.ValidString(value) {
			return expansionError(-1, "context value is not UTF-8")
		}
	}
	return nil
}

type snippetValue struct {
	id, offset int
	definition *stNode
	name       string
	state      uint8
	text       string
	height     int
}

type snippetExpander struct {
	source                                    string
	compiled                                  map[*stTransformValue]*snippetPreparedTransform
	compileWork, transformBytes, initialBytes int
	matchBudget                               snippetRegexMatchBudget
	transforms                                []SnippetFieldTransform
	variables                                 *snippetVariables
	format                                    *snippetFormatter
	context                                   SnippetContext
	groups                                    map[int]*snippetValue
	unknown                                   map[string]*snippetValue
	unknownOrder                              []*snippetValue
	emitted                                   map[int]bool
	groupCount, cacheBytes, lastOffset        int
	resolvingGroups                           int
	finalSeen                                 bool
	out                                       strings.Builder
	fields                                    []Field
	choices                                   []SnippetChoice
}

func numericNode(n *stNode) bool {
	return n.kind == stTabstop || n.kind == stPlaceholder || n.kind == stChoice || n.kind == stTransform && n.name == ""
}

func (e *snippetExpander) variable(name string) (string, bool) {
	if text, ok := e.context.Variables[name]; ok {
		return text, true
	}
	if e.variables != nil {
		return e.variables.lookup(name)
	}
	return "", false
}

func (e *snippetExpander) collect(nodes []stNode) {
	for i := range nodes {
		n := &nodes[i]
		if numericNode(n) && n.index > 0 {
			value := e.groups[n.index]
			if value == nil {
				value = &snippetValue{offset: n.from}
				e.groups[n.index] = value
			}
			if value.definition == nil && (n.kind == stChoice || n.kind == stPlaceholder && len(n.children) > 0) {
				value.definition = n
			}
		}
		if n.kind == stVariable && !n.hasDefault {
			if _, provided := e.variable(n.name); !provided && !knownSnippetVariable(n.name) && e.unknown[n.name] == nil {
				value := &snippetValue{name: n.name, offset: n.from}
				e.unknown[n.name] = value
				e.unknownOrder = append(e.unknownOrder, value)
			}
		}
		e.collect(n.children)
	}
}

func expansionDepth(depth, at int) error {
	if depth > MaxSnippetDepth {
		return expansionError(at, "source nesting exceeds its limit")
	}
	return nil
}

// Cache intrinsic dependency height even for empty text and completed cache hits.
func (e *snippetExpander) value(v *snippetValue) (string, int, error) {
	if v.state == 2 {
		if e.resolvingGroups > MaxSnippetDepth-v.height {
			return "", 0, expansionError(v.offset, "default dependency depth exceeds its limit")
		}
		return v.text, v.height, nil
	}
	if v.state == 1 {
		return "", 0, expansionError(v.offset, "snippet default dependency cycle")
	}
	if e.resolvingGroups == MaxSnippetDepth {
		return "", 0, expansionError(v.offset, "default dependency depth exceeds its limit")
	}
	e.resolvingGroups++
	defer func() { e.resolvingGroups-- }()
	v.state = 1
	var out snippetBuffer
	height := 0
	if v.name != "" {
		if err := e.cacheAppend(&out, v.name, v.offset); err != nil {
			return "", 0, err
		}
	} else if n := v.definition; n != nil {
		if n.kind == stChoice {
			if err := e.cacheAppend(&out, n.choices[0], n.from); err != nil {
				return "", 0, err
			}
		} else {
			if err := e.eval(&out, n.children, 0, &height); err != nil {
				return "", 0, err
			}
		}
	}
	height++
	if height > MaxSnippetDepth {
		return "", 0, expansionError(v.offset, "default dependency depth exceeds its limit")
	}
	v.text, v.height, v.state = out.String(), height, 2
	return v.text, v.height, nil
}

func (e *snippetExpander) cacheAppend(out *snippetBuffer, text string, at int) error {
	if len(text) > MaxSnippetBody-out.Len() {
		return expansionError(at, "expanded value exceeds byte limit")
	}
	if len(text) > MaxSnippetExpansionCache-e.cacheBytes {
		return expansionError(at, "expanded value cache exceeds byte limit")
	}
	e.cacheBytes += len(text)
	out.WriteString(text)
	return nil
}

func (e *snippetExpander) eval(out *snippetBuffer, nodes []stNode, depth int, height *int) error {
	if err := expansionDepth(depth, e.lastOffset); err != nil {
		return err
	}
	for i := range nodes {
		n := &nodes[i]
		e.lastOffset = n.from
		var text string
		var h int
		var err error
		var value *snippetValue
		switch n.kind {
		case stText:
			text = n.text
		case stTransform:
			if n.name != "" {
				text, err = e.initialTransform(n)
			} else {
				if n.index == 0 {
					return expansionError(n.from, "transforms require a positive editable group")
				}
				if _, err = e.compiledTransform(n); err != nil {
					return err
				}
				value = e.groups[n.index]
				text, h, err = e.value(value)
			}
		case stVariable:
			var provided bool
			text, provided = e.variable(n.name)
			if !provided {
				if n.hasDefault {
					if err = e.eval(out, n.children, depth+1, height); err != nil {
						return err
					}
					continue
				}
				if !knownSnippetVariable(n.name) {
					value = e.unknown[n.name]
					text, h, err = e.value(value)
				}
			}
		default:
			if n.index == 0 {
				if n.kind == stChoice {
					return expansionError(n.from, "choices require a positive editable group")
				}
				if err = e.eval(out, n.children, depth+1, height); err != nil {
					return err
				}
				continue
			}
			value = e.groups[n.index]
			text, h, err = e.value(value)
		}
		if err != nil {
			return err
		}
		if h > *height {
			*height = h
		}
		if err = e.cacheAppend(out, text, n.from); err != nil {
			return err
		}

	}
	return nil
}

func (e *snippetExpander) appendRaw(text string, at int) error {
	if len(text) > MaxSnippetBody-e.out.Len() {
		return expansionError(at, "expanded text exceeds byte limit")
	}
	offset := e.out.Len()
	e.out.WriteString(text)
	if e.format != nil {
		e.format.observe(text, offset)
	}
	return nil
}

func (e *snippetExpander) field(group, parent, parentDepth, at int) (int, error) {
	if len(e.fields) == MaxSnippetFields {
		return 0, expansionError(at, "too many expanded fields")
	}
	if parentDepth >= MaxSnippetDepth {
		return 0, expansionError(at, "expanded parent depth exceeds its limit")
	}
	index := len(e.fields)
	e.fields = append(e.fields, Field{Group: group, Start: e.out.Len(), End: e.out.Len(), Parent: parent})
	return index, nil
}

func (e *snippetExpander) emit(v *snippetValue, parent, parentDepth, at int) error {
	text, _, err := e.value(v)
	if err != nil {
		return err
	}
	first := !e.emitted[v.id]
	if first && e.groupCount == MaxSnippetGroups {
		return expansionError(at, "too many expanded groups")
	}
	index, err := e.field(v.id, parent, parentDepth, at)
	if err != nil {
		return err
	}
	if !first {
		if err = e.appendRaw(v.text, at); err != nil {
			return err
		}
	} else {
		e.emitted[v.id] = true
		e.groupCount++
		if n := v.definition; n != nil && n.kind == stPlaceholder {
			if err = e.render(n.children, index+1, parentDepth+1, 0); err != nil {
				return err
			}
		} else {
			if err = e.appendRaw(text, at); err != nil {
				return err
			}
			if n != nil && n.kind == stChoice {
				e.choices = append(e.choices, SnippetChoice{v.id, append([]string(nil), n.choices...)})
			}
		}
	}
	e.fields[index].End = e.out.Len()
	return nil
}

func (e *snippetExpander) render(nodes []stNode, parent, parentDepth, depth int) error {
	if err := expansionDepth(depth, e.lastOffset); err != nil {
		return err
	}
	for i := range nodes {
		n := &nodes[i]
		e.lastOffset = n.from
		var err error
		switch n.kind {
		case stText:
			err = e.appendRaw(n.text, n.from)
		case stTransform:
			if n.name != "" {
				var text string
				text, err = e.initialTransform(n)
				if err == nil {
					err = e.appendRaw(text, n.from)
				}
			} else {
				if n.index == 0 {
					return expansionError(n.from, "transforms require a positive editable group")
				}
				var compiled *snippetPreparedTransform
				compiled, err = e.compiledTransform(n)
				if err == nil {
					index := len(e.fields) + 1
					err = e.emit(e.groups[n.index], parent, parentDepth, n.from)
					if err == nil {
						e.transforms = append(e.transforms, SnippetFieldTransform{index, compiled.compiled})
					}
				}
			}
		case stVariable:
			if text, provided := e.variable(n.name); provided {
				err = e.appendRaw(text, n.from)
			} else if n.hasDefault {
				err = e.render(n.children, parent, parentDepth, depth+1)
			} else if !knownSnippetVariable(n.name) {
				err = e.emit(e.unknown[n.name], parent, parentDepth, n.from)
			}
		default:
			if n.index != 0 {
				err = e.emit(e.groups[n.index], parent, parentDepth, n.from)
			} else {
				if n.kind == stChoice {
					return expansionError(n.from, "choices require a positive editable group")
				}
				if e.finalSeen {
					return expansionError(n.from, "multiple final selections")
				}
				e.finalSeen = true
				var index int
				index, err = e.field(0, parent, parentDepth, n.from)
				if err != nil {
					return err
				}
				err = e.render(n.children, parent, parentDepth, depth+1)
				e.fields[index].End = e.out.Len()
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
