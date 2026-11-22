package host

import (
	"context"
	"time"

	"s9a1/server/internal/dungeon"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

func (g *game) dungeonMove(ctx context.Context, run *dungeon.Run, c storage.Character, a protocol.Actor, x, y byte, layer bool, sequence uint16) error {
	m, err := run.PlanMove(x, y, layer, sequence)
	if err != nil || m.Deferred {
		return err
	}
	db := g.server.DB
	if len(m.Proof) != 0 {
		if run.OculusEntry == nil {
			d := run.Definition.Dungeon
			quest, _, err := g.server.Characters.Quests.ActiveDungeonQuestProof(c, d.ID, d.QuestConnections)
			if err != nil {
				return err
			}
			if quest == 0 {
				return g.send(1, 45, []byte{0, 4, 0})
			}
		}
		if err := db.ValidateDungeonProof(ctx, g.lease, c, m.Proof); err != nil {
			return err
		}
	}
	if run.OculusEntry != nil {
		if !time.Now().Before(g.oculus.ritualUntil) {
			return g.send(1, 45, []byte{0, 4, 0})
		}
		if _, err := g.oculusSceneHistory(ctx, run, c); err != nil {
			return err
		}
	}
	if m.Cost != 0 {
		fatigue, err := db.ChargeDungeonFatigue(ctx, g.lease, c, time.Now(), m.Cost, false,
			dungeonLuck(g.server.Characters.Dungeons, run), nil, nil)
		if err != nil {
			return err
		}
		a.FatigueLimit, a.FatigueUsed = fatigue.Limit, fatigue.Limit-fatigue.Remaining
	}
	return g.packets(run.ApplyMove(m, a))
}

func (g *game) drainWisdomMove(ctx context.Context, run *dungeon.Run, x, y byte, sequence uint16) error {
	roster, err := g.server.DB.Current(ctx, g.lease)
	if err != nil {
		return err
	}
	c, err := selected(roster)
	if err != nil {
		return err
	}
	actor, _, err := readActor(c, g.channel)
	if err != nil {
		return err
	}
	previous := g.seq
	g.seq = sequence
	defer func() { g.seq = previous }()
	return g.dungeonMove(ctx, run, c, actor, x, y, true, sequence)
}
