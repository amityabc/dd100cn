package quest

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"s9a1/server/internal/catalogprep/towns"
	"s9a1/server/internal/questwire"
)

const hiddenNPCPath = "etc/hiddenNpc.etc"

type dungeonMovie = struct {
	Movie, Map   uint32
	ResourceHash string
}

type communicatorScene = struct {
	ItemHash, SceneHash string
	Arrival             questwire.QuestPoint
}

func (s *questSource) prepareObjective(q *questwire.Quest) error {
	var err error
	switch {
	case q.Kind == communicator:
		return s.prepareCommunicator(q)
	case q.Kind == question:
		err = s.prepareQuestion(q)
	case q.Kind == reachRange && q.Subtype == 0:
		err = s.prepareNPCRange(q)
	case q.Kind == cinematic:
		return s.prepareMovie(q)
	default:
		return nil
	}
	return err
}

func resourcePath(path, category string) (string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(strings.ToLower(path), category+"/") {
		path = category + "/" + path
	}
	if strings.Contains(path, ":") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("resource path leaves %s namespace", category)
	}
	for segment := range strings.SplitSeq(path, "/") {
		if anyOf(segment, "", ".", "..") {
			return "", fmt.Errorf("resource path leaves %s namespace", category)
		}
	}
	return path, nil
}

func (s *questSource) indexedPath(category string, id int64) (string, error) {
	path, err := s.indexed(category, id)
	if err != nil {
		return "", err
	}
	return resourcePath(path, category)
}

func (s *questSource) prepareMovie(q *questwire.Quest) error {
	path, err := s.indexed("cinematic", int64(q.Data[0]))
	if err != nil {
		return err
	}
	text, err := s.text(path)
	if err != nil {
		return err
	}
	movie := script{text: text}
	mapID := movie.scalar("MAP", 0)
	if movie.err != nil || len(movie.blocks("SCENARIO")) != 1 {
		return fmt.Errorf("quest movie is not a scenario")
	}
	if q.TownCinematicTarget != nil {
		if mapID != -1 {
			return fmt.Errorf("town quest movie is not a town scenario")
		}
		return nil
	}
	if mapID <= 0 || len(q.DestinationDungeons) == 0 {
		return fmt.Errorf("quest movie has no dungeon map binding")
	}
	rawHash, err := s.rawHash(path)
	if err != nil {
		return err
	}
	q.DungeonCinematicTarget = &dungeonMovie{uint32(q.Data[0]), uint32(mapID), rawHash}
	q.Hash = hash(q.Hash + "\n" + rawHash)
	return nil
}

func (s *questSource) prepareCommunicator(q *questwire.Quest) error {
	const itemHash = "89574935CFBDF3891CC9B64B95957A1ED5FDEFDEB5B9B30CBE6C762A4561A6F6"
	const townHash = "D48B37FB37ACED3769CC5AC9E9FA002EC0635B13A2A5876FBE9A2ADA087C186C"
	const mapHash = "20447CEB49BA9C8A80424DC7353DB968F4DBBC241725FCE8318D867406624727"
	const npcHash = "3AAFF571CCE8355FA78CA3942AF5855C1119027124549C2E4546258C9086A1FF"
	if len(q.Required) != 1 || !slices.Equal(q.Required[0], []uint16{12121}) || len(q.Materials) != 0 ||
		q.Parent != 0 || len(q.Children) != 0 || len(q.DestinationDungeons) != 0 || q.AwakeningAward != nil {
		return fmt.Errorf("invalid communicator quest dependencies")
	}
	for _, entry := range []struct {
		category string
		id       int64
		hash     string
	}{{"stackable", 10312154, itemHash}, {"town", 76, townHash}, {"npc", 100000198, npcHash}} {
		path, err := s.indexedPath(entry.category, entry.id)
		if err != nil {
			return err
		}
		sum, err := s.rawHash(path)
		if err != nil {
			return err
		}
		if sum != entry.hash {
			return fmt.Errorf("communicator resource identity differs: %s", path)
		}
	}
	sum, err := s.rawHash("map/Town/Airship/Airship_rear.map")
	if err != nil {
		return err
	}
	if sum != mapHash {
		return fmt.Errorf("communicator map identity differs")
	}
	hiddenHash, _, err := s.checkVisibility(100000198, npcHash, "", nil)
	if err != nil {
		return err
	}
	scene := hash(strings.Join([]string{townHash, mapHash, npcHash, hiddenHash}, "\n"))
	q.CommunicatorTarget = &communicatorScene{ItemHash: itemHash, SceneHash: scene, Arrival: questwire.QuestPoint{Town: 76, Area: 0, X: 1783, Y: 300, Direction: 5}}
	q.NpcRangeTarget = &questwire.NpcTarget{NpcId: 100000198, Town: 76, Area: 0, X: 1916, Y: 292, ResourceHash: scene}
	q.Hash = hash(strings.Join([]string{q.Hash, itemHash, scene}, "\n"))
	return nil
}

var areaRE = regexp.MustCompile("(?ims)^[ \t]*\\[area\\]\\s*(.*?)^[ \t]*\\[/area\\]")
var areaHeader = regexp.MustCompile("(?i)^\\s*(\\d+)\\s+\x60([^\x60]+\\.map)\x60(?:\\s|$)")
var areaNumber = regexp.MustCompile("^\\s*(\\d+)\\s")
var nestedArea = regexp.MustCompile("(?im)^[ \t]*\\[/?area\\]")

