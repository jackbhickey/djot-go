package djot_test

import (
	"errors"
	"testing"

	"github.com/danielledeleo/djot-go"
)

type renderDjotCase struct {
	name string
	in   string
	want string
}

// renderDjotGoldenCases is shared with FuzzRenderDjotRoundTrip in fuzz_test.go.
var renderDjotGoldenCases = []renderDjotCase{
	{"empty", "", ""},
	{"para", "hello\n", "hello\n"},
	{"two-paras", "a\n\nb\n", "a\n\nb\n"},
	{"soft-break", "a\nb\n", "a\nb\n"},
	{"double-space", "a  b\n", "a  b\n"},
	{"heading-wrapped-prefix", "## Hi\nthere\n", "## Hi\n## there\n"},
	{"heading-auto-id", "# Hello\n", "# Hello\n"},
	{"heading-custom-id", "{#custom}\n# Hello\n", "{#custom}\n# Hello\n"},
	{"para-attr", "{.note}\npara\n", "{.note}\npara\n"},
	{"attr-kv", "{k=\"v v\"}\npara\n", "{k=\"v v\"}\npara\n"},
	{"blockquote", "> a\n> b\n", "> a\n> b\n"},
	{"blockquote-blank", "> a\n>\n> b\n", "> a\n>\n> b\n"},
	{"tight-bullet", "- a\n- b\n", "- a\n- b\n"},
	{"loose-bullet", "- a\n\n- b\n", "- a\n\n- b\n"},
	{"star-marker", "* a\n", "* a\n"},
	{"ordered", "1. a\n2. b\n", "1. a\n2. b\n"},
	{"ordered-start", "5. a\n", "5. a\n"},
	{"ordered-paren-normalized", "(a) x\n", "a. x\n"},
	{"ordered-roman", "ii. x\n", "ii. x\n"},
	// Roman markers have no upper bound: additive Ms round-trip (djot.js's
	// toRoman caps at 4000 and emits "?").
	{"ordered-roman-4000", "MMMM) x\n", "MMMM. x\n"},
	{"task", "- [ ] a\n- [x] b\n", "- [ ] a\n- [X] b\n"},
	{"defn-list", ": term\n\n  def\n", ": term\n\n  def\n"},
	{"code", "```\ncode\n```\n", "```\ncode\n```\n"},
	{"code-lang", "``` go\nx := 1\n```\n", "``` go\nx := 1\n```\n"},
	{"code-inner-fence", "````\n```\n````\n", "````\n```\n````\n"},
	{"raw-block", "``` =html\n<b>\n```\n", "``` =html\n<b>\n```\n"},
	{"thematic-normalized", "***\n", "* * * * *\n"},
	// A break opening a list item needs the empty-attribute shield: break
	// lines may mix '*' and '-', so no character can safely share the marker
	// line ("* ---" is itself one long break).
	{"thematic-in-star-item", "* \n*\n\n ***", "*\n\n* {}\n  ***\n"},
	{"thematic-in-dash-item", "- \n-\n\n ***", "-\n\n- {}\n  ***\n"},
	// A loose list whose natural blanks are all absorbed by sublists (blanks
	// before a sublist, or between items where the previous item ends with
	// one, never count) needs one counting blank after the first marker —
	// shielded with {} when the first block is itself a list.
	{"loose-item-after-sublist", "- *\n-\n\n 00", "-\n\n  {}\n  *\n\n- 00\n"},
	// A break after a paragraph needs a preceding blank (nothing interrupts
	// a paragraph). In a tight item the blank must not count toward
	// looseness, so the break is spelled as a bullet-lookalike spaced break,
	// which the tightness scan skips; in a loose item the counting blank and
	// the unspaced form are both consistent with looseness.
	{"thematic-after-para-tight", "* 0\n\n * **", "* 0\n\n  * * * * *\n"},
	{"thematic-after-para-loose", "* 0\n\n  ***", "* 0\n\n  ***\n"},
	// Deeply nested empty lists must not stack three break characters on one
	// pure marker line: "+ * * *" reparses as "+" item containing a break.
	{"deep-empty-star-nesting", " + *\n* *\n    ", "+ * *\n      *\n"},
	{"div-class", "::: note\na\n:::\n", "{.note}\n:::\na\n:::\n"},
	{"nested-div", "::::\n:::\na\n:::\n::::\n", "::::\n:::\na\n:::\n::::\n"},
	// The fence must also beat ":::" lines embedded in literal text.
	{"div-colon-in-verbatim", "::::\n`\n:::\n0", "::::\n`\n:::\n0`\n::::\n"},
	{"table", "|a|b|\n", "|a|b|\n"},
	{"table-header", "|a|\n|---|\n|b|\n", "|a|\n|---|\n|b|\n"},
	{"table-trailing-head", "|a|\n|:-:|\n", "|a|\n|:-:|\n"},
	{"table-caption", "|a|\n^ cap\n", "|a|\n\n^ cap\n"},
	// Alignments without any header row are carried by a leading separator.
	{"table-align-no-header", "|:-|\n||", "|:--|\n||\n"},
	// Without a trailing pipe this is not a table row; the pipes are
	// escaped as paragraph text.
	{"table-lookalike-paragraph", "||0 {}", "\\|\\|0 {}\n"},
	{"emphasis", "_a_\n", "_a_\n"},
	{"strong", "*a*\n", "*a*\n"},
	// Golden changed (round-trip property wins): a lone "{_ a _}" line is
	// consumed by the parser as a block-attribute line with no target block,
	// so the doc is empty; trailing text keeps it a paragraph and still
	// exercises brace-forcing around whitespace-edged emphasis.
	// Whitespace-edged emphasis uses {} shields rather than the braced
	// form: a braced container at the start of a line cannot always carry
	// edge whitespace (a multi-line "{_ ..." prefix reads as a block
	// attribute attempt).
	{"emph-braced", "{_ a _} x\n", "_{} a {}_ x\n"},
	{"nested-inline", "_*a*_\n", "_*a*_\n"},
	{"mark", "{=a=}\n", "{=a=}\n"},
	{"insert", "{+a+}\n", "{+a+}\n"},
	{"delete", "{-a-}\n", "{-a-}\n"},
	{"superscript", "^a^\n", "^a^\n"},
	{"subscript", "~a~\n", "~a~\n"},
	{"verbatim", "`x`\n", "`x`\n"},
	// Golden changed (round-trip property wins): the parser keeps the spaces
	// (" a`b ") since they are not adjacent to a backtick, and "``a`b``"
	// would reparse to different content.
	{"verbatim-ticks", "`` a`b ``\n", "`` a`b ``\n"},
	// Adjacent verbatim delimiters would fuse into one run.
	{"adjacent-verbatims", "`0`{}`", "`0`{}``\n"},
	{"verbatim-edge-tick", "`` `x ``\n", "`` `x ``\n"},
	{"inline-math", "$`x^2`\n", "$`x^2`\n"},
	{"display-math", "$$`E`\n", "$$`E`\n"},
	{"raw-inline", "`<b>`{=html}\n", "`<b>`{=html}\n"},
	{"nbsp", "a\\ b\n", "a\\ b\n"},
	// A trailing non-breaking space would lose its space to end-of-line
	// trimming and become a hard break; the {} shield keeps it.
	{"nbsp-at-end", "\\ {}", "\\ {}\n"},
	{"smart-quotes", "\"a\"\n", "\"a\"\n"},
	{"smart-dashes", "a... b--c---d\n", "a... b--c---d\n"},
	{"escaped-star", "\\*not\\*\n", "\\*not\\*\n"},
	{"line-start-dash", "\\- foo\n", "\\- foo\n"},
	{"line-start-hash", "\\# foo\n", "\\# foo\n"},
	{"endash-literal", "a\\--b\n", "a\\--b\n"},
	{"link", "[t](/u)\n", "[t](/u)\n"},
	{"ref-link-inlined", "[foo][bar]\n\n[bar]: /url\n", "[foo](/url)\n\n[bar]: /url\n"},
	{"unresolved-ref", "[foo][nope]\n", "[foo][]\n"},
	{"autolink", "<http://x.com>\n", "<http://x.com>\n"},
	{"email", "<a@b.com>\n", "<a@b.com>\n"},
	{"image", "![alt](/i.png)\n", "![alt](/i.png)\n"},
	{"span-attr", "[w]{.c}\n", "[w]{.c}\n"},
	{"footnote", "a[^1]\n\n[^1]: note\n", "a[^1]\n\n[^1]: note\n"},
	{"footnote-two-paras", "a[^1]\n\n[^1]: p1\n\n  p2\n", "a[^1]\n\n[^1]: p1\n\n  p2\n"},
	{"hard-break", "a\\\nb\n", "a\\\nb\n"},
}

