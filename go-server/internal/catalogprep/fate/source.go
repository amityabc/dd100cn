package fate

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	gameplay "s9a1/server/internal/fate"
)

type source struct {
	a        Archive
	err      error
	texts    map[string]string
	indices  map[string]map[int64]string
	identity map[string]string
	units    int
}

func newSource(a Archive) *source {
	return &source{a: a, texts: map[string]string{}, indices: map[string]map[int64]string{}, identity: map[string]string{}}
}

func (s *source) require(ok bool, message string) bool {
	if !ok && s.err == nil {
		s.err = fmt.Errorf("Destiny2: %s", message)
	}
	return ok
}

func (s *source) recordText(path, text string) string {
	text = strings.ReplaceAll(text, "\r", "")
	units := len(utf16.Encode([]rune(text)))
	if s.require(units <= 2*budget && len(s.texts) < 256 && s.units+units <= 8*budget, "activity startup text budget exceeded") {
		s.texts[path] = text
		s.units += units
		s.hash(path)
	}
	return text
}

func (s *source) text(path string) string {
	if text, ok := s.texts[path]; ok {
		return text
	}
	if s.err != nil {
		return ""
	}
	text, err := s.a.ReadText(path, budget)
	if err != nil {
		s.err = fmt.Errorf("Destiny2 %s: %w", path, err)
		return ""
	}
	return s.recordText(path, text)
}

func (s *source) hash(path string) string {
	if h, ok := s.identity[path]; ok {
		return h
	}
	if s.err != nil {
		return ""
	}
	bytes, err := s.a.Read(path, budget)
	if err != nil {
		s.err = fmt.Errorf("Destiny2 resource hash %s: %w", path, err)
		return ""
	}
	h := fmt.Sprintf("%X", sha256.Sum256(bytes))
	s.identity[path] = h
	return h
}

func (s *source) ability() (string, string) {
	if s.err != nil {
		return "", ""
	}
	text, err := s.a.ReadFateAbilityText()
	if err != nil {
		s.err = fmt.Errorf("Destiny2 ability: %w", err)
		return "", ""
	}
	bytes, err := s.a.ReadFateAbility()
	if err != nil {
		s.err = fmt.Errorf("Destiny2 ability hash: %w", err)
		return "", ""
	}
	h := fmt.Sprintf("%X", sha256.Sum256(bytes))
	s.identity[abilityPath] = h
	return strings.ReplaceAll(text, "\r", ""), h
}

func (s *source) index(category string) map[int64]string {
	if index, ok := s.indices[category]; ok {
		return index
	}
	if s.err != nil || !s.require(len(s.indices) < 10, "activity startup index budget exceeded") {
		return nil
	}
	var index map[int64]string
	var err error
	switch category {
	case "passiveobject":
		index, err = s.a.ReadFatePortalIndex()
		s.hash("list/passiveobject.lst")
	case "equipment":
		index, err = s.a.ReadIndex(category, 4*budget)
	default:
		index, err = s.a.ReadIndex(category, budget)
	}
	if err != nil {
		s.err = fmt.Errorf("Destiny2 %s index: %w", category, err)
		return nil
	}
	s.indices[category] = index
	return index
}

func (s *source) resolve(category string, id uint32) string {
	path := s.index(category)[int64(id)]
	if !s.require(path != "", fmt.Sprintf("missing source %s/%d", category, id)) {
		return ""
	}
	path = strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	s.identity[fmt.Sprintf("%s:%d", category, id)] = path
	return path
}

func (s *source) resolveText(category string, id uint32) string {
	return s.text(s.resolve(category, id))
}

func (s *source) ruleHash() string {
	keys := make([]string, 0, len(s.identity))
	for key := range s.identity {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", key, s.identity[key])
	}
	return fmt.Sprintf("%X", h.Sum(nil))
}

