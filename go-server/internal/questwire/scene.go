package questwire

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/storage"
)

// DungeonContext is constructed from the admitted run, never from a quest request.
type DungeonContext struct {
	Lease                  storage.Lease
	RunID, Hash            string
	Character              uint16
	Dungeon, Map           uint32
	Maze, X, Y, Difficulty byte
	Maps                   []uint32
	Connections            []uint16
}

func (d *DungeonContext) living(c storage.Character) bool {
	var p struct{ Progression struct{ Hp uint32 } }
	return d != nil && d.Character == c.ID && guidKey(d.RunID) != "" && d.Dungeon != 0 && hash(d.Hash) && json.Unmarshal(c.Gameplay, &p) == nil && p.Progression.Hp != 0
}

func (catalog *Catalog) dungeonAcceptance(c storage.Character, p player, in storage.Inventory, q *Quest, receipts map[string]json.RawMessage, d *DungeonContext) bool {
	if !catalog.dungeonObjective(q, d) {
		return false
	}
	if q.DungeonCinematicTarget != nil && q.Parent == 0 {
		return true
	}
	if q.Kind == questClear && validActive(q, p.hist.Active[q.Id]) {
		return true
	}
	parent := catalog.ordinary[q.Parent]
	if parent == nil || parent.Kind != questClear || !validActive(parent, p.hist.Active[parent.Id]) || !catalog.dungeonObjective(parent, d) {
		return false
	}
	at := slices.Index(parent.Children, q.Id)
	if at < 0 {
		return false
	}
	v := projectionView(catalog, p, in, receipts)
	for _, id := range parent.Children[:at] {
		child := catalog.ordinary[id]
		run, ok := v.childReceipt(child)
		if !ok || (run == "" && !child.OutsideDungeonClaim) || (child.Id == 3817 && !movieReceipt(v, child, nil, c, active{}, true)) {
			return false
		}
	}
	return true
}

func (v view) priorRoomEvent(q *Quest, d *DungeonContext) string {
	if len(q.Data) == 0 || q.Data[0] <= 0 {
		return ""
	}
	run := guidKey(d.RunID)
	for key := range v.receipts {
		if !strings.HasPrefix(key, "dungeon-death:"+run+":") && !strings.HasPrefix(key, "dungeon-room-clear:"+run+":") {
			continue
		}
		in, result, ok := v.dungeonReceipt(key, run, d.Lease.Account)
		if !ok || !result.RoomCleared || in.Dungeon != d.Dungeon || in.MazeIndex != d.Maze || in.ResourceHash != d.Hash {
			continue
		}
		want := uint32(q.Data[0])
		if in.MapId == want || slices.Contains(in.Maps, want) || slices.Contains(result.Maps, want) {
			return fmt.Sprintf("dungeon-room-clear:%s:%d:%d:%d:%d", run, in.MazeIndex, in.RoomX, in.RoomY, want)
		}
	}
	return ""
}

