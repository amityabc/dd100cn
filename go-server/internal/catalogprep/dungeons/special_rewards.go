package dungeons

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"maps"
	"regexp"
	"slices"
	"strings"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/catalogprep/pvftext"
)

type SpecialRewardData struct {
	Policy      string
	Counter     uint32
	Limit       byte
	Hash        string
	Rows        map[byte][]specialChoice
	Containers  map[uint32]specialContainer
	Templates   map[uint32]json.RawMessage
	SmartPools  map[int][]uint32
	Definitions map[byte]string
}

type specialChoice struct{ Id, Weight, Count uint32 }
type specialContainer struct {
	Id     uint32
	Smart  bool
	Groups [][]specialChoice
}
type smartPreference struct{ weapon, armor string }
type smartEquipment struct {
	path string
	tmpl *commerce.ItemTemplate
}

var specialDungeonPolicies = map[uint32]string{
	100000002: "LocalGhentResourceSpecial/v1", 100000176: "LocalMineOfAbyssResourceSpecial/v1",
	100000177: "LocalMemoryLandResourceSpecial/v1", 100000178: "LocalCryptOfPainResourceSpecial/v1", 100000179: "LocalDarkTempleResourceSpecial/v1",
}
var smartKinds = map[uint32]string{100000002: "PLACE_OF_GENT", 100000177: "LAND_OF_MEMORY", 100000176: "MINE_OF_ABYSS"}

func BuildSpecialRewards(a Archive, defs []Definition) (map[uint32]SpecialRewardData, error) {
	item, err := commerce.NewItems(a)
	if err != nil {
		return nil, err
	}
	source, err := a.ReadText(specialRewardPath, budget)
	if err != nil {
		return nil, err
	}
	blocks, err := specialNativeRows(source)
	if err != nil {
		return nil, err
	}
	specialHash, err := rawHash(a, specialRewardPath)
	if err != nil {
		return nil, err
	}
	out := map[uint32]SpecialRewardData{}
	for _, id := range ordinaryFarming {
		z, err := specialPreparedDungeon(a, item, id, defs, source, specialHash, blocks)
		if err != nil {
			return nil, fmt.Errorf("special rewards %d: %w", id, err)
		}
		if z != nil {
			out[id] = *z
		}
	}
	return out, nil
}

func DailySpecialLimits(values map[uint32]SpecialRewardData) map[uint32]byte {
	out := map[uint32]byte{}
	for id, z := range values {
		if z.Counter == id {
			out[id] = z.Limit
		}
	}
	return out
}

func specialPreparedDungeon(a Archive, items *commerce.Items, id uint32, defs []Definition, source, specialHash string, blocks map[uint32][]int) (*SpecialRewardData, error) {
	prepared := map[byte]Definition{}
	for _, d := range defs {
		if d.ID == id {
			if prepared[d.MazeIndex].ID != 0 {
				return nil, fmt.Errorf("duplicate source maze")
			}
			prepared[d.MazeIndex] = d
		}
	}
	if len(prepared) == 0 {
		return nil, nil
	}
	counter := id
	if id == 100000180 || id == 100000181 {
		counter = 100000179
	}
	vals := blocks[counter]
	aliases := 0
	if counter == 100000179 {
		aliases = 2
	}
	if len(vals) < 4+aliases || vals[1] != aliases || aliases == 2 && (vals[2] != 100000180 || vals[3] != 100000181) ||
		vals[2+aliases] < 1 || vals[2+aliases] > 32 || vals[3+aliases] != 0 {
		return nil, fmt.Errorf("unsupported farming special aliases, daily limit or cycle")
	}
	digest := sha256.New()
	policy := specialDungeonPolicies[counter]
	for _, v := range []string{policy, specialRewardPath, specialHash, source} {
		addLengthText(digest, v)
	}
	data := &SpecialRewardData{Policy: policy, Counter: counter, Limit: byte(vals[2+aliases]), Rows: map[byte][]specialChoice{},
		Containers: map[uint32]specialContainer{}, Templates: map[uint32]json.RawMessage{}, SmartPools: map[int][]uint32{}, Definitions: map[byte]string{}}
	var order []byte
	for i := 4 + aliases; i < len(vals); {
		if len(vals)-i < 6 {
			return nil, fmt.Errorf("farming source row truncated")
		}
		container, kind, level, weight, notice, count := vals[i], vals[i+1], vals[i+2], vals[i+3], vals[i+4], vals[i+5]
		i += 6
		if container <= 0 || kind < 0 || kind > 5 || level < 0 || level > 4 || weight <= 0 || notice < 0 || notice > 2 || count != 1 {
			return nil, fmt.Errorf("farming special reward row changed its supported grammar")
		}
		if kind == 1 {
			if i == len(vals) || vals[i] != 0 {
				return nil, fmt.Errorf("overlaid farming party kind unsupported")
			}
			i++
		}
		if kind != 0 {
			continue
		}
		diff := byte(level)
		for _, d := range prepared {
			if !slices.Contains(d.meta.selectable, diff) {
				return nil, fmt.Errorf("special reward difficulty %d not admitted by its guide", diff)
			}
		}
		if data.Rows[diff] == nil {
			order = append(order, diff)
		}
		data.Rows[diff] = append(data.Rows[diff], specialChoice{uint32(container), uint32(weight), 1})
	}
	for _, d := range prepared {
		for _, diff := range d.meta.selectable {
			if len(data.Rows[diff]) == 0 {
				return nil, fmt.Errorf("special rewards absent for native difficulty %d", diff)
			}
		}
	}
	reader := specialRewardReader{a: a, items: items, digest: digest, data: data, templates: map[uint32]smartEquipment{}}
	smart := false
	for _, diff := range order {
		for _, row := range data.Rows[diff] {
			if _, ok := data.Containers[row.Id]; ok {
				continue
			}
			c, err := reader.container(row.Id, counter, smartKinds[counter])
			if err != nil {
				return nil, err
			}
			data.Containers[c.Id] = c
			smart = smart || c.Smart
		}
	}
	if smart {
		pools, err := reader.smartPools(smartKinds[counter])
		if err != nil {
			return nil, err
		}
		data.SmartPools = pools
	}
	for _, maze := range slices.Sorted(maps.Keys(prepared)) {
		value := prepared[maze].ResourceHash
		data.Definitions[maze] = value
		addLengthText(digest, value)
	}
	data.Hash = fmt.Sprintf("%X", digest.Sum(nil))
	return data, nil
}