func tagLine(text, tag string) [][]int {
	return regexp.MustCompile(`(?im)^\[`+regexp.QuoteMeta(tag)+`\][ \t]*$`).FindAllStringIndex(text, -1)
}

func has(text, tag string) bool {
	return len(tagLine(text, tag)) == 1
}

func blocks(text, tag string) []string {
	t := regexp.QuoteMeta(tag)
	rows := regexp.MustCompile(`(?ims)^\[`+t+`\][ \t]*\n(.*?)^\[/`+t+`\][ \t]*$`).FindAllStringSubmatch(text, -1)
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row[1]
	}
	return out
}

func (s *source) block(text, tag string) string {
	rows := blocks(text, tag)
	if !s.require(len(rows) == 1, "missing or ambiguous block "+tag) {
		return ""
	}
	return rows[0]
}

func sections(text, tag string) []string {
	var out []string
	for _, match := range tagLine(text, tag) {
		rest := strings.TrimPrefix(text[match[1]:], "\n")
		if end := nextTag.FindStringIndex(rest); end != nil {
			rest = rest[:end[0]]
		}
		out = append(out, strings.TrimSpace(rest))
	}
	return out
}

var nextTag = regexp.MustCompile(`(?m)^\[`)

func (s *source) section(text, tag string, optional bool) string {
	rows := sections(text, tag)
	if optional && len(rows) == 0 {
		return ""
	}
	if !s.require(len(rows) == 1, "missing or ambiguous section "+tag) {
		return ""
	}
	return rows[0]
}

var tokenPattern = regexp.MustCompile("`[^`]*`|[^\\s]+")

func tokens(text string) []string {
	return tokenPattern.FindAllString(text, -1)
}

func (s *source) integer(value string) int {
	n, err := strconv.ParseInt(value, 10, 32)
	s.require(err == nil, "invalid integer "+strconv.Quote(value))
	return int(n)
}

func (s *source) numbers(value string) []int {
	words := tokens(value)
	out := make([]int, len(words))
	for i, word := range words {
		out[i] = s.integer(word)
	}
	return out
}

func (s *source) one(value string) int {
	n := s.numbers(value)
	if !s.require(len(n) == 1, "invalid scalar width") {
		return 0
	}
	return n[0]
}

func (s *source) scalar(text, tag string) int {
	return s.one(s.section(text, tag, false))
}

func (s *source) coordinate(n int) uint16 {
	s.require(n >= 0 && n <= 32767, "activity coordinate exceeds native range")
	return uint16(n)
}

func (s *source) id(n int) uint32 {
	s.require(n > 0, "activity identifier must be positive")
	return uint32(n)
}

func (s *source) path(value string) string {
	path := strings.ToLower(strings.ReplaceAll(s.quoted(value), "\\", "/"))
	ok := len(path) > 0 && len(path) <= 512 && !strings.Contains(path, ":")
	for _, part := range strings.Split(path, "/") {
		ok = ok && part != "" && part != "." && part != ".."
	}
	s.require(ok, "activity path is invalid")
	return path
}

func (s *source) quoted(value string) string {
	words := tokens(value)
	if !s.require(len(words) == 1 && len(words[0]) >= 2 && words[0][0] == '`' && words[0][len(words[0])-1] == '`', "invalid activity string") {
		return ""
	}
	return words[0][1 : len(words[0])-1]
}

func validWaves(values []int) bool {
	for _, wave := range values {
		if wave < 1 || wave > 57 {
			return false
		}
	}
	return true
}

func (s *source) ranks(body string, sin bool) []gameplay.RankRule {
	words := tokens(body)
	width := 9
	if sin {
		width = 10
	}
	if !s.require(len(words) == 7*width, "activity rank row is incomplete") {
		return nil
	}
	var out []gameplay.RankRule
	for at := 0; at < len(words); at += width {
		s.require(words[at][0] == '`' && s.integer(words[at+1]) == at/width && words[at+2][0] == '`' && words[at+3][0] == '`' && words[at+4][0] == '`', "invalid activity rank identity")
		maximum, cost := s.integer(words[at+5]), s.integer(words[at+6])
		s.require(maximum >= 1 && maximum <= 5 && cost > 0, "invalid activity rank capacity or cost")
		for _, word := range words[at+7 : at+width] {
			s.integer(word)
		}
		out = append(out, gameplay.RankRule{Maximum: int32(maximum), Cost: int32(cost)})
	}
	return out
}

