package commerce

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

var giftJobs = []string{"swordman", "fighter", "gunner", "mage", "priest", "at gunner", "thief", "at fighter", "at mage", "demonic swordman", "creator mage", "at swordman", "knight", "demonic lancer", "at priest", "gun blader"}
var giftConditionalJob = regexp.MustCompile("^`\\[([^\\]]+)\\]`\\s+")
var giftSignedIntegers = regexp.MustCompile(`^-?[0-9]+(\s+-?[0-9]+)*$`)

func parseGiftPools(fields []gm.Field, resolve giftResolve, special giftSpecialResolve) ([]storage.GiftPool, error) {
	pools := []storage.GiftPool{}
	total := 0
	for i, f := range fields {
		if strings.HasPrefix(f.Name, "/") {
			if f.Value != "" {
				return nil, fmt.Errorf("Random gift has content after a closing pool")
			}
			continue
		}
		if len(pools) >= 4096 || i+1 == len(fields) || fields[i+1].Name != "/"+f.Name {
			return nil, fmt.Errorf("Random gift pool is not closed or exceeds its budget: %s", f.Name)
		}
		p := storage.GiftPool{UsableJobs: math.MaxUint16, MinimumLevel: 1, MaximumLevel: 255, Entries: []storage.GiftPoolEntry{}}
		if f.Name == "avatar" || f.Name == "special avatar" {
			if err := parseGiftAvatarPool(&p, f.Value, &total, special); err != nil {
				return nil, err
			}
			pools = append(pools, p)
			continue
		}
		header, stride := 1, 3
		values := f.Value
		switch f.Name {
		case "charactor", "grow type":
			job := giftConditionalJob.FindStringSubmatch(values)
			if job == nil {
				return nil, fmt.Errorf("Random gift conditional job grammar is unsupported")
			}
			if job[1] != "all" {
				j := slices.Index(giftJobs, job[1])
				if j < 0 {
					return nil, fmt.Errorf("Random gift conditional job is unknown")
				}
				p.UsableJobs = 1 << j
			}
			values = values[len(job[0]):]
			header = 3
			if f.Name == "grow type" {
				header = 4
			}
		case "broadcast":
			stride = 4
		case "etc", "equipment", "stackable", "creature", "cera", "emblem", "booster":
		default:
			return nil, fmt.Errorf("Random gift pool requires an unsupported reward tuple: %s", f.Name)
		}
		n, err := giftInts(values, false)
		if err != nil || len(n) <= header || (len(n)-header)%stride != 0 || n[header-1] == 0 || n[header-1] > 255 {
			return nil, fmt.Errorf("Random gift draw count or reward tuple width is invalid")
		}
		p.DrawCount = int(n[header-1])
		if header > 1 {
			if n[0] == 0 || n[0] > 255 || n[1] < n[0] || n[1] > 255 || header == 4 && n[2] > 255 {
				return nil, fmt.Errorf("Random gift conditional level or growth range is invalid")
			}
			p.MinimumLevel, p.MaximumLevel = byte(n[0]), byte(n[1])
			if header == 4 {
				grow := byte(n[2])
				p.GrowType = &grow
			}
		}
		var weight uint64
		for j := header; j < len(n); j += stride {
			id, w, count := n[j], n[j+1], n[j+2]
			total++
			if id == math.MaxUint32 || count == 0 || total > 32768 || stride == 4 && n[j+3] > 1 {
				return nil, fmt.Errorf("Random gift reward identity, quantity, or broadcast flag is invalid")
			}
			r := resolve(id, count, "")
			r.Broadcast = stride == 4 && n[j+3] != 0
			p.Entries = append(p.Entries, storage.GiftPoolEntry{Weight: uint64(w), Reward: r})
			weight += uint64(w)
		}
		if weight == 0 {
			return nil, fmt.Errorf("Random gift pool has no positive reward weight")
		}
		pools = append(pools, p)
	}
	if len(pools) == 0 {
		return nil, fmt.Errorf("Random gift complete pool list is absent")
	}
	return pools, nil
}

func parseGiftAvatarPool(pool *storage.GiftPool, value string, total *int, special giftSpecialResolve) error {
	if !giftSignedIntegers.MatchString(value) {
		return fmt.Errorf("Random avatar integer grammar is unsupported")
	}
	cells := strings.Fields(value)
	draws, err := giftInt(cells[0])
	if err != nil || draws == 0 || draws > 255 || len(cells) == 1 || (len(cells)-1)%5 != 0 {
		return fmt.Errorf("Random avatar draw count or tuple width is invalid")
	}
	pool.DrawCount = int(draws)
	var weight uint64
	for i := 1; i < len(cells); i += 5 {
		id, e1 := giftInt(cells[i])
		w, e2 := giftInt(cells[i+1])
		count, e3 := giftInt(cells[i+2])
		period, e4 := strconv.ParseInt(cells[i+3], 10, 32)
		ability, e5 := giftInt(cells[i+4])
		*total++
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || id == 0 || id == math.MaxUint32 || count == 0 || ability > 255 || *total > 32768 {
			return fmt.Errorf("Random avatar identity, quantity, period or ability is invalid")
		}
		var r storage.GiftReward
		if period != 0 {
			r = rejectedReward(id, count, "Random avatar relative period requires its verified native lifetime semantics")
		} else {
			r = special(id, count, 3, 0)
			if r.SpecialProduct != nil && r.SpecialProduct.ResourcePeriod != 0 {
				r = rejectedReward(id, count, "Random avatar third creation model is not explicitly permanent")
			}
		}
		a := byte(ability)
		r.FixedAbility = &a
		weight += uint64(w)
		pool.Entries = append(pool.Entries, storage.GiftPoolEntry{Weight: uint64(w), Reward: r})
	}
	if weight == 0 {
		return fmt.Errorf("Random avatar pool has no positive reward weight")
	}
	return nil
}