func specialNativeRows(source string) (map[uint32][]int, error) {
	if len(source) > budget {
		return nil, fmt.Errorf("PVF special rewards exceed preparation budget")
	}
	typed := map[uint32]bool{100000002: true, 100000007: true, 100000176: true, 100000177: true, 100000178: true,
		100000179: true, 100000180: true, 100000181: true, 100000213: true, 100002669: true}
	add, err := pvftext.Section(source, "dungeon special reward add")
	if err != nil {
		return nil, err
	}
	blocks := map[uint32][]int{}
	for _, body := range pvftext.Blocks(add, "dungeon") {
		v, err := specialHeader(body, 32, 127)
		if err != nil {
			return nil, err
		}
		id := uint32(v[0])
		if !typed[id] {
			continue
		}
		if err := validateSpecialColumns(v[4+v[1]:]); err != nil {
			return nil, err
		}
		if _, own := specialDungeonPolicies[id]; own {
			if blocks[id] != nil {
				return nil, fmt.Errorf("repeated farming special reward %d", id)
			}
			blocks[id] = v
		}
	}
	for _, body := range pvftext.Blocks(source, "dungeon account") {
		v, err := specialHeader(body, 256, 255)
		if err != nil {
			return nil, err
		}
		if err := validateSpecialColumns(v[4+v[1]:]); err != nil {
			return nil, err
		}
	}
	adventure, err := pvftext.Numbers(source, "adventure level add reward")
	if err != nil || len(adventure)%5 != 0 {
		return nil, fmt.Errorf("PVF adventure-level reward rows truncated")
	}
	for i := 0; i < len(adventure); i += 5 {
		v := adventure[i : i+5]
		if v[0] <= 0 || v[1] < 0 || v[2] < v[1] || v[3] <= 0 || v[4] <= 0 {
			return nil, fmt.Errorf("PVF adventure-level reward row has invalid value")
		}
	}
	return blocks, nil
}

func specialHeader(body string, maxAliases, maxCycle int) ([]int, error) {
	v, err := pvftext.Integers(body)
	if err != nil || len(v) < 4 || v[0] <= 0 || v[1] < 0 || v[1] > maxAliases || len(v) < v[1]+4 ||
		v[v[1]+2] < 1 || v[v[1]+2] > 32 || v[v[1]+3] < 0 || v[v[1]+3] > maxCycle {
		return nil, fmt.Errorf("PVF special reward header malformed")
	}
	seen := map[int]bool{}
	for _, id := range v[2 : 2+v[1]] {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("PVF special reward aliases repeated or empty")
		}
		seen[id] = true
	}
	return v, nil
}

