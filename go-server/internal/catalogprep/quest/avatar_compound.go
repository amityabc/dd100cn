package quest

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/catalogprep/pvftext"
	"s9a1/server/internal/pvf"
)

type avatarBoxProduct struct {
	Template       gm.SpecialTemplate
	Abilities      map[byte]uint16
	Model          uint32
	ResourcePeriod int64
	AttachType     byte
}
type avatarPool struct {
	BaseRate, AdvancedRate uint32
	Rare                   []avatarBoxProduct
	Weights                []uint32
	Ordinary               []avatarBoxProduct
	Memory                 bool
}
type avatarAbsolute struct {
	Source       *gm.ItemTemplate
	Count, Grade byte
	Products     []avatarBoxProduct
}
type avatarCompoundSource struct {
	Template *gm.ItemTemplate
	Kind     byte
}
type avatarCompound struct {
	Sources                  map[uint32]avatarCompoundSource
	Inputs                   map[uint32]avatarInput
	NormalPools, MemoryPools map[byte]map[uint16]avatarPool
	Absolute                 map[byte]map[uint32]avatarAbsolute
	Unique                   map[byte]avatarUnique
}

var avatarCompoundParts = []string{"hat", "hair", "face", "coat", "pants", "shoes", "neck", "belt"}
var nativeCompoundPaths = regexp.MustCompile("(\\d+)\\s+\x60([^\x60]+)\x60")
var absoluteFields = regexp.MustCompile("^\x60\\[absolute bind cube\\]\x60\\s+(\\d+)\\s+(4|8)\\s+([12])\\s+0\\s+0$")
var absoluteGroups = regexp.MustCompile("(?s)\\[absolute bind cube\\]\\s*(\\d+)\\s*\\[absolute bind plus rare\\](.*?)\\[/absolute bind plus rare\\]\\s*\\[/absolute bind cube\\]")
var absoluteJob = regexp.MustCompile("\x60\\[([^\\]]+)\\]\x60")

type compoundResources struct {
	archive  *pvf.Archive
	items    ItemLookup
	cases    map[uint32]map[byte]uint16
	products map[uint32]*gm.SpecialProduct
}

func (r *compoundResources) product(job byte, id uint32, slot uint16, strict bool) (avatarBoxProduct, error) {
	variant, exists := r.products[id]
	if !exists {
		src, err := r.items.Source(id)
		if err == nil && src.Equipment {
			variant, err = src.SpecialGift(r.cases, 3)
			if err != nil {
				variant = nil
			}
		}
		r.products[id] = variant
	}
	if variant == nil || variant.Template.Kind != 0 || variant.Template.WornSlot != slot || variant.Template.UsableJobs&(1<<job) == 0 ||
		variant.ResourcePeriod != 0 || variant.Template.ResourceExpiration != nil || strict && variant.Template.RelativeLifetimeSeconds != 0 {
		return avatarBoxProduct{}, fmt.Errorf("compound output %d does not have a matching permanent job/part model", id)
	}
	return avatarBoxProduct{variant.Template, variant.Abilities, 3, 0, variant.AttachType}, nil
}

