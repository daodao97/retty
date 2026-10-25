package terminal

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Link is a printed URL or a file reference. Line and Column are 1-based;
// zero means the output did not specify a location.
type Link struct {
	URL, Path    string
	Line, Column int
}

type linkMatch struct {
	Link
	start, end        int // rune indices, excluding surrounding punctuation
	cellA, cellB, row int
}

// Unquoted references in Chinese prose end at these sentence delimiters.
// Quoted filenames may still contain them literally.
const linkProseDelimiters = "，。；：！？、（）【】《》「」『』"

var pathTokenPattern = regexp.MustCompile(`"[^"\r\n]+"(?::[0-9]+(?::[0-9]+)?)?|'[^'\r\n]+'(?::[0-9]+(?::[0-9]+)?)?|[^\s"'<>` + "`" + linkProseDelimiters + `]+`)
var pathLocationPattern = regexp.MustCompile(`:([0-9]+)(?::([0-9]+))?:?$|\(([0-9]+)(?:,([0-9]+))?\)$|#L([0-9]+)(?:C([0-9]+))?$`)

func matchLink(text []rune, cols []int, col int) linkMatch {
	s := string(text)
	contains := func(m linkMatch) bool {
		if m.start < 0 || m.end <= m.start || m.end > len(cols) {
			return false
		}
		last := cols[m.end-1]
		if m.end < len(cols) {
			last = max(last, cols[m.end]-1)
		}
		return cols[m.start] <= col && col <= last
	}
	for _, bounds := range urlPattern.FindAllStringIndex(s, -1) {
		u := trimURL(trimLinkToken(s[bounds[0]:bounds[1]]))
		start := utf8.RuneCountInString(s[:bounds[0]])
		m := linkMatch{Link: Link{URL: u}, start: start, end: start + utf8.RuneCountInString(u)}
		if contains(m) {
			return m
		}
	}
	for _, bounds := range pathTokenPattern.FindAllStringIndex(s, -1) {
		raw := s[bounds[0]:bounds[1]]
		prefix := len(raw) - len(strings.TrimLeft(raw, "([{*"))
		raw = raw[prefix:]
		raw = trimLinkToken(raw)
		raw = strings.TrimRight(raw, ",;.!]")
		// Programs print references in wrappers such as write(notes.md).
		// Keep parentheses inside filenames and compiler locations intact.
		if open := strings.IndexByte(raw, '('); open > 0 && strings.HasSuffix(raw, ")") &&
			!pathLocationPattern.MatchString(raw) && !strings.ContainsAny(raw[:open], `/.:"'`) {
			prefix += open + 1
			raw = raw[open+1:]
		}
		// A surrounding closing parenthesis is punctuation, but file.ts(4,2)
		// is a compiler location and must be parsed before trimming it.
		if !pathLocationPattern.MatchString(raw) {
			raw = trimURL(raw)
		}
		path := raw
		line, column := 0, 0
		if loc := pathLocationPattern.FindStringSubmatchIndex(raw); loc != nil {
			path = raw[:loc[0]]
			for i := 2; i < len(loc); i += 4 {
				if loc[i] >= 0 {
					line, _ = strconv.Atoi(raw[loc[i]:loc[i+1]])
					if i+3 < len(loc) && loc[i+2] >= 0 {
						column, _ = strconv.Atoi(raw[loc[i+2]:loc[i+3]])
					}
					break
				}
			}
			if line <= 0 {
				continue
			}
		}
		path = strings.Trim(path, "\"'")
		if path == "" || strings.Contains(path, "://") || strings.ContainsAny(path, "\x00\r\n") {
			continue
		}
		// Bare prose is not a path. Extensionless files still work when the
		// output includes a directory or a line location.
		base := path[strings.LastIndex(path, "/")+1:]
		dot := strings.LastIndex(base, ".")
		if !strings.Contains(path, "/") && line == 0 && (dot < 0 || dot == len(base)-1) {
			continue
		}
		if strings.Contains(path, ":") || (strings.Contains(path, " ") && raw[0] != '"' && raw[0] != '\'') {
			continue
		}
		start := utf8.RuneCountInString(s[:bounds[0]+prefix])
		m := linkMatch{Link: Link{Path: path, Line: line, Column: column}, start: start, end: start + utf8.RuneCountInString(raw)}
		if contains(m) {
			return m
		}
	}
	return linkMatch{}
}

