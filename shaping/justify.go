// SPDX-License-Identifier: Unlicense OR BSD-3-Clause

package shaping

import (
	"github.com/go-text/typesetting/di"
	ucd "github.com/go-text/typesetting/internal/unicodedata"
	"golang.org/x/image/math/fixed"
)

// JustificationConfig configures how a [Line] is justified to a target width.
//
// The zero value is ready to use and performs justification without limiting
// the expansion applied to a single justification opportunity.
type JustificationConfig struct {
	// MaxExpansion is the maximum amount of space that may be added at a
	// single justification opportunity. If zero or negative, the expansion
	// per opportunity is not limited.
	//
	// When the remaining space cannot be distributed because this cap would
	// be exceeded, the line is expanded as much as possible and its actual
	// width is returned in [JustifiedLine.Width].
	MaxExpansion fixed.Int26_6
}

// JustifiedLine is the result of justifying a [Line].
type JustifiedLine struct {
	// Line is the justified line. Justification mutates the glyph slices of
	// the input line in place (when they are not shared with another line),
	// so this field may alias the input storage. It remains valid under the
	// same constraints as the lines returned by [LineWrapper.WrapParagraph].
	Line Line
	// Width is the advance width of the line after justification, using the
	// signed convention of [Output.Advance]: positive for horizontal text,
	// negative for vertical text. When the requested width could not be
	// reached (no opportunities, capped expansion or a target narrower than
	// the natural line width), this is the actual, unmodified or partially
	// expanded, width of the line.
	Width fixed.Int26_6
}

// opportunityKind classifies a justification opportunity.
type opportunityKind uint8

const (
	// spaceOpportunity is a gap occupied by a UAX#14 Space (LB_SP) rune,
	// typically an ordinary word separator.
	spaceOpportunity opportunityKind = iota + 1
	// cjkOpportunity is a gap between two clusters opened by a large
	// East Asian ideographic (LB_ID, East_Asian_Width F/W/H) rune.
	cjkOpportunity
)

// justificationOpportunity is a location within a line where extra space
// can be inserted without splitting a glyph cluster.
type justificationOpportunity struct {
	runIdx   int // index of the run within the line
	glyphIdx int // index of the glyph whose advance carries the expansion
	kind     opportunityKind
	vertical bool // whether the containing run is laid out on the vertical axis
}

// lineWidth returns the signed advance width of a line.
func lineWidth(line Line) fixed.Int26_6 {
	var width fixed.Int26_6
	for i := range line {
		width += line[i].Advance
	}
	return width
}

// isHardBreakRune reports whether r is a UAX#14 mandatory line break class.
func isHardBreakRune(r rune) bool {
	switch ucd.LookupLineBreak(r) {
	case ucd.LB_BK, ucd.LB_LF, ucd.LB_CR, ucd.LB_NL:
		return true
	default:
		return false
	}
}

// lineEndsInHardBreak reports whether the line is terminated by a mandatory
// line break, rather than wrapped at an ordinary break opportunity.
func lineEndsInHardBreak(line Line, text []rune) bool {
	if len(line) == 0 {
		return false
	}
	// The last run in logical order is the final element of the line,
	// regardless of the visual order resolved by the bidi algorithm.
	lastRun := line[len(line)-1]
	lastRuneIdx := lastRun.Runes.Offset + lastRun.Runes.Count - 1
	if lastRuneIdx < 0 || lastRuneIdx >= len(text) {
		return false
	}
	return isHardBreakRune(text[lastRuneIdx])
}