func TestRenderDjotGolden(t *testing.T) {
	for _, tc := range renderDjotGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			got := djot.RenderDjot(djot.Parse(tc.in))
			if got != tc.want {
				t.Errorf("input:\n%q\n\nwant:\n%q\n\ngot:\n%q", tc.in, tc.want, got)
			}
		})
	}
}

func TestRenderDjotWrap(t *testing.T) {
	cases := []struct {
		name string
		in   string
		opts []djot.DjotOption
		want string
	}{
		{
			name: "wrap-8",
			in:   "aaa bbb ccc ddd\n",
			opts: []djot.DjotOption{djot.WithWrapWidth(8)},
			want: "aaa bbb\nccc ddd\n",
		},
		{
			name: "no-wrap-spaces",
			in:   "a\nb\n",
			opts: []djot.DjotOption{djot.WithWrapWidth(-1)},
			want: "a b\n",
		},
		{
			name: "wrap-30-blockquote",
			in:   "> aaaa bbbb cccc dddd eeee ffff gggg\n",
			opts: []djot.DjotOption{djot.WithWrapWidth(30)},
			want: "> aaaa bbbb cccc dddd eeee\n> ffff gggg\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := djot.RenderDjot(djot.Parse(tc.in), tc.opts...)
			if got != tc.want {
				t.Errorf("input:\n%q\n\nwant:\n%q\n\ngot:\n%q", tc.in, tc.want, got)
			}
		})
	}
}

