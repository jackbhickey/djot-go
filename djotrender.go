package djot

import (
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var errNilDjotWriter = errors.New("djot: RenderDjotTo called with a nil writer")

// djotRenderConfig holds djot-rendering options.
type djotRenderConfig struct {
	wrapWidth int
}

// DjotOption configures djot rendering.
type DjotOption func(*djotRenderConfig)

// WithWrapWidth sets line wrapping: n>0 wraps at column n and renders soft
// breaks as spaces; 0 (default) does not wrap and keeps soft breaks as
// newlines; n<0 does not wrap and renders soft breaks as spaces.
func WithWrapWidth(n int) DjotOption {
	return func(c *djotRenderConfig) { c.wrapWidth = n }
}

// RenderDjot renders a parsed document back to djot markup.
func RenderDjot(doc *Doc, opts ...DjotOption) string {
	var cfg djotRenderConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	r := &djotRenderer{doc: doc, wrapWidth: cfg.wrapWidth, startOfLine: true}
	r.renderDocument(doc.Root())
	return strings.Join(r.buffer, "")
}

// RenderDjotTo renders djot markup to w. Returns the first write error.
func RenderDjotTo(w io.Writer, doc *Doc, opts ...DjotOption) error {
	if w == nil {
		return errNilDjotWriter
	}
	// Wrapping rewrites earlier output, so render fully before writing.
	_, err := io.WriteString(w, RenderDjot(doc, opts...))
	return err
}

// djotRenderer is a port of djot.js's DjotRenderer state machine. The buffer
// holds tokens (each inter-word space is its own " " token so wrap can splice
// at it).
type djotRenderer struct {
	doc            *Doc
	buffer         []string
	prefixes       []string
	startOfLine    bool
	endOfPrefix    int
	column         int
	needsBlankLine bool
	// blankMandatory marks the pending blank line as required by the block
	// before it (paragraphs and headings absorb following non-blank lines),
	// overriding tight-list blank suppression.
	blankMandatory bool
	// lastLineBlank records whether the line most recently finished by
	// newline() had no content beyond its prefixes.
	lastLineBlank bool
	wrapWidth     int
	// nextText is the first byte of the *Text sibling immediately following
	// the inline currently being rendered (0 when there is none or it is
	// unknown); conditional escapes at end of a Text value consult it.
	nextText byte
	// openDelims tracks the delimiters of inline containers currently being
	// rendered; a nested container reusing an ancestor's delimiter must be
	// braced or the reparse pairs the delimiters differently.
	openDelims []string
	// lastListMarker is the marker byte of the bullet/task list rendered as
	// the immediately preceding sibling block (0 otherwise); an adjacent list
	// must pick a different marker or the reparse merges the two lists.
	// nextListMarker is the fixed marker of the following sibling when it is
	// a bullet list, which a task list must also dodge.
	lastListMarker byte
	nextListMarker byte
	// lastOrderedDelim is the delimiter form of the ordered list rendered as
	// the immediately preceding sibling block (0 otherwise); an adjacent
	// ordered list must use the other form or the reparse can merge the two.
	lastOrderedDelim byte
	// suppressBlank is set while rendering the items of a tight list: blank
	// lines between an item's blocks would make the list loose on reparse.
	suppressBlank bool
	// inTableCell is set while rendering a cell's inlines: hard breaks there
	// must not emit a real newline.
	inTableCell bool
	// inHeading is set while rendering a heading's inlines.
	inHeading bool
	// intentionalBreak marks the current line as a deliberate thematic
	// break, exempting it from newline()'s break-lookalike guard.
	intentionalBreak bool
	// noBreakGuard disables the guard entirely while emitting verbatim
	// content, which must not be altered.
	noBreakGuard bool
	// prevWasParagraph reports whether the sibling block just rendered was a
	// paragraph, which a following list's marker cannot interrupt.
	prevWasParagraph bool
	// prevAbsorbing reports whether that sibling lazily absorbs following
	// non-blank lines (a paragraph or blockquote), so a thematic break after
	// it needs a preceding blank line.
	prevAbsorbing bool
	// markerAlive carries escape()'s ordered-marker prefix state across
	// adjacent text nodes on the same line.
	markerAlive bool
	// nextIsSymbol reports whether the inline following the one being
	// rendered is a Symbol (its output begins with a bare ':').
	nextIsSymbol bool
}

// atLineStart reports whether output is at the start of a line (past any
// block prefixes), where djot's line-level syntax is live.
func (r *djotRenderer) atLineStart() bool {
	return r.column == 0 || r.column == r.endOfPrefix
}

func (r *djotRenderer) lit(s string) {
	r.buffer = append(r.buffer, s)
	r.column += len(s)
	r.startOfLine = false
}

// escape backslash-escapes djot syntax characters: the always-escaped set,
// conditional '-'/'.'/'!' (escaped before their trigger char), and leading
// '#'/'-'/'+'/':' at the start of a line. next is the character following s
// (an adjacent Text node may supply the trigger char); 0 means unknown, which
// escapes conservatively — over-escaping is round-trip-safe, under-escaping
// is not.
func (r *djotRenderer) escape(s string, next byte) string {
	var b strings.Builder
	b.Grow(len(s))
	atLineStart := r.atLineStart()
	// markerPrefix tracks whether everything so far on the line is
	// alphanumeric from the start of the line, where a following '.' or ')'
	// would complete an ordered-list marker. It carries across adjacent text
	// nodes (which merge on reparse) via r.markerAlive.
	markerPrefix := r.markerAlive
	if atLineStart {
		markerPrefix = true
	}
	defer func() { r.markerAlive = markerPrefix }()
	for i := 0; i < len(s); i++ {
		c := s[i]
		after := next
		if i+1 < len(s) {
			after = s[i+1]
		}
		switch c {
		case '~', '`', '\'', '"', '$', '{', '}', '[', ']', '^', '<', '>', '\\', '*', '_', '|', ':':
			// ':' is always escaped: two of them around a word form a symbol.
			b.WriteByte('\\')
		case '-':
			if after == '-' || after == 0 || (i == 0 && atLineStart) {
				b.WriteByte('\\')
			}
		case '.', ')':
			if (c == '.' && (after == '.' || after == 0)) ||
				(markerPrefix && (i > 0 || !atLineStart)) {
				b.WriteByte('\\')
			}
		case '!':
			if after == '[' || after == 0 {
				b.WriteByte('\\')
			}
		case '#', '+':
			if i == 0 && atLineStart {
				b.WriteByte('\\')
			}
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			// A line-leading "(" still forms a marker: "(1)" is an ordered
			// list enumerator like "1)".
			if !(c == '(' && i == 0 && atLineStart) {
				markerPrefix = false
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (r *djotRenderer) blankline() {
	r.needsBlankLine = true
}

func (r *djotRenderer) doBlankLines() {
	if r.needsBlankLine {
		r.cr()
		if !r.suppressBlank || r.blankMandatory {
			r.newline()
		}
		r.needsBlankLine, r.blankMandatory = false, false
	}
}

// currentLine returns the output emitted since the last line break.
func (r *djotRenderer) currentLine() string {
	start := len(r.buffer)
	for start > 0 && r.buffer[start-1] != "\n" {
		start--
	}
	return strings.Join(r.buffer[start:], "")
}

func (r *djotRenderer) newline() {
	// A finished line made only of '-'/'*' and spaces (e.g. nested empty
	// list markers) would reparse as a thematic break; an invisible empty
	// attribute set breaks the pattern. Intentional break lines are exempt.
	if !r.intentionalBreak && !r.noBreakGuard {
		start := len(r.buffer)
		for start > 0 && r.buffer[start-1] != "\n" {
			start--
		}
		if isThematicBreak(strings.TrimLeft(strings.Join(r.buffer[start:], ""), " \t")) {
			r.buffer = append(r.buffer, "{}")
			r.column += 2
		}
	}
	r.intentionalBreak = false
	// Marker and heading prefixes make endOfPrefix==column unreliable as a
	// blankness signal; inspect the actual line content.
	r.lastLineBlank = strings.TrimSpace(r.currentLine()) == ""
	if r.endOfPrefix == r.column {
		// Remove spaces after the prefix.
		for len(r.buffer) > 0 {
			last := r.buffer[len(r.buffer)-1]
			trimmed := strings.TrimRight(last, " ")
			if trimmed == last && last != "" {
				break
			}
			if trimmed == "" {
				r.buffer = r.buffer[:len(r.buffer)-1]
			} else {
				r.buffer[len(r.buffer)-1] = trimmed
			}
		}
	}
	r.endOfPrefix = 0
	r.column = 0
	r.buffer = append(r.buffer, "\n")
	for _, prefix := range r.prefixes {
		r.buffer = append(r.buffer, prefix)
		r.column += len(prefix)
	}
	if len(r.prefixes) > 0 {
		r.endOfPrefix = r.column
	}
	r.startOfLine = true
}

func (r *djotRenderer) cr() {
	if !r.startOfLine {
		r.newline()
	}
}

func isSpaceOrNewlineToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

func (r *djotRenderer) wrap() {
	if r.wrapWidth <= 0 {
		return
	}
	idx := len(r.buffer) - 1
	if !r.startOfLine && len(r.buffer) > 0 && r.column > r.wrapWidth {
		// Back up to the last breakable space.
		for idx >= 0 {
			tok := r.buffer[idx]
			if tok == " " {
				break
			}
			if isSpaceOrNewlineToken(tok) { // e.g. indentation
				return // can't wrap
			}
			idx--
		}
		if idx < 0 {
			return // no space to break at
		}
	}
	if idx < len(r.buffer)-1 {
		excess := append([]string(nil), r.buffer[idx+1:]...)
		r.buffer = r.buffer[:idx+1]
		if len(r.buffer) > 0 && r.buffer[len(r.buffer)-1] == " " {
			r.buffer = r.buffer[:len(r.buffer)-1] // pop space at end of line
		}
		r.newline()
		r.startOfLine = true
		for _, tok := range excess {
			r.buffer = append(r.buffer, tok)
			r.column += len(tok)
			r.startOfLine = false
		}
	}
}

// popPrefix removes the innermost block prefix. When the current line holds
// nothing but prefixes (e.g. after an empty definition), it is re-emitted
// with the popped set so the next block does not inherit stale indentation.
func (r *djotRenderer) popPrefix() {
	r.prefixes = r.prefixes[:len(r.prefixes)-1]
	if !r.startOfLine || r.column != r.endOfPrefix {
		return
	}
	for len(r.buffer) > 0 && r.buffer[len(r.buffer)-1] != "\n" {
		r.buffer = r.buffer[:len(r.buffer)-1]
	}
	r.column = 0
	r.endOfPrefix = 0
	for _, prefix := range r.prefixes {
		r.buffer = append(r.buffer, prefix)
		r.column += len(prefix)
	}
	if len(r.prefixes) > 0 {
		r.endOfPrefix = r.column
	}
}

func (r *djotRenderer) space() {
	r.wrap()
	r.lit(" ")
}

func (r *djotRenderer) softBreak() {
	if r.wrapWidth == 0 {
		r.newline()
	} else {
		r.space()
	}
}

func (r *djotRenderer) noWrap(fn func()) {
	saved := r.wrapWidth
	r.wrapWidth = -1
	fn()
	r.wrapWidth = saved
}

func (r *djotRenderer) litlines(s string) {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	r.noBreakGuard = true
	for _, line := range lines {
		r.lit(line)
		r.cr()
	}
	r.noBreakGuard = false
}

// verbatimDelim returns the shortest backtick run of at least minticks that
// does not occur in text.
func verbatimDelim(text string, minticks int) string {
	runs := make(map[int]bool)
	run := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
		} else if run > 0 {
			runs[run] = true
			run = 0
		}
	}
	if run > 0 {
		runs[run] = true
	}
	n := minticks
	for runs[n] {
		n++
	}
	return strings.Repeat("`", n)
}

// fenceDelim returns the shortest run of ch (at least 3) that does not start
// a (possibly indented) line of text — an indented run still closes a fence.
func fenceDelim(text string, ch byte) string {
	n := 3
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimLeft(line, " \t")
		run := 0
		for run < len(line) && line[run] == ch {
			run++
		}
		if run >= n {
			n = run + 1
		}
	}
	return strings.Repeat(string(ch), n)
}

func (r *djotRenderer) verbatimNode(text string) {
	if len(r.buffer) > 0 {
		if last := r.buffer[len(r.buffer)-1]; last != "" && last[len(last)-1] == '`' {
			// Directly after another verbatim's closer the delimiter runs
			// would fuse; an invisible empty attribute set separates them.
			r.lit("{}")
		}
	}
	ticks := verbatimDelim(text, 1)
	// The parser trims one space from each end when the content starts or
	// ends with a backtick, so pad both ends symmetrically.
	pad := strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`")
	r.lit(ticks)
	if pad {
		r.lit(" ")
	}
	// Emit newlines through newline() so continuation lines carry the block
	// prefixes; the parser left-trims continuation lines, so the prefix does
	// not leak into the content.
	r.noBreakGuard = true
	// Heading continuation lines keep their "# " prefix (the parser strips
	// it, so content lines starting with '#' keep their hashes) — except
	// lines starting with '\v' or '\f', which the marker strip would eat
	// and which survive only on lazy (prefix-less) continuation lines.
	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			if r.inHeading && len(r.prefixes) > 0 && line != "" &&
				(line[0] == '\v' || line[0] == '\f') {
				saved := r.prefixes
				r.prefixes = saved[:len(saved)-1]
				r.newline()
				r.prefixes = saved
			} else {
				r.newline()
			}
		}
		if line != "" {
			r.lit(line)
		}
	}
	r.noBreakGuard = false
	if pad {
		r.lit(" ")
	}
	r.lit(ticks)
}

func (r *djotRenderer) renderBlocks(children []Block) {
	defer func(saved, savedAbs bool) { r.prevWasParagraph, r.prevAbsorbing = saved, savedAbs }(r.prevWasParagraph, r.prevAbsorbing)
	r.prevWasParagraph = false
	r.prevAbsorbing = false
	for i, child := range children {
		if i > 0 && r.needsListSeparator(children[i-1], child) {
			// Adjacent lists that would merge on reparse are kept apart by a
			// discarded empty attribute line (no blank line after it, which
			// would loosen a tight enclosing list).
			r.doBlankLines()
			r.lit("{}")
			r.cr()
		}
		r.nextListMarker = 0
		if i+1 < len(children) {
			if b, ok := children[i+1].(*BulletList); ok {
				r.nextListMarker = '-'
				if b.Marker != 0 {
					r.nextListMarker = b.Marker
				}
			}
		}
		r.renderNode(child)
		if i+1 < len(children) {
			if _, isHeading := child.(*Heading); isHeading {
				if _, nextPara := children[i+1].(*Paragraph); nextPara {
					// A heading absorbs plain text lines (only other block
					// constructs interrupt it), so the blank before a
					// following paragraph must survive tight suppression.
					r.blankMandatory = true
				}
			}
		}
		_, r.prevWasParagraph = child.(*Paragraph)
		switch child.(type) {
		case *Paragraph, *BlockQuote:
			r.prevAbsorbing = true
		default:
			r.prevAbsorbing = false
		}
		switch child.(type) {
		case *BulletList, *TaskList:
			// the list handler recorded its marker in lastListMarker
			r.lastOrderedDelim = 0
		case *OrderedList:
			// the list handler recorded its style and delimiter
			r.lastListMarker = 0
		default:
			r.lastListMarker = 0
			r.lastOrderedDelim = 0
		}
	}
}

// markerLineBecomesBreak reports whether any marker-strip suffix of a
// nested-marker line reparses as a thematic break: the parser consumes
// leading markers as nested items and dispatches each remainder in fresh
// block position, where thematic breaks are checked before list markers.
func markerLineBecomesBreak(s string) bool {
	for {
		s = strings.TrimLeft(s, " ")
		if s == "" {
			return false
		}
		if isThematicBreak(s) {
			return true
		}
		if strings.HasPrefix(s, "> ") {
			s = s[2:]
			continue
		}
		if _, after, ok := bulletListMarker(s); ok {
			if len(after) >= 4 && after[0] == '[' && after[2] == ']' && after[3] == ' ' {
				after = after[4:]
			}
			s = after
			continue
		}
		if _, _, after, ok := orderedListMarker(s); ok {
			s = after
			continue
		}
		return false
	}
}

// isListBlock reports whether b is a list-family block. A blank line
// rendered directly before such a block is absorbed by it on reparse and
// does not count toward the enclosing list's looseness; neither does a blank
// between items when the previous item ends with one.
func isListBlock(b Block) bool {
	switch b.(type) {
	case *BulletList, *OrderedList, *TaskList, *DefinitionList:
		return true
	}
	return false
}

// listNeedsMarkerBlank reports whether a loose list's natural rendering
// would contain no blank line that counts toward looseness — every blank it
// emits either precedes a sublist or separates items after a trailing
// sublist — so the writer must place one counting blank directly after the
// first item's marker.
func listNeedsMarkerBlank(list Block) bool {
	var items [][]Block
	tight := false
	switch n := list.(type) {
	case *BulletList:
		tight = n.Tight
		for _, item := range n.Items {
			items = append(items, item.Children)
		}
	case *OrderedList:
		tight = n.Tight
		for _, item := range n.Items {
			items = append(items, item.Children)
		}
	case *TaskList:
		tight = n.Tight
		for _, item := range n.Items {
			items = append(items, item.Children)
		}
	}
	if tight {
		return false
	}
	for i, children := range items {
		if i > 0 {
			prev := items[i-1]
			if len(prev) == 0 || !isListBlock(prev[len(prev)-1]) {
				return false // the separator blank before this item counts
			}
		}
		for j := 1; j < len(children); j++ {
			if !isListBlock(children[j]) {
				return false // the blank before this block counts
			}
		}
	}
	return true
}

// markerBlank, when needed is true, emits the counting blank line that marks
// a list loose, directly after an item marker (see listNeedsMarkerBlank).
// When the item's first block is itself a list — which would absorb the
// blank on reparse — an empty attribute set on its own line follows the
// blank so the parser sees non-list content after it.
func (r *djotRenderer) markerBlank(needed bool, children []Block) {
	if !needed {
		return
	}
	if len(children) == 0 || isListBlock(children[0]) {
		// An empty item has nothing to flush a flagged blank before (and a
		// trailing blank alone never counts), and a list would absorb it;
		// either way a discarded attribute line after the blank makes it
		// count.
		r.cr()
		r.newline()
		r.lit("{}")
		r.cr()
		return
	}
	r.blankline()
}

// needsListSeparator reports whether cur, rendered directly after prev,
// would merge into it on reparse: adjacent definition lists always do, and a
// bullet list whose fixed marker matches the just-rendered list's marker.
func (r *djotRenderer) needsListSeparator(prev, cur Block) bool {
	if _, ok := prev.(*DefinitionList); ok {
		_, ok2 := cur.(*DefinitionList)
		return ok2
	}
	if b, ok := cur.(*BulletList); ok && r.lastListMarker != 0 {
		marker := byte('-')
		if b.Marker != 0 {
			marker = b.Marker
		}
		return marker == r.lastListMarker
	}
	return false
}

// pickTaskListMarker returns a bullet for a task list (whose marker is not
// recorded in the AST): it must differ from the preceding sibling list's
// marker, or the two lists merge on reparse, and from a following bullet
// list's fixed marker, which cannot yield.
func (r *djotRenderer) pickTaskListMarker() string {
	for _, c := range []byte{'-', '+', '*'} {
		if c != r.lastListMarker && c != r.nextListMarker {
			return string(c)
		}
	}
	return "-"
}

func (r *djotRenderer) renderInlines(children []Inline) {
	for i, child := range children {
		r.nextText = 0
		r.nextIsSymbol = false
		if i+1 < len(children) {
			if t, ok := children[i+1].(*Text); ok && t.Value != "" {
				r.nextText = t.Value[0]
			}
			_, r.nextIsSymbol = children[i+1].(*Symbol)
		}
		r.renderNode(child)
		// Adjacent dashes would fuse and regroup on reparse (e.g. two en
		// dashes read back as one em dash plus a hyphen); an empty attribute
		// set keeps the runs apart.
		if isDashInline(child) && i+1 < len(children) &&
			(isDashInline(children[i+1]) || r.nextText == '-') {
			r.lit("{}")
		}
		// A soft break directly after another break (soft or hard) would
		// form a blank line and split the block; an empty attribute set
		// keeps the line visible.
		if i+1 < len(children) {
			_, isSoft := child.(*SoftBreak)
			_, isHard := child.(*HardBreak)
			if isSoft || isHard {
				if _, ok := children[i+1].(*SoftBreak); ok {
					r.lit("{}")
				}
			}
		}
		// Heading and block-marker lines trim trailing whitespace, so
		// shield text that ends in whitespace before a soft break. (An
		// empty attribute set is dropped on reparse, so over-shielding on
		// continuation lines is harmless.)
		if i+1 < len(children) {
			if _, ok := children[i+1].(*SoftBreak); ok {
				if t, ok := child.(*Text); ok && t.Value != "" &&
					strings.IndexAny(t.Value[len(t.Value)-1:], " \t\f\v") == 0 &&
					t.Attributes().Len() == 0 {
					r.lit("{}")
				}
			}
		}
	}
	r.nextText = 0
	if len(children) > 0 {
		r.wrap()
	}
}

func isDashInline(node Node) bool {
	switch node.(type) {
	case *EmDash, *EnDash:
		return true
	}
	return false
}

// renderBlockInlines renders a block's inline content. A trailing space or
// tab on the final text would be trimmed at the end of the line on reparse;
// an empty attribute set is invisible and shields it.
func (r *djotRenderer) renderBlockInlines(children []Inline) {
	if len(children) > 0 {
		if _, ok := children[0].(*SoftBreak); ok {
			// A leading newline would be a blank line (or a bare "{}" line
			// would be a discarded block attribute); two empty attribute
			// sets are invisible inline content.
			r.lit("{}{}")
		}
	}
	r.renderInlines(children)
	if len(children) == 0 {
		return
	}
	switch last := children[len(children)-1].(type) {
	case *Text:
		if last.Value != "" &&
			strings.IndexAny(last.Value[len(last.Value)-1:], " \t\f\v") == 0 &&
			last.Attributes().Len() == 0 {
			r.lit("{}")
		}
	case *SoftBreak:
		// The block would otherwise end at the newline, dropping the break.
		r.lit("{}")
	}
}

func (r *djotRenderer) renderNode(node Node) {
	// Some blocks cannot directly follow a paragraph even inside a tight
	// list item: a nested list's marker cannot interrupt the paragraph, and
	// an attribute line would read as paragraph continuation.
	var attrEntries []Attribute
	if _, ok := node.(Block); ok && node.Attributes().Len() > 0 {
		attrEntries = node.Attributes().Entries()
		if h, ok := node.(*Heading); ok {
			attrEntries = headingAttrsWithoutAutoID(h, attrEntries)
		}
	}
	cannotFollowParagraph := len(attrEntries) > 0
	switch node.(type) {
	case *BulletList, *OrderedList, *TaskList, *DefinitionList:
		cannotFollowParagraph = true
	}
	if cannotFollowParagraph && r.prevWasParagraph {
		saved := r.suppressBlank
		r.suppressBlank = false
		r.doBlankLines()
		r.suppressBlank = saved
	} else {
		r.doBlankLines()
	}
	if sec, ok := node.(*Section); ok {
		r.renderSection(sec)
		return
	}
	if len(attrEntries) > 0 {
		r.renderAttributes(attrEntries, true)
		r.cr()
	}
	r.dispatch(node)
	if _, ok := node.(Inline); ok && node.Attributes().Len() > 0 {
		r.renderAttributes(node.Attributes().Entries(), false)
	}
}

func (r *djotRenderer) renderAttributes(entries []Attribute, block bool) {
	if len(entries) == 0 {
		return
	}
	r.lit("{")
	if block {
		r.prefixes = append(r.prefixes, " ")
	}
	if first := entries[0]; first.Value == "" && first.Key != "id" && first.Key != "class" &&
		(first.Key[0] == '_' || first.Key[0] == '-') {
		// A bare key opening with '_' or '-' directly after '{' would read
		// as a braced inline container; a leading space keeps it an
		// attribute list.
		r.lit(" ")
	}
	for i, attr := range entries {
		if i > 0 {
			r.space()
		}
		switch {
		case attr.Key == "id" && isAttrShorthandWord(attr.Value):
			r.lit("#" + attr.Value)
		case attr.Key == "class" && attr.Value != "" &&
			attr.Value == strings.Join(strings.Fields(attr.Value), " ") &&
			allAttrShorthandWords(strings.Fields(attr.Value)):
			for j, class := range strings.Fields(attr.Value) {
				if j > 0 {
					r.space()
				}
				r.lit("." + class)
			}
		case attr.Value == "" && attr.Key != "id" && attr.Key != "class":
			// The parser does not accept an empty quoted value; a bare key
			// parses back to the same empty-valued attribute.
			r.lit(attr.Key)
		default:
			r.lit(attr.Key + `="`)
			r.lit(r.escape(attr.Value, '"'))
			r.lit(`"`)
		}
	}
	if block {
		r.popPrefix()
	}
	r.lit("}")
}