// A closing wrapper may directly precede prose without whitespace, as in
// "(image.png)已保存". Keep balanced filename brackets and compiler locations.
func trimLinkToken(raw string) string {
	if strings.HasPrefix(raw, `"`) || strings.HasPrefix(raw, "'") {
		return raw
	}
	var depth [3]int
	for i, r := range raw {
		if bracket := strings.IndexRune("([{", r); bracket >= 0 {
			depth[bracket]++
		} else if bracket := strings.IndexRune(")]}", r); bracket >= 0 {
			if depth[bracket] == 0 {
				return raw[:i]
			}
			depth[bracket]--
		}
	}
	return raw
}

func (m linkMatch) valid() bool { return m.URL != "" || m.Path != "" }

// linkAt reads just the pointed logical line, never the full scrollback.
// The emulator lock is held by its caller.
func (v *view) linkAt(x, y float32) linkMatch {
	screen := v.screen()
	if screen == nil || v.cellW == 0 || x*v.scale < float32(v.ox) || y*v.scale < float32(v.oy) ||
		x*v.scale >= float32(v.ox+v.cols*v.cellW) || y*v.scale >= float32(v.oy+v.rows*v.cellH) {
		return linkMatch{}
	}
	col, row := v.cellAt(x, y)
	if ref, ok := screen.CellAt(col, row); ok {
		if url := screen.Hyperlink(ref); url != "" {
			a, b := col, col+1
			for a > 0 {
				ref, ok := screen.CellAt(a-1, row)
				if !ok || screen.Hyperlink(ref) != url {
					break
				}
				a--
			}
			for b < v.cols {
				ref, ok := screen.CellAt(b, row)
				if !ok || screen.Hyperlink(ref) != url {
					break
				}
				b++
			}
			return linkMatch{Link: Link{URL: url}, cellA: a, cellB: b, row: row}
		}
	}
	if m := v.hardWrappedFileAt(col, row); m.valid() {
		return m
	}
	text, cols, start := screen.LogicalRowText(row)
	m := matchLink(text, cols, col+(row-start)*v.cols)
	if m.valid() {
		m.setCells(cols, start)
	}
	return m
}

func (m *linkMatch) setCells(cols []int, start int) {
	m.cellA, m.cellB, m.row = cols[m.start], cols[m.end-1]+1, start
	if m.end < len(cols) {
		m.cellB = max(m.cellB, cols[m.end])
	}
}

// An indented continuation has gaps between its display rows. Only underline
// and hit-test the actual path cells in the pointed row, never table padding.
func (m *linkMatch) setCellsInRow(cols []int, start, row, width int) {
	offset := (row - start) * width
	m.cellA, m.cellB, m.row = width, 0, row
	for i := m.start; i < m.end; i++ {
		x := cols[i] - offset
		if x < 0 || x >= width {
			continue
		}
		right := x + 1
		if i+1 < len(cols) && cols[i+1]-offset < width {
			right = max(right, cols[i+1]-offset)
		}
		m.cellA, m.cellB = min(m.cellA, x), max(m.cellB, right)
	}
}