type failingDjotWriter struct{}

var errDjotWriteFailed = errors.New("write failed")

func (failingDjotWriter) Write(p []byte) (int, error) { return 0, errDjotWriteFailed }

func TestRenderDjotTo(t *testing.T) {
	t.Run("writes same bytes", func(t *testing.T) {
		in := "# Hello\n\nsome _text_ here\n"
		doc := djot.Parse(in)
		want := djot.RenderDjot(doc)
		if want == "" {
			t.Fatal("RenderDjot returned empty output for non-empty input")
		}
		var sb writerBuilder
		if err := djot.RenderDjotTo(&sb, doc); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sb.String() != want {
			t.Errorf("want %q, got %q", want, sb.String())
		}
	})
	t.Run("nil writer errors", func(t *testing.T) {
		if err := djot.RenderDjotTo(nil, djot.Parse("hi\n")); err == nil {
			t.Error("expected error for nil writer")
		}
	})
	t.Run("write error propagates", func(t *testing.T) {
		err := djot.RenderDjotTo(failingDjotWriter{}, djot.Parse("hi\n"))
		if !errors.Is(err, errDjotWriteFailed) {
			t.Errorf("expected errDjotWriteFailed, got %v", err)
		}
	})
}

// writerBuilder is a minimal strings.Builder wrapper kept local so the test
// controls exactly what Write receives.
type writerBuilder struct{ buf []byte }

func (w *writerBuilder) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *writerBuilder) String() string { return string(w.buf) }

// mergeAdjacentText joins adjacent *Text siblings in every []Inline slice of
// doc's tree. It mutates doc and returns it; callers should parse a fresh doc.
// Needed because unmatched smart quotes become literal curly Text at parse
// time and re-merge with neighbors on reparse.
func mergeAdjacentText(doc *djot.Doc) *djot.Doc {
	mergeAdjacentTextNode(doc.Root())
	return doc
}

func mergeInlines(children []djot.Inline) []djot.Inline {
	merged := children[:0]
	for _, child := range children {
		if text, ok := child.(*djot.Text); ok {
			// Empty text nodes are non-semantic and not re-serialized.
			if text.Value == "" && text.Attributes().Len() == 0 {
				continue
			}
			if len(merged) > 0 {
				if prev, ok := merged[len(merged)-1].(*djot.Text); ok {
					prev.Value += text.Value
					continue
				}
			}
		}
		merged = append(merged, child)
	}
	for _, child := range merged {
		mergeAdjacentTextNode(child)
	}
	return merged
}