func (catalog *Catalog) dungeonObjective(q *Quest, d *DungeonContext) bool {
	if d == nil || d.Dungeon == 0 || q.Operations == nil {
		return false
	}
	id := int(d.Dungeon)
	listed := slices.Contains(q.DestinationDungeons, id)
	connected := slices.Contains(d.Connections, q.Id)
	if q.AwakeningAward != nil && (q.Kind != clearMap || q.AwakeningAward.Stage < 1 || q.AwakeningAward.Stage > 2 || q.AdvancementChain != int(q.AwakeningAward.Stage)+1 || !connected) {
		return false
	}
	switch q.Kind {
	case cinematic:
		m := q.DungeonCinematicTarget
		return m != nil && listed && len(q.Materials) == 0 && hash(m.ResourceHash) && slices.Contains(d.Maps, m.Map)
	case seeking:
		if !slices.Contains([]string{"epic", "side", "mission", "achievement", "daily mission"}, q.Operations.Grade) || len(q.Materials) == 0 || len(q.DropRules) == 0 || !(len(q.DestinationDungeons) == 0 || listed) {
			return false
		}
		meets := func(min int) bool { return min >= -1 && min <= 4 && d.Difficulty <= 4 && min <= int(d.Difficulty) }
		found := len(q.DestinationRequirements) == 0
		for _, bound := range q.DestinationRequirements {
			found = found || bound.Dungeon == id && meets(bound.MinimumDifficulty)
		}
		if !found {
			return false
		}
		var source bool
		for _, row := range q.DropRules {
			source = source || (row.Dungeon == id || listed && row.Dungeon == -1) && meets(row.Difficulty)
		}
		if !source {
			return false
		}
		for _, need := range q.Materials {
			var has bool
			for _, row := range q.DropRules {
				has = has || row.Template == need.Template && (row.Dungeon == -1 || row.Dungeon == id) && meets(row.Difficulty)
			}
			for _, row := range q.Operations.AcceptItems {
				has = has || row.Template == need.Template && row.Count >= need.Count
			}
			if !has {
				return false
			}
		}
		return true
	case seekMeet:
		if !connected || len(q.Materials) == 0 || !(len(q.DestinationDungeons) == 0 || listed) {
			return false
		}
		if q.Parent == 0 {
			return q.Operations.Grade == "epic"
		}
		p := catalog.ordinary[q.Parent]
		return p != nil && p.Kind == questClear && p.Subtype == 2 && slices.Contains(p.Children, q.Id)
	case clearMap:
		return len(q.Materials) == 0 && (len(q.Data) == 1 || len(q.Data) == 2 && q.Data[1] == -1) && q.Data[0] > 0 && (len(q.DestinationDungeons) == 0 || listed) && slices.Contains(d.Maps, uint32(q.Data[0]))
	case huntEnemy, huntMonster:
		if len(q.Materials) != 0 {
			return false
		}
		stride := 4
		if q.Kind == huntEnemy {
			stride = 5
		}
		for i := 0; i+stride <= len(q.Data); i += stride {
			if q.Data[i] == -1 || q.Data[i] == id {
				return true
			}
		}
	case clearCondition:
		return len(q.Materials) == 0 && len(q.Data) > 0 && q.Data[0] == id
	case questClear:
		return listed && len(q.Materials) == 0 && len(q.Children) > 0 && (q.OrderedDungeonParent || q.DungeonNpcHandIn) &&
			(connected || slices.Contains(d.Connections, q.Children[len(q.Children)-1]))
	}
	return false
}

func guidKey(s string) string {
	if len(s) != 32 && (len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-') {
		return ""
	}
	key := strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if len(key) != 32 || key == strings.Repeat("0", 32) {
		return ""
	}
	if _, err := hex.DecodeString(key); err != nil {
		return ""
	}
	return key
}

func wireGUID(s string) string {
	k := guidKey(s)
	if k == "" {
		return ""
	}
	return k[:8] + "-" + k[8:12] + "-" + k[12:16] + "-" + k[16:20] + "-" + k[20:]
}

type cinematicFact struct {
	Owner         string
	Epoch         int64
	Character     uint16
	Run           string
	Quest         uint16
	ObjectiveHash string
	Dungeon       uint32
	Maze          byte
	Map           uint32
	X, Y          byte
	DungeonHash   string
	Movie         uint32
	MovieHash     string
}

const cinematicResult = "native-movie-end-reported:v1"

func movieKey(q *Quest, run string) string {
	return fmt.Sprintf("dungeon-cinematic:%d:%s:%d", q.DungeonCinematicTarget.Movie, guidKey(run), q.Id)
}