func selectArea(text string, area uint16) (string, error) {
	number := strconv.Itoa(int(area))
	body := ""
	for _, match := range areaRE.FindAllStringSubmatch(text, -1) {
		prefix := areaNumber.FindStringSubmatch(match[1])
		if len(prefix) == 2 && prefix[1] == number {
			if body != "" || nestedArea.MatchString(match[1]) {
				return "", fmt.Errorf("ambiguous town area %d", area)
			}
			body = match[1]
		}
	}
	if body == "" {
		return "", fmt.Errorf("missing town area %d", area)
	}
	return body, nil
}

func (s *questSource) townArea(town, area uint16) (string, string, string, error) {
	townPath, err := s.indexedPath("town", int64(town))
	if err != nil {
		return "", "", "", err
	}
	text, err := s.text(townPath)
	if err != nil {
		return "", "", "", err
	}
	sum, err := s.rawHash(townPath)
	if err != nil {
		return "", "", "", err
	}
	body, err := selectArea(text, area)
	return text, body, sum, err
}

func (s *questSource) freezeTarget(npc uint32, town, area uint16) (*questwire.NpcTarget, error) {
	_, body, sum, err := s.townArea(town, area)
	if err != nil {
		return nil, err
	}
	header := areaHeader.FindStringSubmatch(body)
	if len(header) != 3 {
		return nil, fmt.Errorf("missing town %d area %d base map", town, area)
	}
	return s.freezeMapTarget(npc, town, area, header[2], sum)
}

func (s *questSource) freezeMapTarget(npc uint32, town, area uint16, path, townHash string) (*questwire.NpcTarget, error) {
	path, err := resourcePath(path, "map")
	if err != nil {
		return nil, err
	}
	text, err := s.text(path)
	if err != nil {
		return nil, err
	}
	if len(text) > budget*4 {
		return nil, fmt.Errorf("town NPC text exceeds parser budget")
	}
	mapHash, err := s.rawHash(path)
	if err != nil {
		return nil, err
	}
	var target *questwire.NpcTarget
	for _, placement := range towns.NPCs(text) {
		if placement.Id != npc {
			continue
		}
		if target != nil {
			return nil, fmt.Errorf("ambiguous NPC %d placement in %s", npc, path)
		}
		target = &questwire.NpcTarget{NpcId: npc, Town: town, Area: area, X: placement.X, Y: placement.Y}
	}
	if target == nil {
		return nil, fmt.Errorf("missing NPC %d placement in %s", npc, path)
	}
	target.ResourceHash = hash(fmt.Sprintf("%d/%d\n%s\n%s\n%s", town, area, townHash, strings.ToLower(path), mapHash))
	return target, nil
}

var (
	hiddenRE  = regexp.MustCompile("(?is)^\\s*\\[hidden npc\\](.*?)\\[/hidden npc\\]\\s*\\[npc visibility quest sequence\\](.*?)\\[/npc visibility quest sequence\\]\\s*$")
	upgradeRE = regexp.MustCompile("(?im)^[ \t]*\\[/?upgrade\\]")
)

func (s *questSource) checkVisibility(npc uint32, expected, path string, visibilitySources map[uint16]string) (string, string, error) {
	hidden, err := s.text(hiddenNPCPath)
	if err != nil {
		return "", "", err
	}
	hiddenHash, err := s.rawHash(hiddenNPCPath)
	if err != nil {
		return "", "", err
	}
	if path == "" {
		path, err = s.indexedPath("npc", int64(npc))
		if err != nil {
			return "", "", err
		}
	}
	npcText, err := s.text(path)
	if err != nil {
		return "", "", err
	}
	npcHash, err := s.rawHash(path)
	if err != nil {
		return "", "", err
	}
	match := hiddenRE.FindStringSubmatch(hidden)
	if len(match) != 3 || expected != npcHash || strings.TrimSpace(npcText) == "" || upgradeRE.MatchString(npcText) {
		return "", "", fmt.Errorf("NPC %d visibility resource differs", npc)
	}
	input := script{}
	ids, sequences := input.numbers(match[1]), input.numbers(match[2])
	isHidden := visibilitySources != nil
	if input.err != nil || slices.Contains(ids, int(npc)) != isHidden ||
		slices.ContainsFunc(ids, func(id int) bool { return id <= 0 }) || len(sequences)%3 != 0 || !isHidden && slices.Contains(sequences, int(npc)) {
		return "", "", fmt.Errorf("NPC %d hidden defaults differ", npc)
	}
	for row := range slices.Chunk(sequences, 3) {
		if row[0] <= 0 || row[0] >= 30000 || row[1] <= 0 || row[1] >= 30000 || row[2] <= 0 || row[2] > 255 ||
			visibilitySources[uint16(row[0])] != "" || visibilitySources[uint16(row[1])] != "" {
			return "", "", fmt.Errorf("NPC %d visibility defaults have changed", npc)
		}
	}
	return hiddenHash, npcHash, nil
}

func (s *questSource) prepareQuestion(q *questwire.Quest) error {
	const dana = "EF82936B2E574D416BEF29DC7E77D0DB9ABC82FA7911F38D5FB40F85D8125301"
	hiddenHash, npcHash, err := s.checkVisibility(331, dana, "", nil)
	if err != nil {
		return err
	}
	target, err := s.freezeTarget(331, 22, 3)
	if err != nil {
		return err
	}
	if target.X != 1369 || target.Y != 223 {
		return fmt.Errorf("question NPC moved outside the confirmed scene")
	}
	target.ResourceHash = hash(strings.Join([]string{target.ResourceHash, npcHash, hiddenHash}, "\n"))
	q.NpcRangeTarget = target
	q.Hash = hash(q.Hash + "\n" + target.ResourceHash)
	return nil
}
