package host

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"s9a1/server/internal/duellobby"
	"s9a1/server/internal/opening"
	"s9a1/server/internal/protocol"
	"s9a1/server/internal/storage"
)

type game struct {
	server          *Server
	conn            net.Conn
	lease           storage.Lease
	channel         uint16
	seq             uint16
	emptyProbe      bool
	opening         *opening.Run
	nextActor       uint16
	premium         [4]int64
	battlePassSent  bool
	gaugePending    bool
	partySettings   []byte
	questAcceptSeen map[uint16]bool
	contracts       [4]int64
	duelPosition    *storage.Position
	duelLast        *duelRoad
}

func (g *game) send(class byte, op uint16, body []byte) error {
	p, err := protocol.EncodeGame(protocol.ServerToClient, class, op, g.seq, body, class == 0 && op == 356)
	if err != nil {
		return err
	}
	return writeFull(g.conn, p)
}

func (g *game) open(ctx context.Context, port uint16) error {
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	material := make([]byte, 334)
	if _, err = rand.Read(material); err != nil {
		return err
	}
	info, err := protocol.ChannelInformation(byte(g.channel), time.Now().Unix(), material)
	if err != nil {
		return err
	}
	if err = g.send(0, 1, info); err != nil {
		return err
	}
	types := map[uint16]byte{19: 1, 251: 13, 252: 8, 63: 63}
	login, err := protocol.Login(port, types[g.channel])
	if err != nil {
		return err
	}
	if err = g.send(1, 1, login); err != nil {
		return err
	}
	if len(r.GameOptions) > 0 {
		body, err := protocol.RestoreGameOptions(r.GameOptions)
		if err != nil {
			return err
		}
		if err = g.send(0, 173, body); err != nil {
			return err
		}
	}
	if len(r.Hotkeys) > 0 {
		body, err := protocol.RestoreHotkeys(r.Hotkeys)
		if err != nil {
			return err
		}
		return g.send(0, 440, body)
	}
	return nil
}

func (g *game) handle(ctx context.Context, f protocol.Frame) error {
	if f.Class != 1 {
		return fmt.Errorf("game class %d is not implemented", f.Class)
	}
	g.seq = f.Sequence
	db := g.server.DB
	switch f.Opcode {
	case 8:
		if err := protocol.CheckListRequest(f.Body); err != nil {
			return err
		}
		r, err := db.List(ctx, g.lease)
		if err != nil {
			return err
		}
		g.emptyProbe = len(r.Characters) == 0
		return g.roster(r)
	case 679:
		if err := protocol.CheckEmptyRosterProbe(f.Body); err != nil {
			return err
		}
		if !g.emptyProbe {
			return storage.ErrPhase
		}
		if err := db.ValidateLease(ctx, g.lease); err != nil {
			return err
		}
		g.emptyProbe = false
		return g.send(1, 679, protocol.Failure(0))
	case 1715:
		body, err := protocol.Ping(f.Body)
		if err != nil {
			return err
		}
		if err = db.ValidateLease(ctx, g.lease); err != nil {
			return err
		}
		if err = g.send(1, 1715, body); err != nil {
			return err
		}
		if g.gaugePending && g.opening == nil {
			return g.send(0, 1987, make([]byte, 4))
		}
		return nil
	case 692:
		name, err := protocol.ReadNameRequest(f.Body)
		if err != nil {
			return err
		}
		available, err := db.NameAvailable(ctx, g.lease, name)
		if errors.Is(err, storage.ErrInvalid) {
			return g.send(1, 692, protocol.Failure(23))
		}
		if err != nil {
			return err
		}
		if !available {
			return g.send(1, 692, protocol.Failure(20))
		}
		return g.send(1, 692, protocol.Ack())
	case 5:
		return g.create(ctx, f.Body)
	case 12, 13:
		return g.party(ctx, f.Opcode, f.Body)
	case 195:
		if len(f.Body) != 0 {
			return storage.ErrInvalid
		}
		r, err := db.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		ready := r.SelectedID != 0 && (r.Phase == storage.TownReady || duellobby.IsChannel(g.channel) && g.duelPosition != nil) && g.opening == nil
		return g.send(1, 195, duellobby.ChannelInfo(ready))
	case 19:
		return g.moveItem(ctx, f)
	case 31, 32:
		return g.questChange(ctx, f)
	case 324:
		return g.mailRecipient(ctx, f.Body)
	case 36, 1418:
		return g.travel(ctx, f.Opcode, f.Body)
	case 4:
		return g.selectCharacter(ctx, f.Body)
	case 7:
		if len(f.Body) != 0 {
			return errors.New("unexpected return to list body")
		}
		if err := db.ReturnToList(ctx, g.lease); err != nil {
			return err
		}
		r, err := db.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		g.emptyProbe = len(r.Characters) == 0
		g.opening = nil
		g.battlePassSent = false
		g.questAcceptSeen = nil
		g.partySettings = nil
		g.duelPosition, g.duelLast = nil, nil
		g.contracts, g.premium = [4]int64{}, [4]int64{}
		g.gaugePending = false
		if err = g.send(1, 7, protocol.Ack()); err != nil {
			return err
		}
		return g.roster(r)
	case 35, 623:
		if g.opening != nil {
			return g.inDungeonPosition(f.Opcode, f.Body)
		}
		if f.Opcode == 35 {
			p, err := protocol.ReadPosition(f.Body)
			if err != nil {
				return err
			}
			return g.move(ctx, p.X, p.Y, p.Speed)
		}
		p, err := protocol.ReadMotion(f.Body)
		if err != nil {
			return err
		}
		if p.VisualState != 6 && p.StateParameter != 0 {
			return storage.ErrInvalid
		}
		return g.move(ctx, p.X, p.Y, p.Speed)
	case 120, 143:
		return g.syncTown(ctx, f.Opcode, f.Body)
	case 37, 39, 45, 191:
		return g.openingRequest(ctx, f.Opcode, f.Body)
	case 63:
		if len(f.Body) != 0 {
			return errors.New("unexpected account info body")
		}
		r, err := db.Snapshot(ctx, g.lease)
		if err != nil {
			return err
		}
		if err = g.send(1, 63, protocol.Ack()); err != nil {
			return err
		}
		return g.wallet(r.Cera)
	case 197:
		data, err := protocol.ReadGameOptions(f.Body)
		if err != nil {
			return err
		}
		return db.SaveGameOptions(ctx, g.lease, data)
	case 198:
		data, err := protocol.ReadHotkeys(f.Body)
		if err != nil {
			return err
		}
		return db.SaveHotkeys(ctx, g.lease, data)
	case 3:
		if len(f.Body) != 1 {
			return errors.New("invalid exit reason")
		}
		if err := db.Release(ctx, g.lease); err != nil {
			return err
		}
		if err := g.send(1, 3, protocol.ExitAccepted()); err != nil {
			return err
		}
		return io.EOF
	default:
		return fmt.Errorf("unported game request %d", f.Opcode)
	}
}

