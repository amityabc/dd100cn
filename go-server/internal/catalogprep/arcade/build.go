// Package arcade prepares the native arcade and training definitions from PVF.
package arcade

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/duellobby"
)

const (
	root   = "etc/pvparcade/"
	budget = 1024 * 1024
)

type Archive interface {
	Read(string, int) ([]byte, error)
	ReadText(string, int) (string, error)
	ReadIndex(string, ...int) (map[int64]string, error)
}

type mode struct {
	difficulty, rounds, revives byte
	pool                        []uint32
	count                       int
}

type specific struct {
	fixed map[byte]uint32
	pool  []uint32
}

type source struct {
	archive Archive
	hashes  map[string]string
	err     error
}

var nativeIndices = []byte{0, 1, 2, 80, 81, 82}

func Build(archive Archive) ([]duellobby.ArcadeRow, error) {
	s := source{archive: archive, hashes: map[string]string{}}
	modes := s.modes(s.text(root + "arcadeinfo.etc"))
	specifics := s.specific(modes, s.text(root+"arcadeapcinfolist.etc"))
	if s.err != nil {
		return nil, s.err
	}
	var rows []duellobby.ArcadeRow
	opp := map[uint32]bool{}
	for job := byte(0); job < 16; job++ {
		for grow := byte(0); grow <= 5; grow++ {
			for _, index := range nativeIndices {
				m, ok := modes[index]
				own := specifics[[3]byte{job, grow, index}]
				if !ok || m.count+len(own.fixed) < int(m.rounds) {
					continue
				}
				if own.fixed == nil {
					own.fixed = map[byte]uint32{}
				}
				pool := m.pool
				if len(own.pool) > 0 {
					pool = own.pool
				}
				allowed := slices.Clone(pool)
				for _, v := range own.fixed {
					allowed = append(allowed, v)
				}
				slices.Sort(allowed)
				allowed = slices.Compact(allowed)
				for _, v := range allowed {
					opp[v] = true
				}
				rows = append(rows, duellobby.ArcadeRow{Job: job, Grow: grow, Definition: duellobby.ArcadeDefinition{
					Index: index, Difficulty: m.difficulty, Rounds: m.rounds, MaximumRevives: m.revives, Opponents: allowed,
					FixedOpponents: own.fixed, RandomOpponents: pool,
				}})
			}
		}
	}
	if len(opp) > 0 {
		s.opponents(opp)
	}
	if s.err != nil {
		return nil, s.err
	}
	hash := s.digest()
	for i := range rows {
		rows[i].Definition.SourceHash = hash
	}
	if _, err := duellobby.NewArcadeCatalog(rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *source) require(ok bool, why string) bool {
	if !ok && s.err == nil {
		s.err = fmt.Errorf("arcade: %s", why)
	}
	return ok
}

func (s *source) text(path string) string {
	if s.err != nil {
		return ""
	}
	value, err := s.archive.ReadText(path, budget)
	if err != nil {
		s.err = fmt.Errorf("arcade %s: %w", path, err)
		return ""
	}
	if _, seen := s.hashes[path]; !seen {
		raw, err := s.archive.Read(path, budget)
		if err != nil {
			s.err = fmt.Errorf("arcade source hash %s: %w", path, err)
			return ""
		}
		s.hashes[path] = fmt.Sprintf("%X", sha256.Sum256(raw))
	}
	return strings.ReplaceAll(value, "\r", "")
}

func (s *source) opponents(needed map[uint32]bool) {
	index, err := s.archive.ReadIndex("aicharacter", budget)
	if err != nil {
		s.err = fmt.Errorf("arcade APC index: %w", err)
		return
	}
	path := "list/aicharacter.lst"
	raw, err := s.archive.Read(path, budget)
	if errors.Is(err, fs.ErrNotExist) {
		path = "aicharacter/aicharacter.lst"
		raw, err = s.archive.Read(path, budget)
	}
	if err != nil {
		s.err = fmt.Errorf("arcade APC index hash: %w", err)
		return
	}
	s.hashes[path] = fmt.Sprintf("%X", sha256.Sum256(raw))
	ids := make([]uint32, 0, len(needed))
	for id := range needed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		path := strings.ToLower(strings.ReplaceAll(index[int64(id)], "\\", "/"))
		if !s.require(path != "", fmt.Sprintf("unknown APC template %d", id)) {
			return
		}
		if !s.require(strings.TrimSpace(s.text(path)) != "", fmt.Sprintf("empty APC template %d", id)) {
			return
		}
	}
}

func (s *source) digest() string {
	keys := make([]string, 0, len(s.hashes))
	for key := range s.hashes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	digest := sha256.New()
	fmt.Fprint(digest, "LocalArcadeCatalog:v2:native012-training808182:fixed-rounds:specific-random-override:default-capacity\n")
	for _, key := range keys {
		fmt.Fprintf(digest, "%s\n%s\n", key, s.hashes[key])
	}
	return fmt.Sprintf("%X", digest.Sum(nil))
}

func (s *source) scalar(text, tag string, lo, hi int) int {
	numbers, err := pvftext.Numbers(text, tag)
	if !s.require(err == nil && len(numbers) == 1 && numbers[0] >= lo && numbers[0] <= hi, "invalid scalar "+tag) {
		return 0
	}
	return numbers[0]
}