func validateSpecialColumns(values []int) error {
	for len(values) > 0 {
		if len(values) < 6 || values[0] <= 0 || values[1] < 0 || values[1] > 5 || values[2] < -1 || values[2] > 4 ||
			values[3] < 0 || values[4] < 0 || values[5] < 0 {
			return fmt.Errorf("PVF special reward raw columns are invalid or truncated")
		}
		kind := values[1]
		values = values[6:]
		if kind == 1 {
			if len(values) == 0 || values[0] < 0 || values[0] > 255 {
				return fmt.Errorf("PVF special reward party kind is absent")
			}
			values = values[1:]
		}
	}
	return nil
}

type specialRewardReader struct {
	a         Archive
	items     *commerce.Items
	digest    hash.Hash
	data      *SpecialRewardData
	templates map[uint32]smartEquipment
}

func (s *specialRewardReader) read(path string) (string, error) {
	text, err := s.a.ReadText(path, budget)
	if err != nil {
		return "", err
	}
	sum, err := rawHash(s.a, path)
	if err != nil {
		return "", err
	}
	for _, v := range []string{path, sum, text} {
		addLengthText(s.digest, v)
	}
	return text, nil
}

func (s *specialRewardReader) readItem(src commerce.ItemSource) {
	for _, v := range []string{src.Path, src.Hash, src.Text} {
		addLengthText(s.digest, v)
	}
}

func (s *specialRewardReader) template(src commerce.ItemSource) (smartEquipment, error) {
	id := src.Id
	if t, ok := s.templates[id]; ok {
		return t, nil
	}
	s.readItem(src)
	random, err := pvftext.Section(src.Text, "random option")
	if err != nil || random != "" && random != "0" {
		return smartEquipment{}, fmt.Errorf("special template %d requires random detail", id)
	}
	t, err := src.Gift()
	if err != nil || t == nil {
		return smartEquipment{}, fmt.Errorf("special template %d (%s) unsupported: %v", id, src.Path, err)
	}
	if t.ResourceExpiration != nil || t.RelativeLifetimeSeconds != 0 || t.DailyDeletion || t.WeeklyDeletionDay != nil ||
		t.AccountCommonInventory || t.FateCollection {
		return smartEquipment{}, fmt.Errorf("special template %d has unsupported item lifecycle", id)
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return smartEquipment{}, err
	}
	s.data.Templates[id] = raw
	row := smartEquipment{src.Path, t}
	s.templates[id] = row
	return row, nil
}

func (s *specialRewardReader) container(id, counter uint32, smartType string) (specialContainer, error) {
	src, err := s.items.Source(id)
	if err != nil {
		return specialContainer{}, err
	}
	s.readItem(src)
	p := &input{}
	kind := p.field(src.Text, "stackable type", true)
	data := specialContainer{Id: id, Smart: kind == "`[smart drop]` 0", Groups: [][]specialChoice{}}
	if !p.require(!src.Equipment && (data.Smart && smartType != "" &&
		p.field(src.Text, "smart drop dungeon type", true) == "`"+smartType+"`" || !data.Smart && kind == "`[booster]` 0"),
		"special container has unsupported source type") {
		return data, p.err
	}
	body := p.field(src.Text, "booster info", true)
	groups := pvftext.Blocks(body, "etc")
	if !p.require(len(groups) > 0 && len(groups) <= 15 && strings.TrimSpace(rewardETCBlock.ReplaceAllString(body, "")) == "",
		"special material booster has an unsupported group") {
		return data, p.err
	}
	for _, group := range groups {
		vals, err := pvftext.Integers(group)
		if err != nil || len(vals) < 4 || len(vals) > 769 || vals[0] != 1 || (len(vals)-1)%3 != 0 {
			return data, fmt.Errorf("special material booster requires one bounded draw per group")
		}
		pool := []specialChoice{}
		for i := 1; i < len(vals); i += 3 {
			id, weight, count := vals[i], vals[i+1], vals[i+2]
			if id <= 0 || weight <= 0 || count < 1 || count > 1000000 {
				return data, fmt.Errorf("special booster quantity or weight invalid")
			}
			src, err := s.items.Source(uint32(id))
			if err != nil {
				return data, err
			}
			if src.Equipment && (counter == 100000002 || count != 1) {
				return data, fmt.Errorf("special equipment booster must grant one admitted farming item")
			}
			t, err := s.template(src)
			if err != nil {
				return data, err
			}
			pool = append(pool, specialChoice{t.tmpl.TemplateId, uint32(weight), uint32(count)})
		}
		data.Groups = append(data.Groups, pool)
	}
	return data, p.err
}