func movieReceipt(v view, q *Quest, d *DungeonContext, c storage.Character, a active, claimed bool) bool {
	if q.DungeonCinematicTarget == nil || !hash(q.DungeonCinematicTarget.ResourceHash) {
		return false
	}
	keys := a.Receipts
	if claimed {
		keys = nil
		for k := range v.receipts {
			if strings.HasPrefix(k, fmt.Sprintf("dungeon-cinematic:%d:", q.DungeonCinematicTarget.Movie)) && strings.HasSuffix(k, fmt.Sprintf(":%d", q.Id)) {
				keys = append(keys, k)
			}
		}
	} else if !validActive(q, a) || a.Remaining[0] != 0 {
		return false
	}
	for _, k := range keys {
		var saved receipt
		var f cinematicFact
		if json.Unmarshal(v.receipts[k], &saved) != nil || saved.Key != k || saved.Revision <= 0 || saved.Revision > v.in.Revision || saved.Result != cinematicResult ||
			json.Unmarshal([]byte(saved.Input), &f) != nil || guidKey(f.Owner) == "" || f.Epoch <= 0 || guidKey(f.Run) == "" || f.Character != c.ID || f.Quest != q.Id || f.ObjectiveHash != q.Hash ||
			f.Movie != q.DungeonCinematicTarget.Movie || f.MovieHash != q.DungeonCinematicTarget.ResourceHash || f.Map != q.DungeonCinematicTarget.Map || !slices.Contains(q.DestinationDungeons, int(f.Dungeon)) || !hash(f.DungeonHash) || k != movieKey(q, f.Run) ||
			(d != nil && (guidKey(f.Run) != guidKey(d.RunID) || f.DungeonHash != d.Hash || f.Maze != d.Maze)) {
			return false
		}
	}
	return len(keys) != 0
}

type dungeonFact struct {
	Source, Version, Account, OwnerGuid, RunId, ResourceHash string
	Epoch                                                    int64
	Character, Opcode, Actor                                 uint16
	Dungeon, Id, MapId, Code                                 uint32
	MazeIndex, RoomX, RoomY, X, Y, Class                     byte
	Maps                                                     []uint32
	Body                                                     []byte
	ScriptTerminal                                           *struct{ QuestId uint16 }
	Spawns                                                   []struct{ BlocksRoom bool }
}

type dungeonResult struct {
	RoomCleared, DungeonCleared, QuestChanged bool
	Actor, ExperienceAward                    uint32
	Maps                                      []uint32
}

func (v view) dungeonReceipt(key, run, account string) (dungeonFact, dungeonResult, bool) {
	var r receipt
	var in dungeonFact
	var out dungeonResult
	if json.Unmarshal(v.receipts[key], &r) != nil || r.Key != key || r.Revision <= 0 || r.Revision > v.in.Revision ||
		json.Unmarshal([]byte(r.Input), &in) != nil || guidKey(in.RunId) != run || guidKey(in.OwnerGuid) == "" || in.Epoch <= 0 || in.Character != v.in.CharacterID || in.MapId == 0 ||
		!hash(in.ResourceHash) || in.Account != account {
		return in, out, false
	}
	if in.Source == "ServerGeneratedEmptyRoom" && r.Result == "room-cleared:no-blocking-spawns" && in.Id != 0 && in.Spawns != nil && !slices.ContainsFunc(in.Spawns, func(row struct{ BlocksRoom bool }) bool { return row.BlocksRoom }) {
		in.Dungeon, in.RoomX, in.RoomY = in.Id, in.X, in.Y
		out.RoomCleared = true
		return in, out, true
	}
	if in.Source != "ClientDelegated" || in.Class != 1 || in.Opcode != 39 || in.Dungeon == 0 || json.Unmarshal([]byte(r.Result), &out) != nil {
		return in, out, false
	}
	return in, out, true
}