func (s *source) ints(text string) []int64 {
	var out []int64
	for _, word := range strings.Fields(text) {
		n, err := strconv.ParseInt(word, 10, 64)
		if !s.require(err == nil, "invalid integer "+word) {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func (s *source) id(v int64) uint32 {
	s.require(v > 0 && v <= 1<<32-1, "invalid APC identity")
	return uint32(v)
}

func (s *source) ids(text string) ([]uint32, int) {
	r := s.ints(text)
	ids := make([]uint32, len(r))
	for i, v := range r {
		ids[i] = s.id(v)
	}
	slices.Sort(ids)
	return slices.Compact(ids), len(r)
}

func tagPattern(tag string) *regexp.Regexp {
	return regexp.MustCompile(`(?im)^\[` + regexp.QuoteMeta(tag) + `\][ \t]*$`)
}

func (s *source) blocks(text, tag string, required bool) []string {
	open := tagPattern(tag).FindAllStringIndex(text, -1)
	close := tagPattern("/"+tag).FindAllStringIndex(text, -1)
	if !s.require(len(open) == len(close) && (!required || len(open) > 0), "missing or malformed block "+tag) {
		return nil
	}
	out := make([]string, len(open))
	for i, row := range open {
		if !s.require(row[1] <= close[i][0] && (i == 0 || row[0] >= close[i-1][1]), "malformed block "+tag) {
			return nil
		}
		out[i] = strings.TrimSpace(text[row[1]:close[i][0]])
	}
	return out
}

func (s *source) block(text, tag string) string {
	rows := s.blocks(text, tag, true)
	if !s.require(len(rows) == 1, "missing or duplicate block "+tag) {
		return ""
	}
	return rows[0]
}

func (s *source) modes(text string) map[byte]mode {
	modes := map[byte]mode{}
	seen := map[int]bool{}
	for _, body := range s.blocks(text, "Arcade Info", true) {
		i := s.scalar(body, "index", 0, 1<<31-1)
		s.require(!seen[i], "duplicate arcade mode")
		seen[i] = true
		if i > 82 || !slices.Contains(nativeIndices, byte(i)) {
			continue
		}
		d := s.scalar(body, "difficulty", 0, 2)
		s.require(i > 2 || i == d, "difficulty and arcade index differ")
		rounds := s.scalar(body, "max round", 1, 255)
		revives := s.scalar(body, "max UsebleCoin", 0, 255)
		ids, count := s.ids(s.block(body, "Random APC Index"))
		s.require(count > 0, "empty arcade default pool")
		modes[byte(i)] = mode{byte(d), byte(rounds), byte(revives), ids, count}
	}
	for i := byte(0); i <= 2; i++ {
		s.require(modes[i].count > 0, "missing native arcade difficulty")
	}
	return modes
}

var (
	jobPattern   = regexp.MustCompile("(?i)^\\s*(\\d+)\\s+`([A-Za-z0-9_]+\\.etc)`")
	indexPattern = tagPattern("Arcade index")
)

func (s *source) specific(modes map[byte]mode, text string) map[[3]byte]specific {
	rows := map[[3]byte]specific{}
	jobs := map[byte]bool{}
	for strings.TrimSpace(text) != "" {
		match := jobPattern.FindStringSubmatchIndex(text)
		if !s.require(len(match) == 6, "malformed arcade job index") {
			break
		}
		i, err := strconv.ParseUint(text[match[2]:match[3]], 10, 8)
		job := byte(i)
		if !s.require(err == nil && job <= 15 && !jobs[job], "invalid or duplicate arcade job") {
			break
		}
		jobs[job] = true
		path := strings.ToLower(text[match[4]:match[5]])
		text = text[match[1]:]
		body := s.text(root + "arcadeapclist/" + path)
		if strings.TrimSpace(body) == "" {
			continue
		}
		grows := map[int]bool{}
		for _, group := range s.blocks(body, "Arcade ApcInfo", true) {
			grow := s.scalar(group, "growType", 0, 1<<31-1)
			if grow > 5 {
				continue
			}
			s.require(!grows[grow], "duplicate arcade grow type")
			grows[grow] = true
			s.growRows(rows, modes, job, byte(grow), group)
		}
	}
	s.require(len(jobs) > 0, "empty arcade job index")
	return rows
}

func (s *source) growRows(rows map[[3]byte]specific, modes map[byte]mode, job, grow byte, group string) {
	tags := indexPattern.FindAllStringIndex(group, -1)
	seen := map[int64]bool{}
	for i, at := range tags {
		end := len(group)
		if i+1 < len(tags) {
			end = tags[i+1][0]
		}
		tail := strings.TrimSpace(group[at[1]:end])
		tokens := strings.Fields(tail)
		if !s.require(len(tokens) > 0, "malformed arcade index") {
			return
		}
		index, err := strconv.ParseInt(tokens[0], 10, 32)
		if !s.require(err == nil && index >= 0 && !seen[index], "invalid or duplicate arcade index for grow type") {
			return
		}
		seen[index] = true
		m, ok := modes[byte(index)]
		if index > 82 || !ok {
			continue
		}
		body := tail[len(tokens[0]):]
		values := s.ints(s.block(body, "APC Index"))
		if !s.require(len(values)%2 == 0, "arcade APC index requires round and template pairs") {
			return
		}
		fixed := map[byte]uint32{}
		for j := 0; j < len(values); j += 2 {
			round, id := values[j], s.id(values[j+1])
			if !s.require(round > 0 && round <= int64(m.rounds), "fixed APC round exceeds its mode") {
				return
			}
			if fixed[byte(round)] == 0 {
				fixed[byte(round)] = id
			}
		}
		var pool []uint32
		random := s.blocks(body, "Random APC Index", false)
		if !s.require(len(random) <= 1, "duplicate arcade random pool") {
			return
		}
		if len(random) == 1 {
			pool, _ = s.ids(random[0])
		}
		rows[[3]byte{job, grow, byte(index)}] = specific{fixed: fixed, pool: pool}
	}
}