func loadAvatarCompound(a *pvf.Archive, items ItemLookup, cases map[uint32]map[byte]uint16, absoluteIDs []uint32) avatarCompound {
	out := avatarCompound{
		Sources: make(map[uint32]avatarCompoundSource), Inputs: make(map[uint32]avatarInput),
		NormalPools: make(map[byte]map[uint16]avatarPool), MemoryPools: make(map[byte]map[uint16]avatarPool),
		Absolute: make(map[byte]map[uint32]avatarAbsolute), Unique: make(map[byte]avatarUnique),
	}
	r := compoundResources{a, items, cases, make(map[uint32]*gm.SpecialProduct)}
	for _, id := range []uint32{21, 899, 490704046, 490709241, 10007495} {
		src, err := items.Source(id)
		if err != nil {
			if anyOf(id, uint32(490709241), uint32(10007495)) {
				continue
			}
			return out
		}
		s := readItemScript(src.Text)
		kind := byte(0)
		switch id {
		case 899:
			kind = 1
		case 490704046, 490709241, 10007495:
			kind = 5
		}
		if s.get("stackable type") != "\x60[etc]\x60 0" || kind == 5 && s.get("action type") != "\x60[bind cube]\x60 5" || s.err != nil {
			return out
		}
		t, err := src.Gift()
		if err != nil {
			return out
		}
		out.Sources[id] = avatarCompoundSource{t, kind}
	}
	normal, _ := a.ReadText("etc/compoundavatar.etc", budget)
	memory, _ := a.ReadText("etc/chn_compound/compoundavatar.etc", budget)
	absolute, _ := a.ReadText("etc/chn_absolute_bind_cube.etc", budget)
	uniqueText, _ := a.ReadText("etc/chn_compounduniqueavatar.etc", budget)
	generations := parseAbsoluteGenerations(absolute)
	absSources := loadAbsoluteSources(items, absoluteIDs)
	memorySource, memoryErr := items.Source(2683630)
	memoryScript := readItemScript(memorySource.Text)
	memoryTemplate, memoryGiftErr := memorySource.Gift()
	for job := byte(0); job < 16; job++ {
		regular, err := r.pools(job, normal, "etc/", false)
		if err != nil {
			continue
		}
		out.NormalPools[job] = regular
		if memoryErr == nil && memoryGiftErr == nil && memoryScript.get("stackable type") == "\x60[etc]\x60 0" && memoryScript.err == nil {
			if pools, err := r.pools(job, memory, "etc/chn_compound/", true); err == nil {
				out.MemoryPools[job] = pools
				out.Sources[2683630] = avatarCompoundSource{memoryTemplate, 0}
			}
		}
		absolute := make(map[uint32]avatarAbsolute)
		for id, src := range absSources {
			jobs := generations[src.generation]
			if row := jobs[jobNames[job]]; row != "" {
				if rule, err := r.absolute(job, src, row); err == nil {
					absolute[id] = rule
				}
			}
		}
		if len(absolute) > 0 {
			out.Absolute[job] = absolute
		}
		if unique, err := r.unique(job, uniqueText); err == nil {
			out.Unique[job] = unique
		}
	}
	return out
}

func (r *compoundResources) pools(job byte, root, prefix string, memory bool) (map[uint16]avatarPool, error) {
	var path string
	for _, row := range nativeCompoundPaths.FindAllStringSubmatch(root, -1) {
		id, err := strconv.ParseUint(row[1], 10, 32)
		if err != nil {
			return nil, err
		}
		if byte(id) == job+1 && id <= 16 {
			if path != "" {
				return nil, fmt.Errorf("duplicate native compound job")
			}
			path = prefix + strings.ToLower(row[2])
		}
	}
	if path == "" {
		return nil, fmt.Errorf("compound job path is absent")
	}
	text, err := r.archive.ReadText(path, budget)
	if err != nil {
		return nil, err
	}
	s := readItemScript(text)
	block := func(tag string) string {
		v, e := pvftext.Section(text, tag)
		if e != nil {
			s.fail(e.Error())
		}
		return v
	}
	if memory && !slices.Equal(s.numbers(block("material")), []uint32{1, 2683630, 1}) {
		return nil, fmt.Errorf("memory cube material contract differs")
	}
	out := make(map[uint16]avatarPool)
	base, advanced := block("rare rate"), block("upper rare rate")
	for slot, name := range avatarCompoundParts {
		values := s.numbers(block(name + " avatar"))
		if len(values) < 5 || (len(values)-1)%2 != 0 || !memory && (len(values) < 9 || len(values) > 2049 || values[0] != 3) ||
			memory && (values[0] < 1 || values[0] > 16 || int(values[0]) >= (len(values)-1)/2) {
			return nil, fmt.Errorf("native compound rows for %s are invalid", name)
		}
		ratePattern := regexp.MustCompile("\x60" + name + "\x60\\s+(\\d+)")
		rate := func(section string) uint32 {
			row := ratePattern.FindStringSubmatch(section)
			if len(row) != 2 {
				s.fail("compound part rate is absent")
				return 0
			}
			value, err := strconv.ParseUint(row[1], 10, 32)
			if err != nil {
				s.fail("compound part rate is invalid")
			}
			return uint32(value)
		}
		pool := avatarPool{BaseRate: rate(base), AdvancedRate: rate(advanced), Memory: memory}
		seen := make(map[uint32]bool)
		baseCount := int(values[0])
		for i, row := range slices.Collect(slices.Chunk(values[1:], 2)) {
			id, weight := row[0], row[1]
			if seen[id] || weight == 0 {
				return nil, fmt.Errorf("duplicate compound output or zero weight")
			}
			seen[id] = true
			p, err := r.product(job, id, uint16(slot), false)
			if err != nil {
				return nil, err
			}
			if i < baseCount {
				pool.Rare = append(pool.Rare, p)
				pool.Weights = append(pool.Weights, weight)
			} else {
				if weight != 1000 {
					return nil, fmt.Errorf("compound ordinary styles have nonuniform weight")
				}
				pool.Ordinary = append(pool.Ordinary, p)
			}
		}
		ordered := slices.Clone(pool.Weights)
		slices.Sort(ordered)
		var sum uint64
		for _, weight := range pool.Weights {
			sum += uint64(weight)
		}
		if s.err != nil || pool.BaseRate > 10000 || pool.AdvancedRate > 10000 ||
			memory && sum != 1000 || !memory && !slices.Equal(ordered, []uint32{200, 300, 500}) {
			return nil, fmt.Errorf("native compound probability policy is invalid")
		}
		out[uint16(slot)] = pool
	}
	return out, nil
}