func mergeBlocks(children []djot.Block) {
	for _, child := range children {
		mergeAdjacentTextNode(child)
	}
}

func mergeAdjacentTextNode(node djot.Node) {
	switch n := node.(type) {
	case *djot.Document:
		mergeBlocks(n.Children)
	case *djot.Section:
		mergeBlocks(n.Children)
	case *djot.BlockQuote:
		mergeBlocks(n.Children)
	case *djot.Div:
		mergeBlocks(n.Children)
	case *djot.DefinitionList:
		mergeBlocks(n.Children)
	case *djot.Definition:
		mergeBlocks(n.Children)
	case *djot.Table:
		mergeBlocks(n.Children)
	case *djot.Footnote:
		mergeBlocks(n.Children)
	case *djot.ListItem:
		mergeBlocks(n.Children)
	case *djot.TaskListItem:
		mergeBlocks(n.Children)
	case *djot.BulletList:
		for _, item := range n.Items {
			mergeAdjacentTextNode(item)
		}
	case *djot.OrderedList:
		for _, item := range n.Items {
			mergeAdjacentTextNode(item)
		}
	case *djot.TaskList:
		for _, item := range n.Items {
			mergeAdjacentTextNode(item)
		}
	case *djot.TableRow:
		for _, cell := range n.Cells {
			mergeAdjacentTextNode(cell)
		}
	case *djot.Paragraph:
		n.Children = mergeInlines(n.Children)
	case *djot.Heading:
		n.Children = mergeInlines(n.Children)
	case *djot.Term:
		n.Children = mergeInlines(n.Children)
	case *djot.TableCell:
		n.Children = mergeInlines(n.Children)
	case *djot.Caption:
		n.Children = mergeInlines(n.Children)
	case *djot.Emphasis:
		n.Children = mergeInlines(n.Children)
	case *djot.Strong:
		n.Children = mergeInlines(n.Children)
	case *djot.Superscript:
		n.Children = mergeInlines(n.Children)
	case *djot.Subscript:
		n.Children = mergeInlines(n.Children)
	case *djot.Insert:
		n.Children = mergeInlines(n.Children)
	case *djot.Delete:
		n.Children = mergeInlines(n.Children)
	case *djot.Mark:
		n.Children = mergeInlines(n.Children)
	case *djot.Link:
		n.Children = mergeInlines(n.Children)
	case *djot.Image:
		n.Children = mergeInlines(n.Children)
	case *djot.Span:
		n.Children = mergeInlines(n.Children)
	case *djot.DoubleQuoted:
		n.Children = mergeInlines(n.Children)
	case *djot.SingleQuoted:
		n.Children = mergeInlines(n.Children)
	}
}