// Some TUIs draw each display row themselves instead of letting the emulator
// wrap it. Recover file references at those edges, without changing the VT's
// actual line boundaries (selection, search and ordinary URLs still use them).
func (v *view) hardWrappedFileAt(col, row int) linkMatch {
	screen := v.screen()
	first, last := max(0, row-8), min(v.rows-1, row+7)
	texts, columns := make([][]rune, last-first+1), make([][]int, last-first+1)
	for y := first; y <= last; y++ {
		r, c := screen.RowText(y)
		end := len(r)
		for end > 0 && unicode.IsSpace(r[end-1]) {
			end--
		}
		texts[y-first], columns[y-first] = r[:end], c[:end]
	}
	for start := first; start <= row; start++ {
		text := append([]rune(nil), texts[start-first]...)
		cols := append([]int(nil), columns[start-first]...)
		for end := start; end < last; end++ {
			r, c := texts[end-first], columns[end-first]
			if len(r) == 0 || screen.RowsJoined(end) ||
				v.cols-c[len(c)-1]-1 > 8 || !unfinishedFileAtEdge(text, cols) {
				break
			}
			next, nc := texts[end+1-first], columns[end+1-first]
			a := 0
			for a < len(next) && unicode.IsSpace(next[a]) {
				a++
			}
			b := a
			for b < len(next) && !unicode.IsSpace(next[b]) && !strings.ContainsRune(linkProseDelimiters, next[b]) {
				b++
			}
			edge := matchLink(text, cols, cols[len(cols)-1])
			// A table continuation starts at its cell's left edge, which can
			// be far from column zero but must not pass the original path.
			indent := max(4, cols[edge.start]%v.cols)
			if a == b || nc[a] > indent || !fileContinuation(string(next[a:b])) {
				break
			}
			text = append(text, next[a:b]...)
			for _, x := range nc[a:b] {
				cols = append(cols, x+(end+1-start)*v.cols)
			}
			if end+1 >= row {
				m := matchLink(text, cols, col+(row-start)*v.cols)
				if m.valid() && (m.Path != "" || strings.HasPrefix(m.URL, "file://")) {
					m.setCellsInRow(cols, start, row, v.cols)
					if col < m.cellA || col >= m.cellB {
						break
					}
					// Keep reading if another row may finish the filename.
					if b < len(next) || end+1 == last || !unfinishedFileAtEdge(text, cols) ||
						v.cols-nc[b-1]-1 > 8 {
						return m
					}
				}
			}
			if b < len(next) {
				break
			}
		}
	}
	return linkMatch{}
}

func unfinishedFileAtEdge(text []rune, cols []int) bool {
	if len(cols) == 0 {
		return false
	}
	m := matchLink(text, cols, cols[len(cols)-1])
	if !m.valid() || m.end != len(text) || m.Line != 0 {
		return false
	}
	path := m.Path
	if m.URL != "" {
		if !strings.HasPrefix(m.URL, "file://") {
			return false
		}
		path = strings.TrimPrefix(m.URL, "file://")
	}
	if !strings.Contains(path, "/") {
		return false
	}
	base := path[strings.LastIndex(path, "/")+1:]
	// A completed filename/location is a separate reference, even near an edge.
	return !strings.Contains(base, ".") || strings.HasSuffix(base, "-")
}

func fileContinuation(s string) bool {
	s = trimURL(trimLinkToken(s))
	if loc := pathLocationPattern.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	if s == "" {
		return false
	}
	if strings.Contains(s, "://") || strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "~/") {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("_-.%/:#", r) {
			return false
		}
	}
	return true
}

// urlPattern finds the URLs programs print as text, which a Command+click
// opens as it does hyperlinks.
var urlPattern = regexp.MustCompile(`(?:https?|ftp|file)://[^\s"'<>` + "`" + linkProseDelimiters + `\x00-\x1f]+|mailto:[^\s"'<>` + "`" + linkProseDelimiters + `]+`)

// urlAt returns the URL of text, a row whose runes are in columns cols, at
// column col, or "".
func urlAt(text []rune, cols []int, col int) string {
	s := string(text)
	for _, m := range urlPattern.FindAllStringIndex(s, -1) {
		u := trimURL(trimLinkToken(s[m[0]:m[1]]))
		start := len([]rune(s[:m[0]]))
		end := start + len([]rune(u))
		if start < len(cols) && cols[start] <= col && col <= cols[end-1] {
			return u
		}
	}
	return ""
}

// trimURL trims what likely ends the sentence around a URL rather than the
// URL: punctuation, and closing brackets that the URL did not open.
func trimURL(u string) string {
	for len(u) > 0 {
		switch last := u[len(u)-1]; last {
		case '.', ',', ';', ':', '!', '?':
			u = u[:len(u)-1]
			continue
		case ')', ']', '}':
			open := map[byte]string{')': "(", ']': "[", '}': "{"}[last]
			if strings.Count(u, open) < strings.Count(u, string(last)) {
				u = u[:len(u)-1]
				continue
			}
		}
		return u
	}
	return u
}