type absoluteSource struct {
	template     *gm.ItemTemplate
	generation   uint32
	count, grade byte
}

func loadAbsoluteSources(items ItemLookup, ids []uint32) map[uint32]absoluteSource {
	out := make(map[uint32]absoluteSource)
	for _, id := range ids {
		src, err := items.Source(id)
		if err != nil {
			continue
		}
		s := readItemScript(src.Text)
		row := absoluteFields.FindStringSubmatch(s.get("action type"))
		if s.get("stackable type") != "\x60[etc]\x60 0" || s.err != nil || len(row) != 4 {
			continue
		}
		values := s.numbers(strings.Join(row[1:], " "))
		t, err := src.Gift()
		if s.err == nil && err == nil {
			out[id] = absoluteSource{t, values[0], byte(values[1]), byte(values[2])}
		}
	}
	return out
}
func parseAbsoluteGenerations(text string) map[uint32]map[string]string {
	out := make(map[uint32]map[string]string)
	invalid := make(map[uint32]bool)
	for _, group := range absoluteGroups.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseUint(group[1], 10, 32)
		if err != nil {
			continue
		}
		id := uint32(n)
		if out[id] != nil || invalid[id] {
			delete(out, id)
			invalid[id] = true
			continue
		}
		rows := make(map[string]string)
		text := group[2]
		matches := absoluteJob.FindAllStringSubmatchIndex(text, -1)
		for i, match := range matches {
			end := len(text)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			job := text[match[2]:match[3]]
			if _, dupe := rows[job]; dupe {
				rows[job] = ""
				continue
			}
			rows[job] = text[match[1]:end]
		}
		out[id] = rows
	}
	return out
}
func (r *compoundResources) absolute(job byte, source absoluteSource, data string) (avatarAbsolute, error) {
	result := avatarAbsolute{Source: source.template, Count: source.count, Grade: source.grade}
	for slot, part := range avatarCompoundParts {
		row := regexp.MustCompile("\x60" + part + "\x60\\s+(\\d+)").FindStringSubmatch(data)
		if len(row) != 2 {
			return result, fmt.Errorf("absolute compound part %s is absent", part)
		}
		id, err := strconv.ParseUint(row[1], 10, 32)
		if err != nil {
			return result, err
		}
		product, err := r.product(job, uint32(id), uint16(slot), true)
		if err != nil {
			return result, err
		}
		result.Products = append(result.Products, product)
	}
	return result, nil
}

var uniqueDigits = regexp.MustCompile("\\d+")
var uniqueSeasons = regexp.MustCompile("(?s)\\[season\\]\\s*(\\d+)(.*?)\\[/season\\]")