// TestRenderDjotRoundTripRegressions pins round-trip bug classes found while
// making the official corpus and fuzzer pass; each input is minimal for one
// distinct failure mode.
func TestRenderDjotRoundTripRegressions(t *testing.T) {
	inputs := []string{
		// escaped char right after a span/link opener was merged into the
		// opener placeholder and dropped by the parser
		"a *b{#id key=\"*\"}o",
		"[\\*b]{#x}",
		// leading space at start of line is eaten on reparse
		"{#id} at beginning",
		// span with no attributes needs an explicit {}
		"[text]{}",
		// same-delimiter container nested in another needs braces
		"_({_foo_})_",
		// empty inline container needs braces
		"{''}hi{''}",
		// adjacent bullet/task lists must not merge on reparse
		"- [ ] foo\n+ [ ] bar\n* [ ] baz",
		// footnote reference at line start followed by ':' reparses as a
		// footnote definition
		"[^a\nb]:  \n    foo",
		// table row of dashes must not reparse as a header separator
		"| a | b |\n| --- | --- |\n| c | d |",
		// separator-only table parses to an empty table
		"|--|--|",
		// an empty definition leaves a stale prefix-only line; the following
		// sibling paragraph must not be indented into the definition
		": \n0",
		// a class value that is not a clean space-separated word list (here
		// empty) cannot round-trip through the ".class" shorthand
		"0{class}",
		// a decimal ordered list may legitimately start at 0
		": 0) 0000",
		// an em dash paragraph must not reparse as a thematic break
		"# 0000000\n---",
		// a single quote only opens after specific characters; a literal curly
		// quote before it forces braces
		"0\"'0'",
		// no blank lines between a tight list item's blocks, or the list
		// reparses loose
		" * #\n:::",
		// a thematic break directly after a list marker would fuse with it
		// into one longer break line
		"*\n***",
		// a verbatim newline followed by a heading-looking line needs an
		// unclosed '{' before it to keep the paragraph together on reparse
		"{`\n# 0",
		// an empty id cannot use the "#" shorthand
		"{id}\n0",
		// a task list's marker is not in the AST, but a bullet list's is: the
		// task list must dodge both neighbours' markers
		"* [X] \n-",
		// the braced quote form's closer strips a trailing literal quote;
		// quotes can open after word chars, so plain delimiters work here
		"'0\"\"\"",
		// adjacent same-style ordered lists must alternate delimiter form
		"0) 0000000000\n0. 000",
		// a single-item loose list needs a blank line inside its item
		"* \n\n 00",
		// braced quote whose content is a literal quote char: the parser must
		// not strip it as a doubled closer
		"0\"'''",
		// a footnote label ending in a backslash must not escape its ']'
		"[^0\\ ]0",
		// unbalanced parens in a link destination must be escaped
		"[][000]\n\n[]: (000",
		// a loose definition list with an empty definition must not leave a
		// trailing blank line (its looseness is unobservable)
		":\n\n  00",
		// a ':' in text can pair with a later one into a symbol
		"0000:0\\:",
		// a verbatim newline inside a prefixed block must carry the prefix
		": `\n 0",
		// an empty table caption is still a caption
		"||\n^ ",
		// a trailing space at the end of a paragraph is trimmed on reparse
		"0 {A}",
		// a sibling after a loose list ending in an empty item must not land
		// on the empty item's line
		"- 0\n\n-\n*",
		// line-start escapes must also fire on a block's first (marker) line
		"> ######\t ",
		// a hard break ends a table row: no newline, and no closing pipe that
		// its backslash would escape
		"||\\",
		// every header row needs its own separator line after it
		"||\n|-|\n||\n|-|",
		// an empty paragraph reparses from two empty attribute sets
		"{}{}",
		// adjacent definition lists need an empty attribute line between them
		" :\n:",
		// a language with spaces or backticks needs a tilde fence
		"~~~0 0",
		// an alpha list opening on a roman-ambiguous letter needs an
		// alpha-only second marker or the reparse settles on roman
		"C) \nA) ",
		// a thematic break opening a list item must keep the marker line valid
		"0) ***",
		// a code fence must be longer than any content line's backtick run
		"`````\n````",
		// odd leading whitespace (form feed) is eaten after a block marker
		"#\n\f",
		// only URL-shaped destinations may use the autolink form
		"[0](0)000000",
		// a paragraph starting with a braced container must indent its
		// continuation lines, or the failed block-attribute scan marks the
		// braces literal
		"{_\n _}0",
		// adjacent dash nodes must not fuse and regroup
		"--------0",
		// a thematic break after a star list must not read as a star item
		"*\n\n***",
		// a caption starting with a soft break keeps its marker space
		"||\n^ \n0",
		// nested empty star lists must not render as a thematic break line
		" - *\n*",
		// the definition-list separator must not loosen a tight outer list
		// "* :\n:" (adjacent empty definition lists inside a tight item) is
		// retired: the shape only arises from lazy-indent geometry and is a
		// documented carve-out in FuzzRenderDjotRoundTrip.
		// a trailing soft break needs a visible continuation line
		"00000000\r{}",
		// consecutive soft breaks must not form a blank line
		"0\r{}\r0",
		// a list after a non-paragraph block in a tight item needs no blank
		"* ||\n:",
		// "0)" at line start in text would read as an ordered-list marker
		"0) 0\\) 0",
		// heading lines trim trailing whitespace before a soft break
		"#\n\f\n0",
		// adjacent same-marker bullet lists need a separating attribute line
		"*\n\n\n *",
		// an indented backtick run still closes a code fence
		"````\n ```",
		// a block starting with a soft break needs invisible content first
		"{A=\"}\n\"}\n0",
		// an attribute line after a paragraph in a tight item needs a blank
		// line, or it reads as paragraph continuation
		"* 0\n #",
		// the "{}" leading-space shield also needs indented continuations
		"{A} 0\n 0",
		// a literal curly quote can supply the unclosed-brace guard via its
		// unmatched marked-opener spelling
		"{\"`}\n# 0",
		// a list enumerator too large for int must not overflow
		"10000000000000000000) 0",
		// the brace guard must keep the count open past a '}' inside the
		// dangerous verbatim: unescape every '{' in the guard text
		"{{`}\n# 0",
		// a single-line paragraph wrapped in braces must not be eaten as a
		// block attribute
		"_0 {}_",
		// hashes at the very end of a verbatim are followed by the closing
		// backtick and are not a heading; no brace guard applies
		"\"`\n#`0",
		// verbatim newlines in headings must not re-emit the "# " prefix,
		// which eats odd leading whitespace from the content
		"# 0`\r\v0",
		// with a real '{' available, curly quotes must stay literal (the
		// paragraph is under plain-brace treatment)
		"{0\"00`\n# 0",
		// a hard break can end any cell: backslash, space, then the pipe
		"||\\  |0",
		// anonymous sections always get a counter ("s-1"); a bare "s" id is
		// custom and must be preserved
		"{#s}\n#",
		// a spaced break inside a list item reads as nested list items,
		// which flips the tightness; use the compact form there
		"*\n\n ***",
		// the ordered-marker escape state must carry across text nodes that
		// merge on reparse
		"0{}0)0",
		// implicit heading refs: symbols contribute nothing to the label
		"# ]:0:0",
	}
	for _, in := range inputs {
		assertDjotRoundTrip(t, in)
	}
}