type savedGameplay struct {
	Initialized bool
	Progression struct {
		GrowType              byte
		Fatigue, FatigueLimit uint16
		Hp                    uint32
	}
	Tutorial *struct {
		ReturnPoint  storage.Position
		Completed    bool
		Dungeon      uint32
		ResourceHash string
	}
	CharacterRename *struct{ PendingSource uint32 }
}

func readGameplay(data json.RawMessage) (savedGameplay, error) {
	var p savedGameplay
	err := json.Unmarshal(data, &p)
	if err == nil && p.Initialized && p.Progression.Fatigue > p.Progression.FatigueLimit {
		err = errors.New("invalid saved fatigue")
	}
	return p, err
}

func (g *game) roster(r storage.Roster) error {
	rows := make([]protocol.RosterCharacter, 0, len(r.Characters))
	for _, c := range r.Characters {
		p, err := readGameplay(c.Gameplay)
		if err != nil {
			return err
		}
		row := protocol.RosterCharacter{ID: c.ID, Name: c.Name, Level: c.Level, Job: c.Job, GrowType: p.Progression.GrowType, Worn: wornEquipment(c.Worn)}
		if p.CharacterRename != nil {
			row.RenamePending = p.CharacterRename.PendingSource != 0
			row.PurchasedRename = p.CharacterRename.PendingSource == 2682290
		}
		rows = append(rows, row)
	}
	body, err := protocol.Roster(rows, r.Capacity)
	if err != nil {
		return err
	}
	return g.send(0, 2, body)
}

func wornEquipment(items []storage.Equipment) []protocol.Equipment {
	out := make([]protocol.Equipment, len(items))
	for i, item := range items {
		out[i] = protocol.Equipment{
			Slot: item.Slot, TemplateID: item.TemplateID, QualitySeed: item.QualitySeed, Durability: item.Durability,
			Raw119: item.Core, Reinforcement: item.Reinforcement, Forge: item.Forge,
			AmplificationType: item.AmplificationType, AmplificationValue: item.AmplificationValue,
		}
	}
	return out
}