var (
	smartJobs = []string{"SWORDMAN", "FIGHTER", "GUNNER", "MAGE", "PRIEST", "AT GUNNER", "THIEF", "AT FIGHTER", "AT MAGE",
		"DEMONIC SWORDMAN", "CREATOR MAGE", "AT SWORDMAN", "KNIGHT", "DEMONIC LANCER", "AT PRIEST", "GUN BLADER"}
	smartProfile       = regexp.MustCompile(`(?ms)^\[([A-Z ]+)\]\s*\[([A-Z ]+)\]\s*\[ITEM\](.*?)^\[/ITEM\]`)
	smartWeapon        = regexp.MustCompile(`(?m)^\[WEAPON\]\s*\[([A-Z ]+)\]`)
	smartArmor         = regexp.MustCompile(`(?m)^\[ARMOR\]\s*\[([A-Z ]+)\]`)
	smartWeaponAliases = map[string]string{"shortsword": "ssword", "lightsword": "beamsword", "handcannon": "hcannon", "blade": "katana",
		"blunt": "club", "heavysword": "hsword", "boxingglove": "boxglove", "battleaxe": "axe"}
	smartArmorAliases = map[string]string{"light": "larmor", "heavy": "harmor"}
)

func smartFamily(value string, aliases map[string]string) string {
	tag := strings.ToLower(strings.ReplaceAll(value, " ", ""))
	if alias := aliases[tag]; alias != "" {
		return alias
	}
	return tag
}

func (s *specialRewardReader) smartPools(kind string) (map[int][]uint32, error) {
	table, err := s.read("etc/smartdroptable.etc")
	if err != nil {
		return nil, err
	}
	p := &input{}
	body := ""
	for _, row := range pvftext.Blocks(table, "table") {
		if slices.Contains(strings.Split(p.field(row, "dungeon type", true), "`"), kind) {
			if body != "" {
				return nil, fmt.Errorf("duplicate farming smart dungeon type")
			}
			body = row
		}
	}
	if body == "" || p.err != nil {
		return nil, fmt.Errorf("missing farming smart dungeon type %s", kind)
	}
	smart, err := s.smartGear(p.field(body, "smart drop item", true))
	if err != nil {
		return nil, err
	}
	common, err := s.smartGear(p.field(body, "drop item", true))
	if err != nil {
		return nil, err
	}
	roles, err := s.read("etc/smartdropjob.etc")
	if err != nil {
		return nil, err
	}
	prefs := map[[2]int]smartPreference{}
	grow := map[int]int{}
	for _, row := range smartProfile.FindAllStringSubmatch(roles, -1) {
		job := slices.Index(smartJobs, row[1])
		weapon, armor := smartWeapon.FindStringSubmatch(row[3]), smartArmor.FindStringSubmatch(row[3])
		if job < 0 || weapon == nil || armor == nil {
			return nil, fmt.Errorf("unknown or incomplete farming smart job preference")
		}
		grow[job]++
		if grow[job] > 255 {
			return nil, fmt.Errorf("farming smart grow exceeds native width")
		}
		prefs[[2]int{job, grow[job]}] = smartPreference{smartFamily(weapon[1], smartWeaponAliases), smartFamily(armor[1], smartArmorAliases)}
	}
	if len(prefs) == 0 {
		return nil, fmt.Errorf("missing farming smart preferences")
	}
	pools := map[int][]uint32{}
	for job := 0; job < 16; job++ {
		for level := 0; level < 16; level++ {
			preference, exists := prefs[[2]int{job, level}]
			output := []uint32{}
			for _, row := range smart {
				t := row.tmpl
				path := strings.ToLower(row.path)
				if t.UsableJobs&(1<<job) != 0 && (!exists ||
					t.Kind == 0 && strings.Contains(path, "/weapon/"+preference.weapon+"/") ||
					t.Kind == 1 && strings.Contains(path, "/"+preference.armor+"/")) {
					output = append(output, t.TemplateId)
				}
			}
			for _, r := range common {
				output = append(output, r.tmpl.TemplateId)
			}
			slices.Sort(output)
			output = slices.Compact(output)
			if exists && len(output) == len(common) {
				return nil, fmt.Errorf("smart job %d grow %d has no matching weapon or armor", job, level)
			}
			pools[job*16+level] = output
		}
	}
	return pools, p.err
}

func (s *specialRewardReader) smartGear(body string) ([]smartEquipment, error) {
	ids, err := pvftext.Integers(body)
	if err != nil || len(ids) == 0 || len(ids) > 512 {
		return nil, fmt.Errorf("invalid farming smart gear pool")
	}
	out := []smartEquipment{}
	seen := map[int]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("invalid or repeated farming smart gear")
		}
		seen[id] = true
		r, err := s.items.Source(uint32(id))
		if err != nil {
			return nil, err
		}
		t, err := s.template(r)
		if err != nil {
			return nil, err
		}
		if !r.Equipment || t.tmpl.Kind > 1 {
			return nil, fmt.Errorf("farming smart pool template %d is not equipment", id)
		}
		out = append(out, t)
	}
	return out, nil
}