func (v view) roomProof(q *Quest, a active, account string, doneRun *DungeonContext, script bool) string {
	if len(q.Data) == 0 || q.Data[0] <= 0 {
		return ""
	}
	for _, key := range a.Receipts {
		parts := strings.Split(key, ":")
		if len(parts) != 6 || parts[0] != "dungeon-room-clear" || guidKey(parts[1]) != parts[1] || parts[5] != strconv.Itoa(q.Data[0]) ||
			(doneRun != nil && guidKey(doneRun.RunID) != parts[1]) {
			continue
		}
		var cells [3]byte
		ok := true
		for i, s := range parts[2:5] {
			n, err := strconv.ParseUint(s, 10, 8)
			ok = ok && err == nil && strconv.FormatUint(n, 10) == s
			cells[i] = byte(n)
		}
		if !ok {
			continue
		}
		run := parts[1]
		match := func(k string) bool {
			in, result, ok := v.dungeonReceipt(k, run, account)
			return ok && result.RoomCleared && in.MazeIndex == cells[0] && in.RoomX == cells[1] && in.RoomY == cells[2] && (len(q.DestinationDungeons) == 0 || slices.Contains(q.DestinationDungeons, int(in.Dungeon))) &&
				(doneRun == nil || in.Dungeon == doneRun.Dungeon && in.ResourceHash == doneRun.Hash) && (!script || in.ScriptTerminal != nil && result.DungeonCleared) &&
				(slices.Contains(in.Maps, uint32(q.Data[0])) || slices.Contains(result.Maps, uint32(q.Data[0])) || in.MapId == uint32(q.Data[0]))
		}
		room := strings.Join(parts[:5], ":")
		if match(room) {
			return wireGUID(run)
		}
		prefix := "dungeon-death:" + strings.Join(parts[1:5], ":") + ":"
		for k := range v.receipts {
			if strings.HasPrefix(k, prefix) && match(k) {
				return wireGUID(run)
			}
		}
	}
	return ""
}

func (v view) noireProof(q *Quest, a active, lease storage.Lease) string {
	for _, key := range a.Receipts {
		parts := strings.Split(key, ":")
		if len(parts) != 6 || parts[0] != "dungeon-death" || guidKey(parts[1]) != parts[1] {
			continue
		}
		in, out, ok := v.dungeonReceipt(key, parts[1], lease.Account)
		if ok && in.ScriptTerminal != nil && in.ScriptTerminal.QuestId == q.Id && in.MazeIndex == 2 && in.RoomX == 5 && in.RoomY == 0 && in.MapId == 70168 && out.RoomCleared && out.DungeonCleared {
			return wireGUID(parts[1])
		}
	}
	return ""
}

func (v view) passiveProof(q *Quest, a active, lease storage.Lease, d *DungeonContext, onlyFirst bool) string {
	if len(q.Data)%5 != 0 || len(q.Data) == 0 || q.Kind != huntEnemy {
		return ""
	}
	var completed string
	for at := 0; at+4 < len(q.Data); at += 5 {
		if onlyFirst && at != 0 {
			break
		}
		kind := q.Data[at+3]
		if kind != 3 && !(onlyFirst && d != nil && kind == 1) {
			continue
		}
		if a.Remaining[at/5] != 0 {
			return ""
		}
		need := q.Data[at+4]
		if d != nil {
			need = 1
		}
		facts := 0
		for _, key := range a.Receipts {
			parts := strings.Split(key, ":")
			if (len(parts) != 6 && len(parts) != 7) || parts[0] != "dungeon-passive-death" && !(kind == 1 && parts[0] == "dungeon-death") || guidKey(parts[1]) != parts[1] || d != nil && guidKey(d.RunID) != parts[1] {
				continue
			}
			in, result, ok := v.dungeonReceipt(key, parts[1], lease.Account)
			if !ok || q.Data[at+2] != int(in.Code) || q.Data[at] != -1 && int(in.Dungeon) != q.Data[at] || d != nil && (in.Dungeon != d.Dungeon || in.ResourceHash != d.Hash) ||
				fmt.Sprintf("%d:%d:%d", in.MazeIndex, in.RoomX, in.RoomY) != strings.Join(parts[2:5], ":") {
				continue
			}
			if kind == 3 {
				if len(in.Body) != 74 || in.Body[30] != 0 || little.Uint32(in.Body) != in.Code || little.Uint16(in.Body[4:]) != 65535 || slices.ContainsFunc(in.Body[6:30], func(b byte) bool { return b != 0 }) ||
					strconv.FormatUint(uint64(in.Code), 10) != parts[5] || result.Actor != in.Code || result.ExperienceAward != 0 || !result.QuestChanged {
					continue
				}
				if d == nil && !slices.Contains(q.DestinationDungeons, int(in.Dungeon)) {
					continue
				}
			} else if result.Actor != uint32(in.Actor) || strconv.FormatUint(uint64(in.Actor), 10) != parts[5] {
				continue
			}
			if completed != "" && completed != parts[1] {
				return ""
			}
			completed = parts[1]
			facts++
		}
		if facts < need {
			return ""
		}
	}
	return wireGUID(completed)
}