func (g *game) create(ctx context.Context, body []byte) error {
	req, err := protocol.ReadCreateRequest(body)
	if err != nil {
		return err
	}
	if !req.Supported {
		return g.send(1, 5, protocol.Failure(18))
	}
	now := time.Now()
	template, ok, err := g.server.Characters.Creation(req.Job, req.GrowType, now)
	if err != nil {
		return err
	}
	if !ok {
		return g.send(1, 5, protocol.Failure(18))
	}
	p, err := readGameplay(template.Gameplay)
	if err != nil {
		return err
	}
	in := storage.CreateCharacter{Name: req.Name, Job: req.Job, Created: uint32(now.Unix()), Stats: template.Stats, Gameplay: template.Gameplay, ResourceHash: template.SourceHash}
	if p.Tutorial != nil {
		in.Opening = &p.Tutorial.ReturnPoint
	}
	c, err := g.server.DB.Create(ctx, g.lease, in)
	switch {
	case errors.Is(err, storage.ErrCapacity):
		return g.send(1, 5, protocol.Failure(4))
	case errors.Is(err, storage.ErrInvalid) || errors.Is(err, storage.ErrNameTaken):
		return g.send(1, 5, protocol.Failure(18))
	case err != nil:
		return err
	}
	g.emptyProbe = false
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	accepted, err := protocol.CreateAccepted(c.ID, c.Name)
	if err != nil {
		return err
	}
	if err = g.send(1, 5, accepted); err != nil {
		return err
	}
	return g.roster(r)
}

func (g *game) selectCharacter(ctx context.Context, body []byte) error {
	index, err := protocol.ReadSelectRequest(body)
	if err != nil {
		return err
	}
	r, err := g.server.DB.Snapshot(ctx, g.lease)
	if err != nil {
		return err
	}
	if int(index) >= len(r.Characters) {
		return storage.ErrPhase
	}
	c := r.Characters[index]
	p, err := readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	if p.CharacterRename != nil && p.CharacterRename.PendingSource != 0 {
		return storage.ErrPhase
	}
	duel := duellobby.IsChannel(g.channel)
	if !duel && g.channel != 19 {
		return errors.New("selection on this channel has not yet been ported")
	}
	var placement *storage.SelectionPlacement
	if duel {
		if c.Level < duellobby.MinimumLevel {
			return storage.ErrInvalid
		}
	} else {
		placement, err = g.loginPlacement(c, p)
		if err != nil {
			return err
		}
	}
	c, err = g.server.DB.SelectAt(ctx, g.lease, index, placement)
	if err != nil {
		return err
	}
	p, err = readGameplay(c.Gameplay)
	if err != nil {
		return err
	}
	view := protocol.SelectCharacter{ID: c.ID, Created: c.Created, FatigueLimit: p.Progression.FatigueLimit, TutorialDone: p.Tutorial == nil || p.Tutorial.Completed}
	if p.Initialized {
		view.FatigueUsed = p.Progression.FatigueLimit - p.Progression.Fatigue
	}
	now := time.Now().Unix()
	remaining := func(until int64) uint64 { return uint64(max(int64(0), until-now)) }
	initialTown := uint32(0)
	conqueror, skillMaster := remaining(r.ConquerorExpiresAt), remaining(r.SkillMasterExpiresAt)
	if duel {
		initialTown = 10
		conqueror, skillMaster = 0, 0
	}
	b, err := protocol.Select(view, initialTown, conqueror, skillMaster, remaining(r.CrystalExpiresAt), p.Tutorial != nil)
	if err != nil {
		return err
	}
	if err = g.send(1, 4, b); err != nil {
		return err
	}
	if err = g.send(0, 1378, protocol.Level(c.Level)); err != nil {
		return err
	}
	g.opening = nil
	g.partySettings = nil
	g.questAcceptSeen = nil
	g.duelPosition, g.duelLast = nil, nil
	if err = g.specialCounters(ctx, c); err != nil {
		return err
	}
	g.premium = [4]int64{}
	g.contracts = [4]int64{r.ConquerorExpiresAt, r.SkillMasterExpiresAt, r.CrystalExpiresAt, r.GrowthExpiresAt}
	return g.showPremium()
}

func (g *game) wallet(cera int64) error { return g.send(0, 53, protocol.AccountWallet(cera)) }

func (g *game) showPremium() error {
	now := time.Now().Unix()
	for i, deadline := range g.contracts {
		seconds := max(int64(0), deadline-now)
		if duellobby.IsChannel(g.channel) && i != 2 {
			seconds = 0
		}
		stamp := deadline
		if deadline > 0 && seconds == 0 {
			stamp = -deadline
		}
		if g.premium[i] == stamp {
			continue
		}
		b := []byte{2, 0, [...]byte{22, 27, 97, 84}[i]}
		b = binary.LittleEndian.AppendUint64(b, uint64(seconds))
		if err := g.send(0, 66, b); err != nil {
			return err
		}
		g.premium[i] = stamp
	}
	return nil
}