// isAttrShorthandWord reports whether s can follow "#" or "." in an
// attribute list; anything else must use the key="value" form.
func isAttrShorthandWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}

func allAttrShorthandWords(words []string) bool {
	for _, w := range words {
		if !isAttrShorthandWord(w) {
			return false
		}
	}
	return true
}

func (r *djotRenderer) renderDocument(root *Document) {
	if root == nil {
		return
	}
	r.renderBlocks(root.Children)
	r.prefixes = nil
	r.cr()
	refs := r.doc.References()
	implicit := implicitHeadingRefs(root)
	labels := make([]string, 0, len(refs))
	for label, ref := range refs {
		if dest, ok := implicit[label]; ok && dest == ref.Destination {
			continue // auto-created heading anchor; recreated on reparse
		}
		labels = append(labels, label)
	}
	if len(labels) > 0 {
		sort.Strings(labels)
		r.blankline()
		for _, label := range labels {
			r.renderReference(label, refs[label])
		}
	}
	r.prefixes = nil
	r.cr()
}

func (r *djotRenderer) renderReference(label string, ref *Reference) {
	r.doBlankLines()
	if ref.Attributes.Len() > 0 {
		r.renderAttributes(ref.Attributes.Entries(), true)
		r.cr()
	}
	r.lit("[" + label + "]:")
	r.prefixes = append(r.prefixes, "  ")
	r.space()
	// The parser trims whitespace from the destination line, so edge
	// whitespace cannot survive a reparse; emit the trimmed fixed point.
	r.lit(strings.TrimSpace(ref.Destination))
	r.wrap()
	r.popPrefix()
	r.blankline()
}

