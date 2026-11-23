package gm

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var monsterTicket = regexp.MustCompile("^(\\d+)\\s+(\\d+)\\s+\x60fixed\x60\\s+-1$")
var monsterEffect = regexp.MustCompile("^\x60[^\x60]+\x60\\s+\x60[^\x60]+\x60$")
var recipeStrings = regexp.MustCompile("^(?:\x60[^\x60]*\x60\\s*)+$")

// Monster-local and independent rewards may grant the source of an effect or
// recipe. Validate its complete resource family before creating an inventory item.
func monsterAcquisition(s script) (bool, error) {
	kind := s.get("stackable type")
	ticket := kind == "\x60[etc]\x60 1" && (s.has("equipment reinforcement ticket") || s.has("equipment amplify reinforcement ticket"))
	recipe := kind == "\x60[etc]\x60 1" && s.has("emancipate ticket")
	lottery := kind == "\x60[upgradable legacy]\x60 1"
	effect := kind == "\x60[only effect]\x60 0"
	if !ticket && !recipe && !lottery && !effect {
		return false, nil
	}
	bad := errors.New("monster source has incomplete or ambiguous use metadata")
	if ticket && recipe {
		return false, bad
	}
	group := []string{"emancipate group", "input item", "/input item", "output item", "/output item", "/emancipate group"}
	allowed := strings.Split("name|explain|explain detail|flavor text|grade|rarity|usable job|/usable job|attach type|minimum level|stack limit|weight|price|value|creation rate|icon|icon mark|field image|move wav|stackable type|usable period|expiration date|daily delete item|weekly delete item|fatiguetime monthly delete", "|")
	switch {
	case ticket:
		allowed = append(allowed, "equipment reinforcement ticket", "equipment amplify reinforcement ticket", "check usable itemlevel")
	case recipe:
		allowed = append(allowed, group...)
		allowed = append(allowed, "emancipate ticket", "string data", "/string data", "need material", "/need material", "success rate", "maintain ability", "maintain socket stat")
	case effect:
		allowed = append(allowed, "cool time", "string data", "/string data", "shared effect")
	default:
		allowed = append(allowed, "int data", "/int data", "need material", "/need material")
	}
	counts := map[string]int{}
	for _, f := range s.fields {
		counts[f.name]++
		if !slices.Contains(allowed, f.name) || counts[f.name] > 1 && !(recipe && slices.Contains(group, f.name)) {
			return false, bad
		}
	}
	nums := func(body string) ([]uint64, bool) {
		words := strings.Fields(body)
		out := make([]uint64, len(words))
		for i, w := range words {
			n, e := strconv.ParseUint(w, 10, 31)
			if e != nil {
				return nil, false
			}
			out[i] = n
		}
		return out, true
	}
	block := func(name string) (string, bool) {
		for i, f := range s.fields {
			if f.name == name {
				if i+1 < len(s.fields) && f.value != "" && s.fields[i+1].name == "/"+name && s.fields[i+1].value == "" {
					return f.value, true
				}
				break
			}
		}
		return "", false
	}
	switch {
	case ticket:
		rows := append(s.values("equipment reinforcement ticket"), s.values("equipment amplify reinforcement ticket")...)
		if len(rows) != 1 {
			return false, bad
		}
		m := monsterTicket.FindStringSubmatch(rows[0])
		if m == nil {
			return false, bad
		}
		ns, ok := nums(m[1] + " " + m[2])
		if !ok || ns[0] < 1 || ns[0] > 31 || ns[1] > 100 {
			return false, bad
		}
		if v := s.get("check usable itemlevel"); v != "" {
			n, ok := nums(v)
			if !ok || len(n) != 2 || n[0] > n[1] {
				return false, bad
			}
		}
	case recipe:
		k, ok := nums(s.get("emancipate ticket"))
		chance, co := nums(s.get("success rate"))
		str, sk := block("string data")
		if !ok || len(k) != 1 || k[0] == 0 || !co || len(chance) != 1 || s.get("maintain ability") != "\x60grade enchant\x60" || counts["maintain socket stat"] != 1 || s.get("maintain socket stat") != "" || !sk || !recipeStrings.MatchString(str) {
			return false, bad
		}
		seen := map[uint64]bool{}
		for i, f := range s.fields {
			if f.name != "emancipate group" {
				continue
			}
			if i+6 > len(s.fields) {
				return false, bad
			}
			part := s.fields[i : i+6]
			for j, x := range part {
				if x.name != group[j] {
					return false, bad
				}
			}
			if part[2].value != "" || part[4].value != "" || part[5].value != "" {
				return false, bad
			}
			v, k := nums(f.value)
			in, a := nums(part[1].value)
			out, b := nums(part[3].value)
			if !k || len(v) != 1 || seen[v[0]] || !a || !b || len(in)%3 != 0 || len(out)%2 != 0 || slices.Contains(out, 0) {
				return false, bad
			}
			pos := map[uint64]bool{}
			for j := 0; j < len(in); j += 3 {
				if in[j] == 0 || in[j+1] == 0 || pos[in[j+2]] {
					return false, bad
				}
				pos[in[j+2]] = true
			}
			seen[v[0]] = true
		}
		if len(seen) == 0 {
			return false, bad
		}
		for _, n := range group {
			if counts[n] != len(seen) {
				return false, bad
			}
		}
	case effect:
		str, ok := block("string data")
		cd, v := nums(s.get("cool time"))
		shared := s.get("shared effect")
		if !ok || !monsterEffect.MatchString(str) || !v || len(cd) != 1 || shared != "0" && shared != "1" {
			return false, bad
		}
	default:
		str, ok := block("int data")
		v, good := nums(str)
		if !ok || !good || len(v)%3 != 0 {
			return false, bad
		}
		positive := false
		for i := 0; i < len(v); i += 3 {
			if v[i] == 0 || v[i+2] == 0 {
				return false, bad
			}
			positive = positive || v[i+1] > 0
		}
		if !positive {
			return false, bad
		}
	}
	if !ticket && !effect && (counts["need material"] != 0 || counts["/need material"] != 0) {
		str, ok := block("need material")
		v, good := nums(str)
		if !ok || !good || len(v)%2 != 0 || slices.Contains(v, 0) {
			return false, bad
		}
	}
	return true, nil
}