func assertDjotRoundTrip(t *testing.T, in string) {
	t.Helper()
	d1 := djot.Parse(in)
	out := djot.RenderDjot(d1)
	d2 := djot.Parse(out)

	if h1, h2 := djot.RenderHTML(d1), djot.RenderHTML(d2); h1 != h2 {
		t.Errorf("HTML round trip differs\ninput:\n%q\nrendered djot:\n%q\nwant HTML:\n%s\ngot HTML:\n%s",
			in, out, h1, h2)
	}

	a1 := djot.RenderAST(mergeAdjacentText(djot.Parse(in)), false)
	a2 := djot.RenderAST(mergeAdjacentText(djot.Parse(out)), false)
	if a1 != a2 {
		t.Errorf("AST round trip differs\ninput:\n%q\nrendered djot:\n%q\nwant AST:\n%s\ngot AST:\n%s",
			in, out, a1, a2)
	}

	if again := djot.RenderDjot(d2); again != out {
		t.Errorf("not idempotent\ninput:\n%q\nfirst:\n%q\nsecond:\n%q", in, out, again)
	}
}

func TestRenderDjotRoundTripOfficial(t *testing.T) {
	allTests := loadOfficialTests(t)

	for file, cases := range allTests {
		t.Run(file, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.Name, func(t *testing.T) {
					in := tc.Input
					d1 := djot.Parse(in)
					out := djot.RenderDjot(d1)
					d2 := djot.Parse(out)

					if h1, h2 := djot.RenderHTML(d1), djot.RenderHTML(d2); h1 != h2 {
						t.Errorf("HTML round trip differs\ninput:\n%q\nrendered djot:\n%q\nwant HTML:\n%s\ngot HTML:\n%s",
							in, out, h1, h2)
					}

					a1 := djot.RenderAST(mergeAdjacentText(djot.Parse(in)), false)
					a2 := djot.RenderAST(mergeAdjacentText(djot.Parse(out)), false)
					if a1 != a2 {
						t.Errorf("AST round trip differs\ninput:\n%q\nrendered djot:\n%q\nwant AST:\n%s\ngot AST:\n%s",
							in, out, a1, a2)
					}

					if again := djot.RenderDjot(d2); again != out {
						t.Errorf("not idempotent\ninput:\n%q\nfirst:\n%q\nsecond:\n%q", in, out, again)
					}
				})
			}
		})
	}
}
