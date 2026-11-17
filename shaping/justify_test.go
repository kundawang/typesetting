package shaping

import (
	"bytes"
	"testing"

	td "github.com/go-text/typesetting-utils/opentype"
	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/language"
	tu "github.com/go-text/typesetting/testutils"
	"golang.org/x/image/math/fixed"
)

// loadTestdataFont loads a font from the go-text/typesetting-utils embedded
// testdata module.
func loadTestdataFont(t testing.TB, name string) *font.Face {
	t.Helper()
	b, err := td.Files.ReadFile(name)
	if err != nil {
		t.Fatalf("reading embedded font %s: %v", name, err)
	}
	face, err := font.ParseTTF(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("parsing font %s: %v", name, err)
	}
	return face
}

// lineWidthForTest sums the run advances of a line.
func lineWidthForTest(line Line) fixed.Int26_6 {
	var w fixed.Int26_6
	for i := range line {
		w += line[i].Advance
	}
	return w
}

// clustersForTest returns the (cluster index, glyph count) sequence of a line,
// used to prove justification never splits a glyph cluster.
func clustersForTest(line Line) [][2]int {
	var out [][2]int
	for _, run := range line {
		for _, g := range run.Glyphs {
			out = append(out, [2]int{g.ClusterIndex, g.GlyphCount})
		}
	}
	return out
}

// shapeLine builds a single-run shaped output for the given text.
func shapeLine(t testing.TB, text []rune, face *font.Face, dir di.Direction, script language.Script) Output {
	t.Helper()
	return (&HarfbuzzShaper{}).Shape(Input{
		Text:      text,
		RunStart:  0,
		RunEnd:    len(text),
		Direction: dir,
		Face:      face,
		Size:      fixed.I(16),
		Script:    script,
	})
}

// copyLine deep-copies a line so the original shaping output can be compared
// after in-place justification.
func copyLine(line Line) Line {
	out := make(Line, len(line))
	for i, run := range line {
		out[i] = run.copy()
	}
	return out
}

func TestJustifyLatin(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("one two three")
	run := shapeLine(t, text, face, di.DirectionLTR, language.Latin)
	tu.Assert(t, len(run.Glyphs) == len(text)) // 1:1 rune/glyph mapping

	natural := run.Advance
	line := Line{run.copy()}
	before := clustersForTest(line)

	target := natural + fixed.I(20)
	res := JustifyLine(line, text, target, JustificationConfig{})
	tu.Assert(t, res.Width == target)
	tu.Assert(t, lineWidthForTest(res.Line) == target)

	// Only the two space glyphs (clusters 3 and 7) may have gained advance.
	for i, g := range res.Line[0].Glyphs {
		switch i {
		case 3, 7:
			tu.Assert(t, g.Advance > run.Glyphs[i].Advance)
		default:
			tu.Assert(t, g.Advance == run.Glyphs[i].Advance)
			tu.Assert(t, g.XOffset == run.Glyphs[i].XOffset)
		}
	}
	// Clusters are untouched.
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))

	// The two gaps must share the expansion as evenly as possible.
	gain := res.Line[0].Glyphs[3].Advance - run.Glyphs[3].Advance
	gain += res.Line[0].Glyphs[7].Advance - run.Glyphs[7].Advance
	tu.Assert(t, gain == fixed.I(20))

	// A non-integer expansion must not lose units.
	line2 := Line{run.copy()}
	res2 := JustifyLine(line2, text, natural+fixed.Int26_6(7), JustificationConfig{})
	tu.Assert(t, res2.Width == natural+fixed.Int26_6(7))
	g1 := line2[0].Glyphs[3].Advance - run.Glyphs[3].Advance
	g2 := line2[0].Glyphs[7].Advance - run.Glyphs[7].Advance
	tu.Assert(t, g1+g2 == fixed.Int26_6(7))
	tu.Assert(t, g1-g2 <= 1 && g2-g1 <= 1)
}