type uniqueReward struct {
	Template      *gm.ItemTemplate
	Count, Weight uint32
}
type uniqueProduct struct {
	Season  int
	Product avatarBoxProduct
	IsClone bool
	Targets []uint32
}
type avatarUnique struct {
	Material                    *gm.ItemTemplate
	UpgradeCount, CompoundCount uint32
	Gauge                       int
	Rates                       []uint32
	MysticFail, AvatarFail      []uniqueReward
	Products                    []uniqueProduct
}

func (r *compoundResources) unique(job byte, text string) (avatarUnique, error) {
	s := readItemScript(text)
	values := func(s *itemScript, name string) []uint32 {
		return s.numbers(strings.Join(uniqueDigits.FindAllString(s.get(name), -1), " "))
	}
	out := avatarUnique{}
	needs, rates, gauge := values(s, "need mystic count info"), values(s, "upgrade success rate"), values(s, "upgrade gauge")
	if len(needs) != 3 || slices.Contains(needs, 0) || len(rates) != 4 || len(gauge) != 1 || gauge[0] == 0 || gauge[0] > 1000 || slices.ContainsFunc(rates, func(n uint32) bool { return n > 100 }) {
		return out, fmt.Errorf("unsupported unique avatar upgrade rules")
	}
	readGift := func(id uint32) (*gm.ItemTemplate, error) {
		src, err := r.items.Source(id)
		if err != nil {
			return nil, err
		}
		if _, err = src.Weight(); err != nil {
			return nil, err
		}
		return src.Gift()
	}
	out.UpgradeCount, out.CompoundCount, out.Gauge, out.Rates = needs[1], needs[2], int(gauge[0]), rates
	var err error
	out.Material, err = readGift(needs[0])
	if err != nil {
		return out, err
	}
	seen := make(map[[2]uint32]bool)
	for _, row := range uniqueSeasons.FindAllStringSubmatch(text, -1) {
		season, err := strconv.ParseUint(row[1], 10, 31)
		if err != nil {
			return out, err
		}
		seasonScript := readItemScript(row[2])
		targets := distinct(values(seasonScript, "target avatar material"))
		slices.Sort(targets)
		for slot, part := range avatarCompoundParts {
			entries := values(seasonScript, part+" avatar")
			if len(entries) != 32 {
				return out, fmt.Errorf("incomplete unique avatar season job table")
			}
			for clone, id := range entries[int(job)*2 : int(job)*2+2] {
				key := [2]uint32{uint32(season), id}
				if seen[key] {
					return out, fmt.Errorf("duplicate unique avatar product")
				}
				seen[key] = true
				p, err := r.product(job, id, uint16(slot), true)
				if err != nil {
					return out, err
				}
				src, err := r.items.Source(id)
				if err != nil {
					return out, err
				}
				s := readItemScript(src.Text)
				if s.get("grade") != "4" || s.err != nil {
					return out, fmt.Errorf("unexpected unique avatar product grade")
				}
				only := targets
				if clone != 0 {
					only = []uint32{}
				}
				out.Products = append(out.Products, uniqueProduct{int(season), p, clone != 0, only})
			}
		}
		if seasonScript.err != nil {
			return out, seasonScript.err
		}
	}
	if len(out.Products) == 0 {
		return out, fmt.Errorf("unique avatar has no qualified season")
	}
	for _, r := range []struct {
		name string
		dest *[]uniqueReward
	}{
		{"mystic material fail reward", &out.MysticFail}, {"avatar material fail reward", &out.AvatarFail},
	} {
		args := values(s, r.name)
		if len(args) == 0 || len(args)%3 != 0 || len(args) > 4096 {
			return out, fmt.Errorf("incomplete unique avatar reward rules")
		}
		var sum uint64
		for row := range slices.Chunk(args, 3) {
			if row[1] == 0 || row[2] == 0 {
				return out, fmt.Errorf("zero quantity or weight in unique avatar reward")
			}
			t, err := readGift(row[0])
			if err != nil {
				return out, err
			}
			*r.dest = append(*r.dest, uniqueReward{t, row[1], row[2]})
			sum += uint64(row[2])
		}
		if sum > math.MaxInt32 {
			return out, fmt.Errorf("unique avatar weights overflow")
		}
	}
	return out, s.err
}