func (s *source) points(body string) map[int][]int {
	values := s.numbers(body)
	if !s.require(len(values) == 42, "invalid seven-node five-phase point table") {
		return nil
	}
	out := map[int][]int{}
	for at := 0; at < len(values); at += 6 {
		node, row := values[at], values[at+1:at+6]
		s.require(node >= 1 && node <= 7 && out[node] == nil && slices.Min(row) >= 0, "invalid or duplicate activity point row")
		out[node] = row
	}
	return out
}

func (s *source) layouts(grid int, text string) []gameplay.Layout {
	edges := map[int][]int{}
	values := s.numbers(s.block(text, "grid preset info"))
	for n := 0; n < len(values); {
		if !s.require(len(values)-n >= 2, "truncated activity edge header") {
			return nil
		}
		id, count := values[n], values[n+1]
		n += 2
		if !s.require(id >= 0 && id <= 255 && count >= 0 && count <= 255 && len(values)-n >= count && edges[id] == nil, "invalid or duplicate activity edge") {
			return nil
		}
		next := append([]int{}, values[n:n+count]...)
		n += count
		seen := map[int]bool{}
		for _, v := range next {
			s.require(v >= 0 && v <= 255 && !seen[v], "duplicate activity grid connection")
			seen[v] = true
		}
		edges[id] = next
	}
	s.require(len(edges) > 0 && len(edges) <= 256 && edges[0] != nil, "activity grid must have entry node zero")
	for _, next := range edges {
		for _, v := range next {
			s.require(edges[v] != nil, "activity grid refers to unknown node")
		}
	}
	presets := blocks(text, "node preset")
	s.require(len(presets) > 0 && len(presets) <= 256, "invalid activity preset count")
	var out []gameplay.Layout
	for i, preset := range presets {
		nodes := map[int]gameplay.Node{}
		for _, body := range blocks(preset, "node") {
			id, typ := s.scalar(body, "id"), s.scalar(body, "type")
			s.require(edges[id] != nil && typ >= 1 && typ <= 7 && nodes[id].Type == 0 && (typ != 3 || len(edges[id]) == 0), "invalid or duplicate activity node")
			var fixed string
			if has(body, "/fixed phase spawn") {
				fixed = s.block(body, "fixed phase spawn")
			} else {
				fixed = s.section(body, "fixed phase spawn", true)
			}
			nodes[id] = gameplay.Node{Id: id, Type: typ, Next: edges[id], Hidden: has(body, "hide"), FixedWaves: s.fixedWaves(fixed)}
		}
		s.require(len(nodes) == len(edges), "incomplete activity node preset")
		out = append(out, gameplay.Layout{Grid: grid, Preset: i, Nodes: nodes})
	}
	return out
}

func (s *source) fixedWaves(text string) [][]int {
	out := [][]int{}
	if strings.TrimSpace(text) == "" {
		return out
	}
	rows := s.numbers(text)
	at := 0
	for phase := 0; phase < 5; phase++ {
		if !s.require(len(rows)-at >= 2 && rows[at] == phase, "invalid fixed wave phase") {
			return nil
		}
		n := rows[at+1]
		at += 2
		if !s.require(n > 0 && n <= 57 && len(rows)-at >= n && validWaves(rows[at:at+n]), "invalid fixed wave list") {
			return nil
		}
		out = append(out, rows[at:at+n])
		at += n
	}
	s.require(at == len(rows), "trailing fixed activity waves")
	return out
}