func TestJustifyCJK(t *testing.T) {
	face := loadTestdataFont(t, "common/mplus-1p-regular.ttf")
	text := []rune("日本語のテスト")
	run := shapeLine(t, text, face, di.DirectionLTR, language.Han)
	tu.Assert(t, len(run.Glyphs) == len(text)) // 1:1 rune/glyph mapping

	natural := run.Advance
	line := Line{run.copy()}
	before := clustersForTest(line)

	// No whitespace: only the six inter-cluster CJK gaps are expandable.
	target := natural + fixed.Int26_6(13)
	res := JustifyLine(line, text, target, JustificationConfig{})
	tu.Assert(t, res.Width == target)
	tu.Assert(t, lineWidthForTest(res.Line) == target)
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))

	// The first glyph (line start) never carries expansion: there is no gap
	// before the line. Every CJK gap is anchored on the first glyph of the
	// cluster following it, so glyphs 1..6 absorb the whole deficit.
	tu.Assert(t, res.Line[0].Glyphs[0].Advance == run.Glyphs[0].Advance)
	var gain fixed.Int26_6
	for i := 1; i < len(run.Glyphs); i++ {
		g := res.Line[0].Glyphs[i].Advance - run.Glyphs[i].Advance
		tu.Assert(t, g >= 0)
		gain += g
	}
	tu.Assert(t, gain == fixed.Int26_6(13))
}

func TestJustifyRTL(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/Amiri-Regular.ttf")
	text := []rune("تثذرزسشص لمنهويء") // 16 runes, one internal space at index 8
	run := shapeLine(t, text, face, di.DirectionRTL, language.Arabic)
	tu.Assert(t, len(run.Glyphs) == len(text)) // 1:1 rune/glyph mapping

	natural := run.Advance
	line := Line{run.copy()}
	before := clustersForTest(line)

	// Find the visual index of the space cluster.
	spaceIdx := -1
	for i, g := range line[0].Glyphs {
		if g.ClusterIndex == 8 {
			spaceIdx = i
		}
	}
	tu.Assert(t, spaceIdx >= 0)

	target := natural + fixed.I(10)
	res := JustifyLine(line, text, target, JustificationConfig{})
	tu.Assert(t, res.Width == target)
	tu.Assert(t, lineWidthForTest(res.Line) == target)
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))

	// Only the space glyph changes, and the expansion remains positive even
	// in an RTL run.
	for i, g := range res.Line[0].Glyphs {
		if i == spaceIdx {
			tu.Assert(t, g.Advance == run.Glyphs[i].Advance+fixed.I(10))
		} else {
			tu.Assert(t, g.Advance == run.Glyphs[i].Advance)
		}
	}

	// With two spaces, the deficit is spread evenly across both regardless of
	// visual storage order.
	twoSpaces := []rune("اب ج د") // spaces at rune indices 2 and 4
	arRun := shapeLine(t, twoSpaces, face, di.DirectionRTL, language.Arabic)
	var spaceIdxs []int
	for i, g := range arRun.Glyphs {
		if twoSpaces[g.ClusterIndex] == ' ' {
			spaceIdxs = append(spaceIdxs, i)
		}
	}
	tu.Assert(t, len(spaceIdxs) == 2)
	rtlLine := Line{arRun.copy()}
	rtlRes := JustifyLine(rtlLine, twoSpaces, arRun.Advance+fixed.Int26_6(5), JustificationConfig{})
	tu.Assert(t, rtlRes.Width == arRun.Advance+fixed.Int26_6(5))
	var rtlGain fixed.Int26_6
	for i, g := range rtlRes.Line[0].Glyphs {
		gain := g.Advance - arRun.Glyphs[i].Advance
		if i == spaceIdxs[0] || i == spaceIdxs[1] {
			tu.Assert(t, gain == 2 || gain == 3) // 5 split over two gaps
		} else {
			tu.Assert(t, gain == 0)
		}
		rtlGain += gain
	}
	tu.Assert(t, rtlGain == fixed.Int26_6(5))
}

func TestJustifyMixedFontRuns(t *testing.T) {
	latinFace := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	cjkFace := loadTestdataFont(t, "common/mplus-1p-regular.ttf")
	// "ab 日本 cd": run boundaries sit between the Latin and CJK content and
	// must never be expanded themselves.
	text := []rune("ab 日本 cd")
	shaper := HarfbuzzShaper{}

	latin1 := shaper.Shape(Input{Text: text, RunStart: 0, RunEnd: 3, Face: latinFace, Size: fixed.I(16), Script: language.Latin, Direction: di.DirectionLTR})
	cjk := shaper.Shape(Input{Text: text, RunStart: 3, RunEnd: 5, Face: cjkFace, Size: fixed.I(16), Script: language.Han, Direction: di.DirectionLTR})
	latin2 := shaper.Shape(Input{Text: text, RunStart: 5, RunEnd: 8, Face: latinFace, Size: fixed.I(16), Script: language.Latin, Direction: di.DirectionLTR})

	line := Line{latin1.copy(), cjk.copy(), latin2.copy()}
	natural := lineWidthForTest(line)
	before := clustersForTest(line)

	// Opportunities: one space inside the first run plus one CJK gap between
	// the two ideographs; the two run boundaries are excluded.
	opps, _, ok := collectOpportunities(line, text)
	tu.Assert(t, ok)
	tu.Assert(t, len(opps) == 2)

	res := JustifyLine(line, text, natural+fixed.I(10), JustificationConfig{})
	tu.Assert(t, res.Width == natural+fixed.I(10))
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))
	// Run advances were cached consistently with their glyphs.
	for i := range res.Line {
		var sum fixed.Int26_6
		for _, g := range res.Line[i].Glyphs {
			sum += g.Advance
		}
		tu.Assert(t, sum == res.Line[i].Advance)
	}
}

