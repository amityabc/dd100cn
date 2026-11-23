package dungeons

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/opening"
)

type Opening struct {
	opening.Definition
	ExperienceMultiplier json.Number
	DisableExperience    bool
}

func (o Opening) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Job         byte
		Hash        string
		ReturnPoint opening.Point
		Dungeon     nativeDungeon
	}{o.Job, o.Hash, o.ReturnPoint, wireDungeon(o.Dungeon)})
}

var (
	tutorialJobs = []string{"swordman", "fighter", "gunner", "mage", "priest", "at gunner", "thief", "at fighter",
		"at mage", "ds swordman", "creator", "at swordman", "knight", "demonic lancer", "at priest", "gun blader"}
	tutorialFlow          = regexp.MustCompile(`(?is)\[tutorial flow\](.*?)\[/tutorial flow\]`)
	tutorialJob           = regexp.MustCompile("^\\s*`\\[([^\\]]+)\\]`")
	tutorialDungeon       = regexp.MustCompile(`(?i)\[dungeon index\]\s*(\d+)`)
	tutorialMarker        = regexp.MustCompile(`(?i)\[tutorial dungeon\]\s*1\b`)
	tutorialTown          = regexp.MustCompile(`(?i)\[town\]\s*(\d+)\s+(\d+)`)
	tutorialPoint         = regexp.MustCompile(`(?i)\[startPos\]\s*(\d+)\s+(\d+)`)
	tutorialFallbackMap   = regexp.MustCompile("`map`\\s+(\\d+)\\s+(\\d+)\\s+(\\d+)")
	tutorialFallbackStart = regexp.MustCompile(`(?i)(\[start map\]\s*)0\s+0(\s*\[/start map\])`)
)

func BuildOpenings(a Archive) ([]Opening, error) {
	s, err := newMapSource(a)
	if err != nil {
		return nil, err
	}
	flow, err := a.ReadText("etc/tutorial/tutorialflow.etc", budget)
	if err != nil {
		return nil, err
	}
	rows := make(map[byte]Opening, 16)
	add := func(job byte, id uint32, point opening.Point, source string) error {
		path := s.dungeons[int64(id)]
		if path == "" {
			return fmt.Errorf("tutorial %d has no source dungeon %d", job, id)
		}
		raw, err := a.ReadText(path, budget)
		if err != nil {
			return err
		}
		if !tutorialMarker.MatchString(raw) {
			return fmt.Errorf("dungeon %d is not a tutorial", id)
		}
		text := raw
		if job == 9 || job == 10 {
			text, err = normalizeTimegate(id, raw)
			if err != nil {
				return err
			}
		}
		index := -1
		for i, maze := range mazeBlocks(text) {
			if !strings.Contains(strings.ToLower(maze), "[quest connection]") {
				index = i
				break
			}
		}
		d, meta, err := s.parse(id, text, index, layoutOptions{})
		if err != nil {
			return err
		}
		rows[job] = Opening{Definition: opening.Definition{
			Job: job, Hash: hashText(source + "\n" + raw + "\n" + d.ResourceHash), ReturnPoint: point, Dungeon: d,
		}, ExperienceMultiplier: meta.multiplier, DisableExperience: meta.disableExperience}
		return nil
	}
	for _, row := range tutorialFlow.FindAllStringSubmatch(flow, -1) {
		text := row[1]
		name, id := tutorialJob.FindStringSubmatch(text), tutorialDungeon.FindStringSubmatch(text)
		town, point := tutorialTown.FindStringSubmatch(text), tutorialPoint.FindStringSubmatch(text)
		if name == nil || id == nil || town == nil || point == nil {
			return nil, fmt.Errorf("tutorial flow has an incomplete job, dungeon or return point")
		}
		job := slices.Index(tutorialJobs, name[1])
		if job < 0 || rows[byte(job)].Dungeon.ID != 0 {
			return nil, fmt.Errorf("unknown or duplicate tutorial job %q", name[1])
		}
		values := []struct {
			token string
			bits  int
		}{{id[1], 32}, {town[1], 8}, {town[2], 8}, {point[1], 16}, {point[2], 16}}
		parsed := make([]uint64, len(values))
		for i, v := range values {
			parsed[i], err = strconv.ParseUint(v.token, 10, v.bits)
			if err != nil {
				return nil, err
			}
		}
		if err := add(byte(job), uint32(parsed[0]), opening.Point{Town: byte(parsed[1]), Area: byte(parsed[2]), X: uint16(parsed[3]), Y: uint16(parsed[4]), Direction: 5}, text); err != nil {
			return nil, err
		}
	}
	for job, id := range map[byte]uint32{9: 10001, 10: 10002} {
		if rows[job].Dungeon.ID == 0 {
			if err := add(job, id, opening.Point{Town: 38, X: 1677, Y: 222, Direction: 5}, fmt.Sprintf("native-timegate-%d", job)); err != nil {
				return nil, err
			}
		}
	}
	out := make([]Opening, 0, len(rows))
	for i := byte(0); i < 16; i++ {
		if row, ok := rows[i]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func normalizeTimegate(id uint32, text string) (string, error) {
	expect := map[uint32]int{10001: 6, 10002: 7}[id]
	rows := tutorialFallbackMap.FindAllStringSubmatch(text, -1)
	if expect == 0 || len(rows) != expect || !tutorialFallbackStart.MatchString(text) {
		return "", fmt.Errorf("unsupported timegate empty-start layout %d", id)
	}
	for i, row := range rows {
		x, _ := strconv.Atoi(row[1])
		y, _ := strconv.Atoi(row[2])
		if x != i+1 || y != 0 {
			return "", fmt.Errorf("unexpected timegate map cell %d/%d", x, y)
		}
	}
	return tutorialFallbackStart.ReplaceAllString(text, "${1}1 0${2}"), nil
}