// renderSection renders a section transparently, merging any non-default
// section attributes into the leading heading's block-attribute line so the
// custom id survives a round trip.
func (r *djotRenderer) renderSection(sec *Section) {
	children := sec.Children
	if len(children) > 0 {
		if h, ok := children[0].(*Heading); ok {
			if entries := sectionHeadingAttrs(sec, h); entries != nil {
				r.renderAttributes(entries, true)
				r.cr()
				r.dispatch(h)
				children = children[1:]
			}
		}
	}
	for _, child := range children {
		r.renderNode(child)
	}
}

// sectionHeadingAttrs returns the merged attribute line for a section's
// heading, or nil when the section carries nothing beyond the auto-assigned
// slug id (heading-only attributes then take the generic path).
func sectionHeadingAttrs(sec *Section, h *Heading) []Attribute {
	secAttrs := sec.Attributes().Entries()
	if len(secAttrs) == 0 {
		return nil
	}
	id, hasID := sec.Attributes().Lookup("id")
	var merged []Attribute
	if hasID && !isAutoSlugID(id, headingSlug(h)) {
		merged = append(merged, Attribute{Key: "id", Value: id})
	}
	for _, attr := range secAttrs {
		if attr.Key != "id" {
			merged = append(merged, attr)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return append(merged, h.Attributes().Entries()...)
}

// implicitHeadingRefs recomputes the label → destination map the parser
// auto-registers for section headings (see registerHeadingRefs in parse.go),
// so those entries can be omitted from the serialized reference definitions.
func implicitHeadingRefs(root *Document) map[string]string {
	refs := make(map[string]string)
	walkRead(root, func(node Node) {
		sec, ok := node.(*Section)
		if !ok {
			return
		}
		id := sec.Attributes().Get("id")
		if id == "" {
			return
		}
		for _, child := range sec.Children {
			if h, ok := child.(*Heading); ok {
				var b strings.Builder
				for _, inline := range h.Children {
					appendInlineSlugText(&b, inline)
				}
				if label := b.String(); label != "" {
					if _, exists := refs[label]; !exists {
						refs[label] = "#" + id
					}
				}
				break
			}
		}
	})
	return refs
}

// headingAttrsWithoutAutoID drops an "id" attribute the parser auto-assigned
// to a heading (the slug, possibly with a "-N" dedup suffix) — it is
// recreated on reparse, and an attribute line for it would change structure.
func headingAttrsWithoutAutoID(h *Heading, entries []Attribute) []Attribute {
	slug := headingSlug(h)
	kept := make([]Attribute, 0, len(entries))
	for _, attr := range entries {
		if attr.Key == "id" && isAutoSlugID(attr.Value, slug) {
			continue
		}
		kept = append(kept, attr)
	}
	return kept
}

func isAutoSlugID(id, slug string) bool {
	// Anonymous headings (slug "s") always get a counter, so a bare "s" is a
	// custom id.
	if id == slug && slug != "s" {
		return true
	}
	if !strings.HasPrefix(id, slug+"-") {
		return false
	}
	suffix := id[len(slug)+1:]
	if suffix == "" {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	return true
}

// headingSlug computes the auto id the parser would assign to the heading
// (mirrors autoID in section.go over the typed AST, without deduplication).
func headingSlug(h *Heading) string {
	var text strings.Builder
	for _, child := range h.Children {
		appendInlineSlugText(&text, child)
	}
	var b strings.Builder
	prevWasSpace := false
	for _, c := range text.String() {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevWasSpace && b.Len() > 0 {
				b.WriteByte('-')
			}
			prevWasSpace = true
		} else if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' {
			b.WriteRune(c)
			prevWasSpace = false
		} else {
			prevWasSpace = false
		}
	}
	id := strings.TrimRight(b.String(), "-")
	if id == "" {
		return "s"
	}
	return id
}

func appendInlineSlugText(b *strings.Builder, node Node) {
	switch n := node.(type) {
	case *Text:
		b.WriteString(n.Value)
		return
	case *SoftBreak, *HardBreak, *NonBreakingSpace:
		b.WriteByte(' ')
		return
	case *Verbatim:
		b.WriteString(n.Text)
		return
	case *InlineMath:
		b.WriteString(n.Text)
		return
	case *DisplayMath:
		b.WriteString(n.Text)
		return
	case *RawInline:
		b.WriteString(n.Text)
		return
	case *Symbol:
		// The parser's collectParseText contributes nothing for symbols.
		return
	}
	forEachChild(node, func(child Node) {
		appendInlineSlugText(b, child)
	})
}

func (r *djotRenderer) dispatch(node Node) {
	switch n := node.(type) {
	case *Document:
		r.renderDocument(n)
	case *Section:
		r.renderSection(n)
	case *Paragraph:
		if len(n.Children) == 0 {
			// A single "{}" line is a discarded block attribute; two of them
			// reparse as an empty paragraph.
			r.lit("{}{}")
			r.blankline()
			r.blankMandatory = true
			return
		}
		// When the paragraph text starts with '{', the block parser tries a
		// multi-line attribute; unindented continuation lines make that scan
		// fail in a way that marks the braces literal. Indented continuation
		// lines are left-trimmed on parse, so the indent is invisible.
		indent := startsWithBracedContainer(n.Children)
		if indent {
			r.prefixes = append(r.prefixes, " ")
		}
		paraStart := len(r.buffer)
		r.renderBlockInlines(n.Children)
		r.guardAttrLookalike(paraStart)
		if indent {
			r.popPrefix()
		}
		r.blankline()
		r.blankMandatory = true
	case *Heading:
		hashes := strings.Repeat("#", n.Level)
		r.lit(hashes + " ")
		r.prefixes = append(r.prefixes, hashes+" ")
		r.endOfPrefix = r.column
		r.inHeading = true
		r.renderBlockInlines(n.Children)
		r.inHeading = false
		r.popPrefix()
		r.blankline()
	case *ThematicBreak:
		// Directly after a list marker the stars would fuse with it into one
		// longer break line (break lines may mix '*' and '-'); keep the marker
		// line valid with an invisible empty attribute set and start a fresh
		// (item-indented) line.
		if !r.startOfLine {
			r.lit("{}")
			r.cr()
		}
		inListItem := false
		for _, p := range r.prefixes {
			if strings.Trim(p, " ") == "" && p != "" {
				inListItem = true
			}
		}
		switch {
		case inListItem && r.suppressBlank && r.prevAbsorbing:
			// In a tight item a break cannot directly follow a paragraph or
			// blockquote (they absorb following lines), yet a counting blank
			// would loosen the list: a bullet-lookalike spaced break after a
			// blank is skipped by the tightness scan and still parses as a
			// break.
			if !r.lastLineBlank {
				r.cr()
				r.newline()
			}
			r.lit("* * * * *")
		case inListItem:
			// Loose item: the natural blank before the break counts (an
			// unspaced break is not marker-like), consistent with the
			// list's looseness.
			r.lit("***")
		case r.lastListMarker == '*':
			// After a star list, "* ..." would continue the list as an item.
			r.lit("- - - - -")
		default:
			r.lit("* * * * *")
		}
		r.intentionalBreak = true
		r.blankline()
	case *CodeBlock:
		ticks := fenceDelim(n.Text, '`')
		if strings.ContainsAny(n.Language, " `") {
			// A backtick fence's info string may not contain spaces or
			// backticks; a tilde fence has no such restriction.
			ticks = fenceDelim(n.Text, '~')
		}
		r.lit(ticks)
		if n.Language != "" {
			r.lit(" " + n.Language)
		}
		r.cr()
		r.litlines(n.Text)
		r.cr()
		r.lit(ticks)
		r.blankline()
	case *RawBlock:
		ticks := fenceDelim(n.Text, '`')
		if strings.ContainsRune(n.Format, '`') {
			// A backtick fence's info string may not contain backticks.
			ticks = fenceDelim(n.Text, '~')
		}
		r.lit(ticks)
		r.lit(" =" + n.Format)
		r.cr()
		r.litlines(n.Text)
		r.cr()
		r.lit(ticks)
		r.blankline()
	case *BlockQuote:
		// Blank lines inside the quote carry the "> " prefix, so they cannot
		// make an enclosing tight list loose.
		savedSuppress := r.suppressBlank
		r.suppressBlank = false
		r.prefixes = append(r.prefixes, "> ")
		r.lit("> ")
		r.endOfPrefix = r.column
		r.renderBlocks(n.Children)
		r.suppressBlank = savedSuppress
		r.popPrefix()
		r.blankline()
		// A blockquote lazily absorbs following non-blank lines, so its
		// trailing blank must survive tight suppression.
		r.blankMandatory = true
	case *Div:
		fence := strings.Repeat(":", divFenceLen(n))
		r.lit(fence)
		r.cr()
		r.renderBlocks(n.Children)
		// A pending blank belonging to the last inner block is void once
		// the fence closes; letting it (or its mandatory flag) leak would
		// emit a tightness-breaking blank after the div.
		r.needsBlankLine, r.blankMandatory = false, false
		r.cr()
		r.lit(fence)
		r.blankline()
	case *BulletList:
		marker := "-"
		if n.Marker != 0 {
			marker = string(n.Marker)
		}
		markerBlank := listNeedsMarkerBlank(n)
		savedSuppress := r.suppressBlank
		r.suppressBlank = n.Tight
		for i, item := range n.Items {
			if i > 0 {
				r.cr()
				if !n.Tight {
					r.newline()
				}
			}
			line := r.currentLine()
			// Look past block prefixes such as "> ": only the content
			// portion decides break danger. Markers replace continuation
			// prefixes on their line, so strip prefixes only while they
			// match.
			for _, p := range r.prefixes {
				if !strings.HasPrefix(line, p) {
					break
				}
				line = line[len(p):]
			}
			if markerLineBecomesBreak(strings.TrimLeft(line, " ") + marker) {
				// Some marker-strip suffix of the finished line would
				// reparse as a thematic break; start this nested list on a
				// continuation line instead. A checkbox left bare at the
				// line end would lose its trailing space (and its
				// task-ness) — shield it first.
				if strings.HasSuffix(r.currentLine(), "] ") {
					r.lit("{}")
				}
				r.cr()
			}
			r.lit(marker)
			r.needsBlankLine, r.blankMandatory = false, false
			r.space()
			r.prefixes = append(r.prefixes, strings.Repeat(" ", len(marker)+1))
			r.endOfPrefix = r.column
			r.markerBlank(markerBlank && i == 0, item.Children)
			r.renderBlocks(item.Children)
			r.popPrefix()
		}
		r.suppressBlank = savedSuppress
		r.lastListMarker = marker[0]
		// Always pending, even for loose lists: an empty last item emits no
		// content, and a following sibling must not land on its line.
		r.blankline()
	case *OrderedList:
		start := n.Start
		// "0." is a valid decimal marker; for letter/roman styles there is no
		// zeroth marker, so 0 there means an unset start.
		if start == 0 && n.Style != ListDecimal {
			start = 1
		}
		// An adjacent ordered list can merge into the previous one whenever
		// their delimiters match (even across styles, when the markers happen
		// to read as continuations), so alternate the delimiter form.
		delim := byte('.')
		if r.lastOrderedDelim == '.' {
			delim = ')'
		}
		markerBlank := listNeedsMarkerBlank(n)
		savedSuppress := r.suppressBlank
		r.suppressBlank = n.Tight
		for i, item := range n.Items {
			if i > 0 {
				r.cr()
				if !n.Tight {
					r.newline()
				}
			}
			num := formatListNumber(start+i, n.Style)
			if i == 1 && alphaRomanAmbiguous(formatListNumber(start, n.Style)) &&
				(n.Style == ListAlphaLower || n.Style == ListAlphaUpper) {
				// If the first marker also reads as a roman numeral, the
				// reparse settles the whole list as roman unless a later
				// marker is alpha-only. Item values beyond the first are not
				// checked, so bump the second marker to a non-roman letter.
				num = nextNonRomanLetter(num)
			}
			marker := num + string(delim)
			r.lit(marker)
			r.needsBlankLine, r.blankMandatory = false, false
			r.space()
			r.prefixes = append(r.prefixes, strings.Repeat(" ", len(marker)+1))
			r.endOfPrefix = r.column
			if len(item.Children) == 0 || (markerBlank && i == 0) {
				// "1." without its trailing space is not a list item;
				// invisible empty attributes keep the space from being trimmed.
				r.lit("{}")
			}
			r.markerBlank(markerBlank && i == 0, item.Children)
			r.renderBlocks(item.Children)
			r.popPrefix()
		}
		r.suppressBlank = savedSuppress
		r.lastOrderedDelim = delim
		r.blankline()
	case *TaskList:
		marker := r.pickTaskListMarker()
		markerBlank := listNeedsMarkerBlank(n)
		savedSuppress := r.suppressBlank
		r.suppressBlank = n.Tight
		for i, item := range n.Items {
			if i > 0 {
				r.cr()
				if !n.Tight {
					r.newline()
				}
			}
			r.needsBlankLine, r.blankMandatory = false, false
			if item.Checked {
				r.lit(marker + " [X]")
			} else {
				r.lit(marker + " [ ]")
			}
			r.space()
			// The parser's task-item content indent is the marker plus one
			// space (the checkbox is content), so continuation lines use a
			// two-space prefix; djot.js accepts any indent past the marker.
			r.prefixes = append(r.prefixes, "  ")
			r.endOfPrefix = r.column
			if len(item.Children) == 0 || (markerBlank && i == 0) {
				// "- [X]" without its trailing space is not a task item;
				// invisible empty attributes keep the space from being trimmed.
				r.lit("{}")
			}
			r.markerBlank(markerBlank && i == 0, item.Children)
			r.renderBlocks(item.Children)
			r.popPrefix()
		}
		r.suppressBlank = savedSuppress
		r.lastListMarker = marker[0]
		r.blankline()
	case *DefinitionList:
		savedSuppress := r.suppressBlank
		r.suppressBlank = n.Tight
		first := true
		open := false
		for _, child := range n.Children {
			if term, ok := child.(*Term); ok {
				if open {
					r.popPrefix()
					open = false
				}
				if !first {
					r.cr()
					if !n.Tight {
						r.newline()
					}
				}
				first = false
				r.lit(":")
				r.needsBlankLine, r.blankMandatory = false, false
				r.space()
				r.prefixes = append(r.prefixes, "  ")
				r.endOfPrefix = r.column
				open = true
				r.renderNode(term)
			} else {
				// An empty definition renders nothing; entering it anyway
				// would flush a pending blank line into thin air, leaving a
				// stray trailing blank.
				if d, ok := child.(*Definition); ok && len(d.Children) == 0 {
					continue
				}
				r.renderNode(child)
			}
		}
		if open {
			r.popPrefix()
		}
		r.suppressBlank = savedSuppress
	case *Term:
		r.renderBlockInlines(n.Children)
		r.blankline()
		// A term lazily absorbs following non-blank lines like a paragraph,
		// so the blank separating it from its definition must survive tight
		// suppression.
		r.blankMandatory = true
	case *Definition:
		r.renderBlocks(n.Children)
	case *Table:
		r.renderTable(n)
	case *Footnote:
		r.lit("[^" + n.Label + "]:")
		r.space()
		r.needsBlankLine, r.blankMandatory = false, false
		r.prefixes = append(r.prefixes, "  ")
		r.endOfPrefix = r.column
		r.renderBlocks(n.Children)
		r.popPrefix()
		r.blankline()
	case *Text:
		// A leading space or tab at the start of a line is eaten on reparse;
		// an empty attribute set is invisible and shields it.
		if r.atLineStart() && (strings.IndexAny(n.Value, " \t") == 0 ||
			(r.inHeading && strings.IndexAny(n.Value, "\f\v") == 0)) {
			// Space and tab are eaten at any line start; \f and \v survive in
			// paragraphs but are eaten after a heading marker.
			r.lit("{}")
		}
		r.renderTextValue(n.Value, r.nextText)
	case *SoftBreak:
		r.softBreak()
	case *HardBreak:
		if len(r.buffer) > 0 && !r.atLineStart() {
			if last := r.buffer[len(r.buffer)-1]; last != "" &&
				(last[len(last)-1] == ' ' || last[len(last)-1] == '\t') {
				// Whitespace before the break's backslash is trimmed with
				// the line end; an invisible empty attribute set bounds the
				// kept whitespace, and a fresh (trimmed) space precedes the
				// backslash.
				r.lit("{} ")
			}
		}
		r.lit("\\")
		// Inside a table cell the backslash at end of row IS the break; a
		// real newline would split the row.
		if !r.inTableCell {
			r.newline()
		}
	case *NonBreakingSpace:
		r.lit("\\ ")
		if r.nextText == 0 {
			// With no following text on the line, end-of-line trimming
			// would eat the space and leave a hard break; an invisible
			// empty attribute set keeps it.
			r.lit("{}")
		}
	case *Emphasis:
		r.inlineContainer(n.Children, "_", false)
	case *Strong:
		r.inlineContainer(n.Children, "*", false)
	case *Superscript:
		r.inlineContainer(n.Children, "^", false)
	case *Subscript:
		r.inlineContainer(n.Children, "~", false)
	case *SingleQuoted:
		r.inlineContainer(n.Children, "'", false)
	case *DoubleQuoted:
		r.inlineContainer(n.Children, `"`, false)
	case *Mark:
		r.inlineContainer(n.Children, "=", true)
	case *Delete:
		r.inlineContainer(n.Children, "-", true)
	case *Insert:
		r.inlineContainer(n.Children, "+", true)
	case *Span:
		r.lit("[")
		r.renderInlines(n.Children)
		r.lit("]")
		// Without an attribute list the brackets reparse as literal text.
		if n.Attributes().Len() == 0 {
			r.lit("{}")
		}
	case *Link:
		r.renderLinkOrImage(n.Children, n.Destination, n.DestinationSet, false)
	case *Image:
		r.renderLinkOrImage(n.Children, n.Destination, n.DestinationSet, true)
	case *Verbatim:
		r.verbatimNode(n.Text)
	case *InlineMath:
		r.lit("$")
		r.verbatimNode(n.Text)
	case *DisplayMath:
		r.lit("$$")
		r.verbatimNode(n.Text)
	case *RawInline:
		r.verbatimNode(n.Text)
		r.lit("{=" + n.Format + "}")
	case *Symbol:
		r.lit(":" + n.Name + ":")
	case *FootnoteReference:
		atStart := r.atLineStart()
		r.lit("[^" + footnoteLabel(n.Label) + "]")
		// At the start of a line, a directly following ':' would make this
		// reparse as a footnote definition. Text escapes its ':' already;
		// a following Symbol supplies a bare one, so bound the reference
		// with an empty attribute set.
		if atStart && r.nextIsSymbol {
			r.lit("{}")
		}
	case *Ellipsis:
		r.lit("...")
	case *EmDash:
		atStart := r.atLineStart()
		r.lit("---")
		// A line of dashes reparses as a thematic break; an empty attribute
		// set attached to the dash is invisible and breaks the pattern.
		if atStart {
			r.lit("{}")
		}
	case *EnDash:
		atStart := r.atLineStart()
		r.lit("--")
		if atStart {
			r.lit("{}")
		}
	}
}

// renderTextValue emits a Text value, preserving space runs (each inter-word
// space is its own token so wrap can splice there). next is the first output
// byte following the value, for conditional escapes.
func (r *djotRenderer) renderTextValue(value string, next byte) {
	if value == "" {
		return
	}
	segs := strings.Split(value, " ")
	for i, seg := range segs {
		if i > 0 {
			r.space()
		}
		if seg != "" {
			segNext := byte(' ')
			if i == len(segs)-1 {
				segNext = next
			}
			r.lit(r.escape(seg, segNext))
		}
	}
}

// guardAttrLookalike checks a just-rendered single-line paragraph: if the
// whole line parses as a block attribute (e.g. "{_0 _}"), the block parser
// would eat it; an empty attribute set in front breaks that reading.
func (r *djotRenderer) guardAttrLookalike(start int) {
	text := strings.Join(r.buffer[start:], "")
	if len(text) < 2 || text[0] != '{' {
		return
	}
	// Mirror the block parser's tryBlockAttr: join lines (continuations are
	// whitespace-trimmed) until one ends with '}'.
	lines := strings.Split(text, "\n")
	buf := strings.TrimRight(lines[0], " \t")
	if !strings.HasSuffix(buf, "}") {
		joined := false
		for _, ln := range lines[1:] {
			buf += " " + strings.TrimSpace(ln)
			buf = strings.TrimRight(buf, " \t")
			if strings.HasSuffix(buf, "}") {
				joined = true
				break
			}
		}
		if !joined {
			return
		}
	}
	if attrs, _ := parseAttrsOrdered(buf[1 : len(buf)-1]); attrs == nil {
		return
	}
	r.buffer = append(r.buffer[:start],
		append([]string{"{}"}, r.buffer[start:]...)...)
	if len(lines) == 1 {
		r.column += 2
	}
}

// startsWithBracedContainer reports whether the paragraph's rendered text
// will begin with '{' — an inline container that takes braces at the start of
// a block (empty, whitespace-adjacent, or always-braced content).
func startsWithBracedContainer(children []Inline) bool {
	if len(children) == 0 {
		return false
	}
	var inner []Inline
	switch n := children[0].(type) {
	case *Mark, *Delete, *Insert:
		return true
	case *SoftBreak:
		return true // rendered as a leading "{}{}"
	case *Text:
		// A leading space or tab is shielded with "{}".
		return strings.IndexAny(n.Value, " \t") == 0
	case *Emphasis:
		inner = n.Children
	case *Strong:
		inner = n.Children
	case *Superscript:
		inner = n.Children
	case *Subscript:
		inner = n.Children
	case *SingleQuoted:
		inner = n.Children
	case *DoubleQuoted:
		inner = n.Children
	default:
		return false
	}
	return len(inner) == 0 || beginsWithWhitespace(inner) || endsWithWhitespace(inner)
}

// footnoteLabel makes a label safe to embed in an inline "[^...]" reference:
// a trailing backslash would escape the closing bracket, so a space (removed
// again when the label is normalized on reparse) is appended after it.
// Definition labels are raw block text and need no such protection.
func footnoteLabel(label string) string {
	if strings.HasSuffix(label, "\\") {
		return label + " "
	}
	return label
}

func (r *djotRenderer) renderTable(table *Table) {
	var caption *Caption
	var rows []*TableRow
	for _, child := range table.Children {
		switch c := child.(type) {
		case *Caption:
			if caption == nil {
				caption = c
			}
		case *TableRow:
			rows = append(rows, c)
		}
	}
	// Headness lives on the cells in the typed AST.
	isHeader := func(row *TableRow) bool {
		return len(row.Cells) > 0 && row.Cells[0].Header
	}
	renderSeparator := func(headRow *TableRow) {
		for j, cell := range headRow.Cells {
			if j == 0 {
				r.lit("|")
			}
			switch cell.Alignment {
			case AlignLeft:
				r.lit(":--")
			case AlignRight:
				r.lit("--:")
			case AlignCenter:
				r.lit(":-:")
			default:
				r.lit("---")
			}
			r.lit("|")
		}
		r.cr()
	}
	if len(rows) == 0 {
		// A separator-only table parses to a table with no rows; "|-|" is the
		// shortest input that reparses the same way.
		r.lit("|-|")
		r.cr()
	}
	if len(rows) > 0 && !isHeader(rows[0]) {
		for _, cell := range rows[0].Cells {
			if cell.Alignment != AlignDefault {
				// Alignments with no header row are carried by a leading
				// separator line.
				renderSeparator(rows[0])
				break
			}
		}
	}
	for _, row := range rows {
		rowStart := len(r.buffer)
		for j, cell := range row.Cells {
			if j == 0 {
				r.lit("|")
			}
			children := cell.Children
			// Cell edges are trimmed on parse; whitespace at either edge
			// needs an invisible empty attribute set to survive.
			if len(children) > 0 {
				if first, ok := children[0].(*Text); ok && first.Value != "" &&
					strings.TrimLeft(first.Value, " \t\v\f\r") != first.Value {
					r.lit("{}")
				}
			}
			cellStart := len(r.buffer)
			r.inTableCell = true
			r.noWrap(func() {
				r.renderInlines(children)
			})
			r.inTableCell = false
			if seg := strings.Join(r.buffer[cellStart:], ""); seg != "" &&
				strings.TrimRight(seg, " \t\v\f\r") != seg {
				r.lit("{}")
			}
			// A trailing hard break's backslash would escape the closing
			// pipe; a space in between keeps the pipe a separator while the
			// cell content trims back to the bare backslash.
			if len(children) > 0 {
				if _, ok := children[len(children)-1].(*HardBreak); ok {
					r.lit(" ")
				}
			}
			r.lit("|")
		}
		// A content row made only of dashes/colons would reparse as a header
		// separator; a space inside the first cell breaks the pattern without
		// changing the cells (cell content is trimmed on parse).
		if looksLikeTableSeparator(strings.Join(r.buffer[rowStart:], "")) {
			r.buffer = append(r.buffer[:rowStart+1],
				append([]string{" "}, r.buffer[rowStart+1:]...)...)
			r.column++
		}
		r.cr()
		// A separator line marks the row above it as a header, so every
		// header row gets its own.
		if isHeader(row) {
			renderSeparator(row)
		}
	}
	if caption != nil {
		r.newline()
		r.lit("^ ")
		r.needsBlankLine, r.blankMandatory = false, false
		r.prefixes = append(r.prefixes, "  ")
		r.endOfPrefix = r.column
		if len(caption.Children) == 0 || beginsWithWhitespace(caption.Children) {
			// Keep the caption marker's space from being trimmed away (an
			// empty or soft-break-initial caption leaves the line as a bare
			// "^"); an empty attribute set is invisible content.
			r.lit("{}")
		}
		r.renderBlockInlines(caption.Children)
		r.popPrefix()
		r.blankline()
	}
	r.blankline()
}

// looksLikeTableSeparator reports whether s is a table row the parser would
// read as a header separator: "|" then one or more cells of dashes with
// optional leading/trailing ':', each closed by "|".
func looksLikeTableSeparator(s string) bool {
	if len(s) < 3 || s[0] != '|' {
		return false
	}
	cells := 0
	for i := 1; i < len(s); {
		start := i
		if i < len(s) && s[i] == ':' {
			i++
		}
		dashes := 0
		for i < len(s) && s[i] == '-' {
			i++
			dashes++
		}
		if i < len(s) && s[i] == ':' {
			i++
		}
		if dashes == 0 || i == start || i >= len(s) || s[i] != '|' {
			return false
		}
		i++
		cells++
		if i == len(s) {
			return cells > 0
		}
	}
	return false
}

func (r *djotRenderer) renderLinkOrImage(children []Inline, destination string, destinationSet, image bool) {
	// Autolinks carry their destination without the resolved flag, so treat
	// any non-empty destination as set.
	hasDest := destinationSet || destination != ""
	if hasDest && len(children) == 1 && !image {
		if t, ok := children[0].(*Text); ok &&
			(t.Value == destination || "mailto:"+t.Value == destination) &&
			!strings.ContainsAny(t.Value, "\n>") &&
			// Only URL- or email-shaped content parses as an autolink.
			(strings.Contains(t.Value, "://") ||
				(strings.Contains(t.Value, "@") && !strings.Contains(t.Value, " ") &&
					"mailto:"+t.Value == destination)) {
			r.lit("<" + t.Value + ">")
			return
		}
	}
	if image {
		r.lit("!")
	}
	r.lit("[")
	r.renderInlines(children)
	r.lit("]")
	if hasDest {
		r.lit("(")
		// Parens and backslashes would end or garble the destination; the
		// parser unescapes any escaped ASCII punctuation in it.
		escaper := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
		r.lit(escaper.Replace(destination))
		r.lit(")")
	} else {
		r.lit("[]")
	}
}

func (r *djotRenderer) inlineContainer(children []Inline, delim string, forceBraces bool) {
	// An empty container ("''") or one nested inside a same-delimiter
	// ancestor ("_(_foo_)_") reparses wrongly without braces.
	// Quotes may open after a word character (unlike _ and *), and the braced
	// form's marked closer strips a trailing literal quote from the content,
	// so quote containers avoid braces unless whitespace or nesting force them.
	isQuote := delim == "'" || delim == `"`
	braces := forceBraces || len(children) == 0 || r.delimOpen(delim) ||
		(!isQuote && r.endsWithWordChar()) ||
		(delim == "'" && !r.singleQuoteCanOpen())
	if braces {
		r.lit("{")
	}
	r.lit(delim)
	// Whitespace at a content edge would keep a bare delimiter from
	// opening/closing; an empty attribute set bounds it. (Braced forms at
	// the start of a line cannot always carry edge whitespace either: a
	// multi-line "{_ ... " prefix reads as a block-attribute attempt.)
	if beginsWithWhitespace(children) {
		r.lit("{}")
	}
	r.openDelims = append(r.openDelims, delim)
	r.renderInlines(children)
	r.openDelims = r.openDelims[:len(r.openDelims)-1]
	if endsWithWhitespace(children) {
		r.lit("{}")
	}
	r.lit(delim)
	if braces {
		r.lit("}")
	}
}

func (r *djotRenderer) delimOpen(delim string) bool {
	for _, d := range r.openDelims {
		if d == delim {
			return true
		}
	}
	return false
}

// singleQuoteCanOpen reports whether a "'" emitted now would be read as a
// quote opener: only at the start of input or after one of the characters the
// parser's canOpenQuote allows (endsWithWordChar covers the alphanumeric
// cases, but e.g. a literal curly quote also blocks opening).
func (r *djotRenderer) singleQuoteCanOpen() bool {
	for i := len(r.buffer) - 1; i >= 0; i-- {
		tok := r.buffer[i]
		if tok == "" {
			continue
		}
		switch tok[len(tok)-1] {
		case ' ', '\t', '\n', '\r', '"', '\'', '-', '(', '[':
			return true
		}
		return false
	}
	return true
}

func (r *djotRenderer) endsWithWordChar() bool {
	if len(r.buffer) == 0 {
		return false
	}
	last := r.buffer[len(r.buffer)-1]
	if last == "" {
		return false
	}
	// Unlike djot.js's /\w$/, '_' is excluded: a just-emitted emphasis
	// delimiter must not force braces onto a directly nested container.
	c := last[len(last)-1]
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func beginsWithWhitespace(children []Inline) bool {
	if len(children) == 0 {
		return false
	}
	switch first := children[0].(type) {
	case *SoftBreak:
		return true
	case *HardBreak:
		// A leading hard break serializes as '\', which is not whitespace,
		// so a bare delimiter still opens (a braced one would not reparse).
		return false
	case *Text:
		return strings.HasPrefix(first.Value, " ") || strings.HasPrefix(first.Value, "\t")
	}
	return false
}

func endsWithWhitespace(children []Inline) bool {
	if len(children) == 0 {
		return false
	}
	switch last := children[len(children)-1].(type) {
	case *SoftBreak, *HardBreak:
		return true
	case *Text:
		return strings.HasSuffix(last.Value, " ") || strings.HasSuffix(last.Value, "\t")
	}
	return false
}

// divFenceLen returns the fence length for div: longer than every nested
// div's fence and longer than any line-leading colon run embedded in literal
// text (verbatim, math, raw, code), either of which would otherwise close
// the div early on reparse.
func divFenceLen(div *Div) int {
	fence := 3
	var scan func(node Node)
	scan = func(node Node) {
		forEachChild(node, func(child Node) {
			text := ""
			switch n := child.(type) {
			case *Div:
				if inner := divFenceLen(n) + 1; inner > fence {
					fence = inner
				}
				return
			case *Verbatim:
				text = n.Text
			case *InlineMath:
				text = n.Text
			case *DisplayMath:
				text = n.Text
			case *RawInline:
				text = n.Text
			case *CodeBlock:
				text = n.Text
			case *RawBlock:
				text = n.Text
			}
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimLeft(line, " \t")
				run := 0
				for run < len(line) && line[run] == ':' {
					run++
				}
				if run+1 > fence {
					fence = run + 1
				}
			}
			scan(child)
		})
	}
	scan(div)
	return fence
}

func formatListNumber(n int, style ListStyle) string {
	switch style {
	case ListAlphaLower:
		return string(rune('a' + (n-1)%26))
	case ListAlphaUpper:
		return string(rune('A' + (n-1)%26))
	case ListRomanLower:
		return strings.ToLower(toRoman(n))
	case ListRomanUpper:
		return toRoman(n)
	default:
		return strconv.Itoa(n)
	}
}

// alphaRomanAmbiguous reports whether a formatted list marker also reads as a
// roman numeral (the reparse prefers the roman reading for such markers).
func alphaRomanAmbiguous(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'i', 'v', 'x', 'l', 'c', 'd', 'm', 'I', 'V', 'X', 'L', 'C', 'D', 'M':
		default:
			return false
		}
	}
	return len(s) > 0
}

// nextNonRomanLetter returns the first letter at or after s (a single-letter
// alpha marker) that is not a roman numeral letter.
func nextNonRomanLetter(s string) string {
	c := s[0]
	for alphaRomanAmbiguous(string(c)) {
		c++
	}
	return string(c)
}

func toRoman(n int) string {
	if n <= 0 {
		return "?"
	}
	// No 4000 cap (unlike djot.js's toRoman): the parser reads additive
	// runs of M, so MMMM round-trips as 4000.
	values := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	symbols := []string{"M", "CM", "D", "CD", "C", "XC", "L", "XL", "X", "IX", "V", "IV", "I"}
	var b strings.Builder
	for i, v := range values {
		for n >= v {
			b.WriteString(symbols[i])
			n -= v
		}
	}
	return b.String()
}