// collectOpportunities finds the justification opportunities of a line.
//
// Only gaps internal to a single run are considered: gaps between two runs
// are never expanded, and a line edge can never be an opportunity. Glyph
// clusters are never split: an opportunity is always attached to a whole
// glyph of the shaped output.
//
// It returns ok == false if the line mixes text axes or contains an
// unexpected advance sign and must therefore be left unchanged.
func collectOpportunities(line Line, text []rune) (opps []justificationOpportunity, vertical bool, ok bool) {
	axisKnown := false
	for runIdx := range line {
		run := &line[runIdx]
		if len(run.Glyphs) == 0 {
			continue
		}
		runVertical := run.Direction.IsVertical()
		if axisKnown && runVertical != vertical {
			// A line mixing horizontal and vertical runs has no meaningful
			// single main axis; leave it untouched.
			return nil, false, false
		}
		axisKnown, vertical = true, runVertical
		rtl := run.Direction.Progression() == di.TowardTopLeft

		// Walk the glyphs cluster by cluster. The glyphs of a cluster are
		// contiguous and their count is given by GlyphCount on the first
		// glyph of the cluster.
		firstClusterIdx := run.Glyphs[0].ClusterIndex
		for blockStart := 0; blockStart < len(run.Glyphs); {
			g := &run.Glyphs[blockStart]
			glyphCount := g.GlyphCount
			if glyphCount <= 0 {
				// Defensive fallback: treat the glyph as its own cluster.
				glyphCount = 1
			}
			blockEnd := blockStart + glyphCount
			if blockEnd > len(run.Glyphs) {
				blockEnd = len(run.Glyphs)
			}

			// The cluster that begins the run has no predecessor inside the
			// run, so it can only open a run or line edge: never an internal
			// gap. This also excludes every run boundary, since the adjacent
			// content on the other side lives in another run and is not
			// considered.
			if g.ClusterIndex != firstClusterIdx {
				runeIdx := g.ClusterIndex
				if runeIdx >= 0 && runeIdx < len(text) {
					r := text[runeIdx]
					switch {
					case ucd.LookupLineBreak(r) == ucd.LB_SP:
						// A space glyph carries the gap itself. In visual
						// storage the space is the first glyph of its
						// cluster block in RTL and the only glyph in LTR.
						anchor := blockStart
						opps = append(opps, justificationOpportunity{
							runIdx:   runIdx,
							glyphIdx: anchor,
							kind:     spaceOpportunity,
							vertical: runVertical,
						})
					case ucd.LookupLineBreak(r) == ucd.LB_ID && ucd.IsLargeEastAsian(r):
						// A CJK gap opens just before the current cluster.
						// Anchor it on the first glyph of the current
						// cluster in LTR and on the last glyph of the
						// preceding cluster in RTL, matching the visual
						// side of the gap and the end-of-cluster spacing
						// convention used by Output.AddLetterSpacing.
						anchor := blockStart
						if rtl {
							anchor = blockStart - 1
							if anchor < 0 {
								anchor = 0
							}
						}
						opps = append(opps, justificationOpportunity{
							runIdx:   runIdx,
							glyphIdx: anchor,
							kind:     cjkOpportunity,
							vertical: runVertical,
						})
					}
				}
			}
			blockStart = blockEnd
		}
	}

	// Validate the advance signs on the main axis. Horizontal text must
	// advance positively and vertical text negatively; anything else makes
	// width arithmetic undefined, so the line is returned unchanged.
	for runIdx := range line {
		run := &line[runIdx]
		for glyphIdx := range run.Glyphs {
			advance := run.Glyphs[glyphIdx].Advance
			if vertical {
				if advance > 0 {
					return nil, false, false
				}
			} else if advance < 0 {
				return nil, false, false
			}
		}
	}
	return opps, vertical, true
}

// distributeExpansion splits total (a positive, main-axis magnitude) into
// n integer shares as evenly as possible. If perShareMax is positive, the
// total is first clamped so that no share exceeds it. The returned shares
// are all non-negative and their sum is the clamped total.
func distributeExpansion(total fixed.Int26_6, n int, perShareMax fixed.Int26_6) (shares []fixed.Int26_6, applied fixed.Int26_6) {
	if n == 0 || total <= 0 {
		return nil, 0
	}
	if perShareMax > 0 {
		capacity := int64(perShareMax) * int64(n)
		if int64(total) > capacity {
			if capacity > 1<<31-1 {
				total = 1<<31 - 1
			} else {
				total = fixed.Int26_6(capacity)
			}
		}
	}
	base := total / fixed.Int26_6(n)
	remainder := int(total % fixed.Int26_6(n)) // total and n are positive
	shares = make([]fixed.Int26_6, n)
	for i := range shares {
		share := base
		if i < remainder {
			share++
		}
		shares[i] = share
		applied += share
	}
	return shares, applied
}

// applyExpansion enlarges the opportunities of a line by the given shares,
// updating glyph advances, the space glyph offset (so that its content stays
// centered in the widened gap) and the cached run advances.
func applyExpansion(line Line, opps []justificationOpportunity, shares []fixed.Int26_6) {
	for i, opp := range opps {
		d := shares[i]
		if d == 0 {
			continue
		}
		g := &line[opp.runIdx].Glyphs[opp.glyphIdx]
		if opp.vertical {
			// Vertical advances are negative: widening the gap means moving
			// the next dot further in the progression direction.
			g.Advance -= d
			g.YAdvance -= d
			if opp.kind == spaceOpportunity {
				// Match the convention used by Output.AddWordSpacing for
				// vertical word spacing.
				g.YOffset += d / 2
			}
		} else {
			g.Advance += d
			g.XAdvance += d
			if opp.kind == spaceOpportunity {
				// Keep the (usually empty) space glyph content centered in
				// the enlarged gap.
				g.XOffset += d / 2
			}
		}
	}
	// Recompute the cached advance of every run that carries an opportunity.
	// Multiple opportunities may share a run, so each run index is handled
	// only once.
	recomputed := make(map[int]struct{}, len(opps))
	for _, opp := range opps {
		if _, done := recomputed[opp.runIdx]; done {
			continue
		}
		recomputed[opp.runIdx] = struct{}{}
		line[opp.runIdx].RecomputeAdvance()
	}
}

