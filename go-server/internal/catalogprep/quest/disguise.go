package quest

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/questwire"
)

var disguiseStrings = regexp.MustCompile("\x60([^\x60]*)\x60")
var disguiseForbidden = regexp.MustCompile("(?i)expiration|expiry|usable period|avatar|creature|artifact|package|contract|required skill|purchase|limit count")

type disguiseForm = struct {
	Kind     byte
	Template uint32
	Seconds  int64
}

func BuildDisguise(items ItemLookup) (*questwire.DisguiseData, error) {
	source, err := items.Source(questwire.DisguiseBook)
	if err != nil {
		return nil, err
	}
	fields, err := giftFields(source.Text)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for _, f := range fields {
		if !anyOf(f.name, "name", "explain", "flavor text", "grade", "rarity", "usable job", "/usable job", "attach type", "minimum level",
			"icon", "field image", "stackable type", "move wav", "string data", "/string data", "int data", "/int data", "disguise appendage",
			"/disguise appendage", "action usable place", "/action usable place", "reusable disguise") ||
			!strings.HasPrefix(f.name, "/") && disguiseForbidden.MatchString(f.name) {
			return nil, fmt.Errorf("unsupported disguise field %q", f.name)
		}
		if _, duplicate := values[f.name]; duplicate {
			return nil, fmt.Errorf("duplicate disguise field %q", f.name)
		}
		values[f.name] = f.value
	}
	for _, name := range []string{"usable job", "string data", "int data", "disguise appendage", "action usable place"} {
		_, open := values[name]
		close, paired := values["/"+name]
		if !open || !paired || close != "" {
			return nil, fmt.Errorf("unpaired disguise field %s", name)
		}
	}
	reuse, exists := values["reusable disguise"]
	if values["disguise appendage"] != "\x60village moving speed\x60 \x60%\x60 10" ||
		values["action usable place"] != "\x60[village]\x60 \x60[seria room]\x60 \x60[dont use assult]\x60" || !exists || reuse != "" ||
		values["stackable type"] != "\x60[random disguise]\x60 0" || values["attach type"] != "\x60[account]\x60" ||
		values["usable job"] != "\x60[all]\x60" || values["minimum level"] != "1" {
		return nil, fmt.Errorf("disguise item differs from the native town contract")
	}
	targets := disguiseStrings.FindAllStringSubmatch(values["string data"], -1)
	args := strings.Fields(values["int data"])
	if len(args) < 2 || len(args)%2 != 0 || len(args) > 512 || len(targets) < len(args)/2 {
		return nil, fmt.Errorf("incomplete disguise forms")
	}
	for _, target := range targets {
		if target[1] != "monster" {
			return nil, fmt.Errorf("unsupported disguise target type")
		}
	}
	out := &questwire.DisguiseData{}
	for i := 0; i < len(args); i += 2 {
		id, idErr := strconv.ParseUint(args[i], 10, 31)
		seconds, secErr := strconv.ParseUint(args[i+1], 10, 31)
		if idErr != nil || secErr != nil || id == 0 || seconds == 0 || seconds > 2147483 {
			return nil, fmt.Errorf("invalid disguise form bounds")
		}
		out.Forms = append(out.Forms, disguiseForm{Kind: 2, Template: uint32(id), Seconds: int64(seconds)})
	}
	template := gm.ItemTemplate{TemplateId: questwire.DisguiseBook, Kind: 2, FirstBagSlot: 65, LastBagSlot: 120,
		WornSlot: math.MaxUint16, StackLimit: math.MaxUint32, MinimumLevel: 1, UsableJobs: math.MaxUint16, ResourceHash: source.Hash}
	if limit := values["stack limit"]; limit != "" {
		count, err := strconv.ParseUint(limit, 10, 32)
		if err != nil || count == 0 {
			return nil, fmt.Errorf("invalid disguise stack limit")
		}
		template.StackLimit = uint32(count)
	}
	// The original stackable template carries unrestricted weapon qualification.
	var wire struct {
		gm.ItemTemplate
		WeaponQualification struct {
			Verified bool
			Jobs     []any
			Valid    bool
		}
	}
	wire.ItemTemplate = template
	wire.WeaponQualification.Jobs, wire.WeaponQualification.Valid = []any{}, true
	out.Source, err = json.Marshal(wire)
	return out, err
}
