package host

import (
	"math"

	"s9a1/server/internal/protocol"
	"s9a1/server/internal/resources"
	"s9a1/server/internal/storage"
)

func oculusEligible(c storage.Character, initialized bool, q resources.TownQuests) bool {
	_, cleared := q.Cleared[12167]
	return c.Level == 100 && initialized && cleared
}

func (g *game) oculusPoint(mode uint16, area byte) (storage.Position, bool) {
	if g.server.Characters.Dungeons == nil {
		return storage.Position{}, false
	}
	def, exists := g.server.Characters.Dungeons.Oculus[mode]
	source := def.Rules.RecruitingArea
	if area == 2 {
		source = def.Rules.WaitingArea
	}
	if !exists || source.Town != 81 || source.Area != area || area != 1 && area != 2 {
		return storage.Position{}, false
	}
	return storage.Position{Town: 81, Area: uint16(area), X: source.X, Y: source.Y, Direction: 5}, true
}

func oculusLobbyQuests(c storage.Character) (resources.TownQuests, error) {
	p, err := readGameplay(c.Gameplay)
	if err != nil {
		return resources.TownQuests{}, err
	}
	q, err := resources.ReadTownQuests(c.Gameplay)
	if err == nil && !oculusEligible(c, p.Initialized, q) {
		err = storage.ErrInvalid
	}
	return q, err
}

func (g *game) moveOculus(r storage.Roster, x, y uint16) error {
	if r.Phase != storage.TownReady || g.oculus.position == nil {
		return storage.ErrPhase
	}
	c, err := selected(r)
	if err != nil {
		return err
	}
	dest := *g.oculus.position
	_, current, _, _ := g.oculusLobbyParty()
	if dest.Area == 2 && !current {
		return storage.ErrPhase
	}
	q, err := oculusLobbyQuests(c)
	if err != nil {
		return err
	}
	dest.X, dest.Y = x, y
	if dest.Town != 81 || dest.Area != 1 && dest.Area != 2 || !g.server.Characters.AllowsOrdinaryTownPosition(c.Level, q, point(dest)) {
		return storage.ErrInvalid
	}
	g.oculus.position = &dest
	return nil
}

func (g *game) travelOculus(r storage.Roster, req protocol.TownChange, reject func() error) error {
	if r.Phase != storage.TownReady || g.oculus.position == nil || g.ordinary.Active() || req.NativeNPC || req.Kind != 0 || req.TargetNPC != 0 ||
		req.Town != 81 || req.PreviousTown != 81 || req.Area != 1 && req.Area != 2 || req.Direction > 7 || req.X > math.MaxInt16 || req.Y > math.MaxInt16 {
		return reject()
	}
	c, err := selected(r)
	if err != nil {
		return reject()
	}
	q, err := oculusLobbyQuests(c)
	if err != nil {
		return reject()
	}
	src := *g.oculus.position
	mode, current, recruiting, ended := g.oculusLobbyParty()
	if src.Town != 81 || src.Area != 1 && src.Area != 2 ||
		!(ended && req.Area == 1) && (current && !recruiting || (src.Area == 2 || req.Area == 2) && !recruiting) {
		return reject()
	}
	if req.Area == 1 {
		mode = 0
	}
	dest, ok := g.oculusPoint(mode, req.Area)
	if !ok || req.X != dest.X || req.Y != dest.Y {
		return reject()
	}
	dest.Direction = req.Direction
	same := src.Area == uint16(req.PreviousArea)
	replay := g.oculusLast != nil && g.oculusLast.to == dest && src == dest && g.oculusLast.from.Town == uint16(req.PreviousTown) && g.oculusLast.from.Area == uint16(req.PreviousArea)
	if !same && !replay || same && src.Area == dest.Area && (src.X != dest.X || src.Y != dest.Y) {
		return reject()
	}
	if replay {
		src = g.oculusLast.from
	}
	catalog := g.server.Characters
	if !catalog.AllowsOrdinaryTownPosition(c.Level, q, point(src)) || !catalog.AllowsOrdinaryTownPosition(c.Level, q, point(dest)) {
		return reject()
	}
	g.oculus.position = &dest
	if !replay {
		g.oculusLast = &duelRoad{from: src, to: dest}
	}
	a := protocol.Actor{RosterCharacter: protocol.RosterCharacter{ID: c.ID}, Town: 81, Area: req.Area, X: dest.X, Y: dest.Y, Direction: dest.Direction}
	if err = g.send(0, 23, protocol.Area(a)); err != nil {
		return err
	}
	return g.send(0, 24, protocol.Town(a))
}