func (v view) dungeonCompleted(q *Quest, d *DungeonContext, lease storage.Lease, c storage.Character) bool {
	if !v.catalog.dungeonObjective(q, d) {
		return false
	}
	a := v.p.hist.Active[q.Id]
	if q.Id == 3275 {
		left, e := v.remaining(q, map[uint16]bool{})
		return e == nil && left == 0 && v.noireProof(q, a, lease) == wireGUID(d.RunID)
	}
	if q.DungeonCinematicTarget != nil {
		return movieReceipt(v, q, d, c, a, false)
	}
	remaining, err := v.remaining(q, map[uint16]bool{})
	if err != nil || remaining != 0 {
		return false
	}
	switch q.Kind {
	case seeking, seekMeet:
		return true
	case clearMap:
		return v.roomProof(q, a, lease.Account, d, false) != ""
	case huntEnemy:
		for i := 3; i < len(q.Data); i += 5 {
			if q.Data[i] == 3 && v.passiveProof(q, a, lease, d, false) == "" {
				return false
			}
		}
	}
	run := guidKey(d.RunID)
	return slices.ContainsFunc(a.Receipts, func(key string) bool {
		return key == "dungeon-clear:"+run || strings.HasPrefix(key, "dungeon-death:"+run+":") || strings.HasPrefix(key, "dungeon-room-clear:"+run+":") || strings.HasPrefix(key, "dungeon-passive-death:"+run+":")
	})
}

func (v view) orderedTerminal(q *Quest, d *DungeonContext, lease storage.Lease, c storage.Character) (*Quest, string, bool) {
	if len(q.Children) == 0 || !validActive(q, v.p.hist.Active[q.Id]) || (d == nil && !q.OrderedDungeonParent) || (d != nil && !v.catalog.dungeonObjective(q, d)) {
		return nil, "", false
	}
	for _, id := range q.Children[:len(q.Children)-1] {
		child := v.catalog.ordinary[id]
		run, ok := v.childReceipt(child)
		if !ok || (run == "" && !child.OutsideDungeonClaim) || (child.Id == 3817 && !movieReceipt(v, child, nil, c, active{}, true)) {
			return nil, "", false
		}
	}
	last := v.catalog.ordinary[q.Children[len(q.Children)-1]]
	if last == nil || (d != nil && !v.catalog.dungeonObjective(last, d)) {
		return nil, "", false
	}
	if v.p.hist.Cleared[last.Id] != 0 {
		run, ok := v.childReceipt(last)
		return nil, wireGUID(run), ok && run != "" && (d == nil || guidKey(d.RunID) == run)
	}
	if d != nil {
		return last, wireGUID(d.RunID), v.dungeonCompleted(last, d, lease, c)
	}
	a := v.p.hist.Active[last.Id]
	if !validActive(last, a) || slices.ContainsFunc(a.Remaining, func(n uint32) bool { return n != 0 }) {
		return nil, "", false
	}
	run := v.roomProof(last, a, lease.Account, nil, false)
	return last, run, run != ""
}