// JustifyLine adjusts the glyph positions of line so that it fills targetWidth,
// expanding only legal whitespace and CJK gaps of non-final content. It never
// splits glyph clusters and never shrinks a line: if targetWidth is not larger
// than the natural line width, the line is returned unchanged.
//
// text is the paragraph runes the line was shaped and wrapped from.
//
// The following lines are returned unchanged, with their natural width:
//   - lines terminated by a mandatory (hard) line break,
//   - lines without any justification opportunity,
//   - lines mixing horizontal and vertical runs or containing unexpected
//     advance signs.
//
// When [JustificationConfig.MaxExpansion] prevents the line from reaching
// targetWidth, the line is expanded up to the cap and its actual width is
// reported in the result.
func JustifyLine(line Line, text []rune, targetWidth fixed.Int26_6, config JustificationConfig) JustifiedLine {
	if len(line) == 0 {
		return JustifiedLine{Line: line}
	}
	if lineEndsInHardBreak(line, text) {
		return JustifiedLine{Line: line, Width: lineWidth(line)}
	}
	opps, vertical, ok := collectOpportunities(line, text)
	if !ok || len(opps) == 0 {
		return JustifiedLine{Line: line, Width: lineWidth(line)}
	}

	width := lineWidth(line)
	if vertical {
		// Vertical line advances are negative; reason on the positive
		// magnitude and convert back when storing the result.
		if targetWidth >= width {
			return JustifiedLine{Line: line, Width: width}
		}
		deficit := clampDeficit(width-targetWidth, len(opps))
		shares, applied := distributeExpansion(deficit, len(opps), config.MaxExpansion)
		applyExpansion(line, opps, shares)
		return JustifiedLine{Line: line, Width: width - applied}
	}
	if targetWidth <= width {
		// Justification only expands; a target at or below the natural width
		// leaves the line untouched.
		return JustifiedLine{Line: line, Width: width}
	}
	deficit := clampDeficit(targetWidth-width, len(opps))
	shares, applied := distributeExpansion(deficit, len(opps), config.MaxExpansion)
	applyExpansion(line, opps, shares)
	return JustifiedLine{Line: line, Width: width + applied}
}

// maxInt26_6 is the largest value representable by fixed.Int26_6.
const maxInt26_6 = fixed.Int26_6(1<<31 - 1)

// clampDeficit bounds the total expansion so that distributing it among
// opportunities and adding it to the glyph advances cannot overflow the
// fixed.Int26_6 representation.
func clampDeficit(deficit fixed.Int26_6, opportunities int) fixed.Int26_6 {
	if deficit <= 0 {
		return 0
	}
	capacity := maxInt26_6 / fixed.Int26_6(opportunities)
	if deficit > capacity {
		return capacity
	}
	return deficit
}

// JustifyParagraph justifies the lines produced by [LineWrapper.WrapParagraph]
// (or successive [LineWrapper.WrapNextLine] calls) to the constant targetWidth.
//
// Every line except the last one is justified unless it ends with a mandatory
// line break. The last line of a paragraph keeps its natural width, following
// the usual typographic convention. text is the paragraph rune slice used for
// shaping and wrapping, and config controls the per-opportunity expansion cap.
//
// The returned slice has one entry per input line. See [JustifyLine] for the
// edge cases in which a line is left unchanged.
func JustifyParagraph(lines []Line, text []rune, targetWidth fixed.Int26_6, config JustificationConfig) []JustifiedLine {
	out := make([]JustifiedLine, len(lines))
	last := len(lines) - 1
	for i, line := range lines {
		// The final line keeps its natural width, as does any line closed by
		// a mandatory break; every other line is stretched to the target.
		if i != last && !lineEndsInHardBreak(line, text) {
			out[i] = JustifyLine(line, text, targetWidth, config)
		} else {
			out[i] = JustifiedLine{Line: line, Width: lineWidth(line)}
		}
	}
	return out
}