var giftRestrictionFlag = regexp.MustCompile("`([^`]+)`")

func parseGiftUsage(fields []gm.Field, resolve giftResolve) (*storage.GiftUsage, error) {
	usage := &storage.GiftUsage{ConsumeSource: true, WeeklyResetDay: 4, AccumulatedRewards: []storage.GiftAccumulatedReward{}}
	for _, name := range []string{"cool time", "cooltime group", "total usable count", "total weekly usable count", "total weekly usable count reset day", "total accumulated usable count"} {
		v, at, err := giftUnique(fields, name)
		if err != nil {
			return nil, err
		}
		if at < 0 {
			continue
		}
		value, err := giftInt(v)
		if err != nil {
			return nil, fmt.Errorf("Gift usage integer is invalid: %s", name)
		}
		switch name {
		case "cool time":
			usage.CooldownMilliseconds = value
		case "cooltime group":
			usage.CooldownGroup = value
		case "total usable count":
			usage.DailyLimit = value
		case "total weekly usable count":
			usage.WeeklyLimit = value
		case "total weekly usable count reset day":
			if value > 6 {
				return nil, fmt.Errorf("Gift usage integer is invalid: %s", name)
			}
			usage.WeeklyResetDay = byte(value)
		case "total accumulated usable count":
			usage.AccumulatedLimit = value
		}
	}
	maintenance, at, err := giftUnique(fields, "cooltime maintenance")
	if err != nil || at >= 0 && strings.Join(strings.Fields(maintenance), " ") != "1 1 1" {
		return nil, fmt.Errorf("Gift cooldown persistence modes are unsupported")
	}
	var impossible []int
	closed := 0
	for i, f := range fields {
		switch f.Name {
		case "impossible contents":
			impossible = append(impossible, i)
		case "/impossible contents":
			closed++
		}
	}
	for _, i := range impossible {
		v := fields[i].Value
		if len(impossible) > 1 && (v != "`gift`" || closed != len(impossible) || i+1 == len(fields) || fields[i+1].Name != "/impossible contents" || fields[i+1].Value != "") {
			return nil, fmt.Errorf("Ambiguous gift usage field: impossible contents")
		}
		if v == "" {
			continue
		}
		flags := giftRestrictionFlag.FindAllStringSubmatch(v, -1)
		if len(flags) == 0 || strings.TrimSpace(giftRestrictionFlag.ReplaceAllString(v, "")) != "" {
			return nil, fmt.Errorf("Gift impossible-contents grammar is malformed")
		}
		for _, f := range flags {
			switch f[1] {
			case "delete booster":
				usage.ConsumeSource = false
			case "gift", "charac cargo", "ban redeemitem":
			default:
				return nil, fmt.Errorf("Gift requires an unsupported contents restriction: %s", f[1])
			}
		}
	}
	const accumulated = "total accumulated usable count reward"
	v, at, err := giftUnique(fields, accumulated)
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		if at+1 >= len(fields) || fields[at+1].Name != "/"+accumulated {
			return nil, fmt.Errorf("Gift accumulated reward list is not closed")
		}
		n, err := giftInts(v, false)
		if err != nil || len(n)%3 != 0 || len(n)/3 > 255 {
			return nil, fmt.Errorf("Gift accumulated reward tuple width or count is invalid")
		}
		grouped := map[uint32][]storage.GiftReward{}
		for i := 0; i < len(n); i += 3 {
			threshold, id, count := n[i], n[i+1], n[i+2]
			if threshold == 0 || id == math.MaxUint32 || count == 0 {
				return nil, fmt.Errorf("Gift accumulated reward threshold, identity or quantity is invalid")
			}
			grouped[threshold] = append(grouped[threshold], resolve(id, count, ""))
		}
		for _, k := range sortedIDs(grouped) {
			usage.AccumulatedRewards = append(usage.AccumulatedRewards, storage.GiftAccumulatedReward{Count: k, Rewards: grouped[k]})
		}
	}
	if usage.ConsumeSource && usage.CooldownMilliseconds == 0 && usage.CooldownGroup == 0 && usage.DailyLimit == 0 && usage.WeeklyLimit == 0 && usage.AccumulatedLimit == 0 && len(usage.AccumulatedRewards) == 0 {
		return nil, nil
	}
	return usage, nil
}