func TestJustifyEdgeCases(t *testing.T) {
	latinFace := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	cjkFace := loadTestdataFont(t, "common/mplus-1p-regular.ttf")

	t.Run("no opportunities", func(t *testing.T) {
		text := []rune("hello")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		line := Line{run.copy()}
		natural := run.Advance
		res := JustifyLine(line, text, natural+fixed.I(10), JustificationConfig{})
		tu.Assert(t, res.Width == natural)
		tu.Assert(t, equalSlices(clustersForTest(res.Line), clustersForTest(Line{run.copy()})))
		tu.Assert(t, res.Line[0].Glyphs[0].Advance == run.Glyphs[0].Advance)
	})

	t.Run("hard break", func(t *testing.T) {
		text := []rune("one two\n")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		line := Line{run.copy()}
		natural := run.Advance
		res := JustifyLine(line, text, natural+fixed.I(10), JustificationConfig{})
		tu.Assert(t, res.Width == natural)
		tu.Assert(t, lineWidthForTest(res.Line) == natural)
	})

	t.Run("target not wider than line", func(t *testing.T) {
		text := []rune("one two")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		natural := run.Advance
		for _, target := range []fixed.Int26_6{natural, natural - fixed.I(5), 0} {
			line := Line{run.copy()}
			res := JustifyLine(line, text, target, JustificationConfig{})
			tu.Assert(t, res.Width == natural)
			for i := range line[0].Glyphs {
				tu.Assert(t, line[0].Glyphs[i].Advance == run.Glyphs[i].Advance)
			}
		}
	})

	t.Run("capped expansion", func(t *testing.T) {
		text := []rune("one two three")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		natural := run.Advance
		line := Line{run.copy()}
		cap := fixed.Int26_6(3)
		res := JustifyLine(line, text, natural+fixed.I(20), JustificationConfig{MaxExpansion: cap})
		// Two opportunities, at most 3 units each.
		tu.Assert(t, res.Width == natural+2*cap)
		tu.Assert(t, lineWidthForTest(res.Line) == natural+2*cap)
		g1 := line[0].Glyphs[3].Advance - run.Glyphs[3].Advance
		g2 := line[0].Glyphs[7].Advance - run.Glyphs[7].Advance
		tu.Assert(t, g1 == cap && g2 == cap)
	})

	t.Run("negative advance is left untouched", func(t *testing.T) {
		text := []rune("one two")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		// Fabricate an unexpected negative advance on a horizontal run.
		run.Glyphs[1].Advance = -1
		run.RecomputeAdvance()
		line := Line{run.copy()}
		natural := run.Advance
		res := JustifyLine(line, text, natural+fixed.I(10), JustificationConfig{})
		tu.Assert(t, res.Width == natural)
		tu.Assert(t, line[0].Glyphs[3].Advance == run.Glyphs[3].Advance)
	})

	t.Run("mixed axis line is left untouched", func(t *testing.T) {
		latin := []rune("ab cd")
		cjk := []rune("日本語")
		hRun := shapeLine(t, latin, latinFace, di.DirectionLTR, language.Latin)
		vRun := shapeLine(t, cjk, cjkFace, di.DirectionTTB, language.Han)
		line := Line{hRun.copy(), vRun.copy()}
		width := lineWidthForTest(line)
		res := JustifyLine(line, append(latin, cjk...), fixed.I(100000), JustificationConfig{})
		tu.Assert(t, res.Width == width)
	})

	t.Run("empty line", func(t *testing.T) {
		res := JustifyLine(Line{}, nil, fixed.I(100), JustificationConfig{})
		tu.Assert(t, len(res.Line) == 0)
		tu.Assert(t, res.Width == 0)
	})

	t.Run("huge target does not overflow", func(t *testing.T) {
		text := []rune("one two")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		line := Line{run.copy()}
		// A target far beyond the Int26_6 range must remain deterministic.
		res := JustifyLine(line, text, 1<<31-1, JustificationConfig{})
		tu.Assert(t, res.Width > 0)
		tu.Assert(t, res.Width == lineWidthForTest(res.Line))
		for _, g := range res.Line[0].Glyphs {
			tu.Assert(t, g.Advance > 0)
		}
	})

	t.Run("cluster index outside text is ignored", func(t *testing.T) {
		text := []rune("ab")
		run := Output{
			Glyphs:    []Glyph{simpleGlyph(0), simpleGlyph(99)},
			Advance:   2 * fixed.I(10),
			Direction: di.DirectionLTR,
			Runes:     Range{Offset: 0, Count: 2},
		}
		line := Line{run.copy()}
		res := JustifyLine(line, text, run.Advance+fixed.I(5), JustificationConfig{})
		// No valid opportunity: the out-of-text cluster cannot be classified.
		tu.Assert(t, res.Width == run.Advance)
	})

	t.Run("zero cap means unlimited", func(t *testing.T) {
		text := []rune("one two")
		run := shapeLine(t, text, latinFace, di.DirectionLTR, language.Latin)
		line := Line{run.copy()}
		res := JustifyLine(line, text, run.Advance+fixed.I(40), JustificationConfig{MaxExpansion: 0})
		tu.Assert(t, res.Width == run.Advance+fixed.I(40))
	})
}

// TestJustifyKeepsClustersIntact uses synthetic glyphs with fused clusters and
// ligatures to prove that justification never inserts space inside a cluster.
func TestJustifyKeepsClustersIntact(t *testing.T) {
	// Logical runes a b c d e with a space at rune index 2:
	// a ligature covering runes 0-1, the space, and a fused cluster 3-4.
	text := []rune("ab de")
	glyphSlice := []Glyph{
		ligatureGlyph(0, 2), // glyph 0: runes 0,1
		simpleGlyph(2),      // glyph 1: space rune
		complexGlyph(3, 2, 2),
		complexGlyph(3, 2, 2), // glyphs 2,3: runes 3,4
	}
	run := Output{
		Glyphs:    glyphSlice,
		Advance:   4 * fixed.I(10),
		Direction: di.DirectionLTR,
		Runes:     Range{Offset: 0, Count: len(text)},
	}
	// Mark the space glyph as contentless like a shaped word separator.
	run.Glyphs[1].Width = 0

	line := Line{run.copy()}
	before := clustersForTest(line)
	res := JustifyLine(line, text, run.Advance+fixed.I(6), JustificationConfig{})
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))
	// Only the space glyph (index 1) absorbed the expansion.
	tu.Assert(t, res.Line[0].Glyphs[1].Advance == fixed.I(10)+fixed.I(6))
	tu.Assert(t, res.Line[0].Glyphs[0].Advance == fixed.I(10))
	tu.Assert(t, res.Line[0].Glyphs[2].Advance == fixed.I(10))
	tu.Assert(t, res.Line[0].Glyphs[3].Advance == fixed.I(10))
}

func TestJustifyWrappingAndLastLine(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("one two three four five six")
	run := shapeLine(t, text, face, di.DirectionLTR, language.Latin)
	cell := run.Glyphs[0].Advance // monospace glyph advance at the chosen size

	// Width of eight cells: with 27 runes this produces at least three lines.
	width := 8 * cell
	// With the default trailing whitespace trimming, a line ending in a space
	// exposes the space as an internal, expandable gap and its own width is
	// dropped, so every non-last line can reach exactly the target width.
	lines, _ := (&LineWrapper{}).WrapParagraph(WrapConfig{}, width.Round(), text,
		NewSliceIterator([]Output{run.copy()}))
	tu.Assert(t, len(lines) >= 3)

	justified := JustifyParagraph(lines, text, width, JustificationConfig{})
	tu.Assert(t, len(justified) == len(lines))
	for i, jl := range justified {
		if i < len(justified)-1 {
			tu.Assert(t, jl.Width == width)
			tu.Assert(t, lineWidthForTest(jl.Line) == width)
		} else {
			// The last line keeps its natural, shorter width.
			tu.Assert(t, jl.Width <= width)
			tu.Assert(t, jl.Width == lineWidthForTest(jl.Line))
		}
	}

	// When trailing whitespace trimming is disabled, a line ending with a
	// space cannot be stretched all the way to the target: justification
	// reports the actual width instead of inventing space at the line edge.
	untrimmed, _ := (&LineWrapper{}).WrapParagraph(WrapConfig{DisableTrailingWhitespaceTrim: true}, width.Round(), text,
		NewSliceIterator([]Output{run.copy()}))
	for i, line := range untrimmed {
		jl := JustifyLine(copyLine(line), text, width, JustificationConfig{})
		tu.Assert(t, jl.Width == lineWidthForTest(jl.Line))
		tu.Assert(t, jl.Width <= width)
		_ = i
	}

	// A hard-broken line is never justified even when it is not last.
	hardText := []rune("one two\nthree four five")
	hardRun := shapeLine(t, hardText, face, di.DirectionLTR, language.Latin)
	hardLines, _ := (&LineWrapper{}).WrapParagraph(WrapConfig{}, width.Round(), hardText,
		NewSliceIterator([]Output{hardRun.copy()}))
	hardJustified := JustifyParagraph(hardLines, hardText, width, JustificationConfig{})
	sawHardBreak := false
	for i, jl := range hardJustified {
		if lineEndsInHardBreak(jl.Line, hardText) {
			sawHardBreak = true
			// A hard-broken line keeps exactly its natural, unjustified width.
			tu.Assert(t, jl.Width == lineWidthForTest(jl.Line))
			tu.Assert(t, jl.Width == lineWidthForTest(hardLines[i]))
		}
	}
	tu.Assert(t, sawHardBreak)
}

func TestJustifyDoesNotChangeDefaultWrapping(t *testing.T) {
	// Default wrapping behavior must be independent of justification: wrap
	// twice and assert identical runs when no justification is applied.
	face := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("one two three four five six")
	run := shapeLine(t, text, face, di.DirectionLTR, language.Latin)
	width := (8 * run.Glyphs[0].Advance).Round()

	first, _ := (&LineWrapper{}).WrapParagraph(WrapConfig{}, width, text,
		NewSliceIterator([]Output{run.copy()}))
	second, _ := (&LineWrapper{}).WrapParagraph(WrapConfig{}, width, text,
		NewSliceIterator([]Output{run.copy()}))
	tu.Assert(t, len(first) == len(second))
	for i := range first {
		tu.Assert(t, len(first[i]) == len(second[i]))
		for j := range first[i] {
			tu.Assert(t, first[i][j].Advance == second[i][j].Advance)
			tu.Assert(t, len(first[i][j].Glyphs) == len(second[i][j].Glyphs))
		}
	}
}

func TestJustifyVertical(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("ab cd")
	run := shapeLine(t, text, face, di.DirectionTTB, language.Latin)
	tu.Assert(t, run.Advance < 0) // vertical advances are negative

	line := Line{run.copy()}
	before := clustersForTest(line)
	target := run.Advance - fixed.I(8) // more negative: a taller column
	res := JustifyLine(line, text, target, JustificationConfig{})
	tu.Assert(t, res.Width == target)
	tu.Assert(t, equalSlices(clustersForTest(res.Line), before))
	// The single space cluster (index 2) absorbs the expansion.
	for i, g := range res.Line[0].Glyphs {
		if i == 2 {
			tu.Assert(t, g.Advance == run.Glyphs[i].Advance-fixed.I(8))
			tu.Assert(t, g.YAdvance == run.Glyphs[i].YAdvance-fixed.I(8))
		} else {
			tu.Assert(t, g.Advance == run.Glyphs[i].Advance)
		}
	}
}

func TestJustifyCJKNoSpaceOnlyCJKGaps(t *testing.T) {
	face := loadTestdataFont(t, "common/mplus-1p-regular.ttf")
	text := []rune("あいうえお")
	run := shapeLine(t, text, face, di.DirectionLTR, language.Han)
	line := Line{run.copy()}
	natural := run.Advance

	// Cap below the deficit: four CJK gaps, each capped at 2 units.
	res := JustifyLine(line, text, natural+fixed.I(100), JustificationConfig{MaxExpansion: 2})
	tu.Assert(t, res.Width == natural+4*2)
}

func TestJustifyVerticalCapped(t *testing.T) {
	face := loadOpentypeFont(t, "../font/testdata/UbuntuMono-R.ttf")
	text := []rune("ab cd ef") // two space opportunities
	run := shapeLine(t, text, face, di.DirectionTTB, language.Latin)
	line := Line{run.copy()}

	// Each of the two space gaps is capped at 3 (sub-pixel) units, so the
	// column grows by exactly 6 units: its negative advance becomes 6 more
	// negative even though the requested target was much further away.
	target := run.Advance - fixed.I(100)
	res := JustifyLine(line, text, target, JustificationConfig{MaxExpansion: 3})
	expected := run.Advance - fixed.Int26_6(6)
	tu.Assert(t, res.Width == expected)
	tu.Assert(t, lineWidthForTest(res.Line) == expected)
}

// equalSlices is a small generic-free helper comparing cluster sequences.
func equalSlices(a, b [][2]int) bool {
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
