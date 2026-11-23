package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"
)

type CommerceRejection uint16

func (code CommerceRejection) Error() string { return fmt.Sprintf("commerce rejected (%d)", code) }

type CargoStage struct {
	Previous, Capacity                       uint16
	GoldLimit, Material, MaterialCount, Gold uint32
	Commodity                                int32
}

type CargoCatalog struct {
	RequiredLevel byte
	Stages        []CargoStage
}

type CommerceCatalog struct {
	AccountCargo         CargoCatalog
	TownPeers            []CommercePeer
	DungeonPeers         []CommercePeer
	OculusPeers          []OculusMerchantPeer
	TownAreas            []CommerceArea
	TownPhases           map[uint16][]CommercePhase
	TownMaps             map[string][]uint32
	Visibility           CommerceVisibility
	Products             []CommerceProduct
	DungeonProducts      []CommerceProduct
	SalePrices           map[uint32]uint32
	DailyExpiryTemplates map[uint32]json.RawMessage
	Templates            map[uint32]json.RawMessage
	Cash                 map[uint32]CashOffer
	Potions              map[uint32]PotionDefinition
	FatigueItems         map[uint32]FatigueItemDefinition
	Discard              map[uint32]DiscardPolicy
	Disassemblers        []uint32
	Disassembly          map[uint32]DisassemblyRecipe
	RepairNPCs           []uint32
	RepairDefinitions    map[uint32]RepairDefinition
	SortMetadata         map[uint32]SortMetadata
	AvatarOffers         map[uint32]AvatarCashOffer
	AvatarBoxes          map[uint32]AvatarGiftBox
	PurchaseCodes        []uint32
	Gifts                map[uint32]GiftDefinition
	CashPackages         map[uint32]GiftCashPackage
	CargoGift            json.RawMessage
	ContractGrants       map[uint32]GiftContract
	AvatarClones         map[uint32]AvatarClone
	MagicSeal            map[uint32]MagicSealDefinition
	ContainerCoupons     map[uint32]ContainerCoupon
	PetHatch             map[uint32]PetHatchDefinition
	PetNames             map[uint32]PetNameRule
	PetRenameCard        *PetRenameCard
	PetCare              *PetCareRule
	PetFeed              *PetFeedRule
	Lottery              map[uint32]LotteryRule
	LotteryTemplates     map[uint32]json.RawMessage
	KeyedBoxes           map[uint32]KeyedBoxRule
	LuckyBoxes           map[uint32]LuckyBoxRule
	Quality              QualityCatalog
	Craft                CraftCatalog
	GmApcs               map[uint32]string
	FateExchange         *FateExchangeCatalog
	FateBoxes            FateBoxesCatalog
	BoxWeights           map[uint32]int64
	Activities           []uint16
}

type CommercePeer struct {
	NPC        uint32 `json:"Npc"`
	Shop       uint32
	Placements []uint32
}

type CommerceArea struct {
	Town, Area uint16
	Path       string
	Phases     []string
	Permission struct {
		Level                     byte
		All, Any, Except, Started []uint16
		Blocked                   bool
	}
}

type CommercePhase struct {
	Kind      string
	Id, Index int
}

type CommerceVisibility struct {
	Hidden, Blocked []uint32
	Rules           map[uint32][]CommerceVisibilityRule
}

type CommerceVisibilityRule struct {
	Quest       uint16
	Clear, Show bool
	Earlier     []uint16
}

type CommercePayment struct {
	TemplateID uint32 `json:"TemplateId"`
	Count      uint32
}

type CommerceLimit struct {
	Account bool
	Period  byte
	Count   uint32
	Legacy  bool
}

type CommerceProduct struct {
	NPC          uint32 `json:"Npc"`
	Shop         uint32
	Job          byte
	TemplateID   uint32 `json:"TemplateId"`
	Gold         uint32
	Materials    []CommercePayment
	Template     json.RawMessage
	Limits       []CommerceLimit
	MinimumLevel byte
	Equipment    bool
}

type CashOffer struct {
	Commodity, Count, Cera, Gold                                                uint32
	TemplateID                                                                  uint32 `json:"TemplateId"`
	TargetBagExpansion, TargetPersonalCargoCapacity, TargetAccountCargoCapacity uint16
	RevivalCoins, NativeFreePointOnly                                           bool
	AccountRestriction, AccountLimit, CharacterRestriction, CharacterDailyLimit uint32
	AccountPeriod, CharacterPeriod                                              byte
	BonusItems                                                                  []CommercePayment
	Template                                                                    json.RawMessage
	RequiredCouponTemplates                                                     map[uint32]json.RawMessage
	OwnedItemExclusions                                                         []uint32
	PreviousPersonalCargoCapacity                                               uint16
	PremiumType                                                                 byte
	PremiumSeconds                                                              uint32
	CharacterSlot                                                               bool
	TargetAvatarExpansion                                                       uint16
	PetTemplate                                                                 json.RawMessage
	NameTag                                                                     *CashNameTag
	SkillStyleHash                                                              string
	BattlePassHash                                                              string
	BattlePassDeluxe                                                            bool
}

type CommerceReplay struct {
	Opcode, Sequence, CharacterID uint16
	Revision, AssetRevision       int64
	Body                          string
	Scene                         string
	Granted                       Asset
	Grants                        []Asset
	Gift                          *GiftResult
	LuckyRewards                  []LuckyBoxPayout
	Craft                         *CraftResult
	Summon                        *PotionSummonAuthorization
	MoveDestination               uint16
	MoveCount                     uint32
}

type CommerceResult struct {
	Character Character
	Inventory Inventory
	Replay    CommerceReplay
	Granted   Asset
	Grants    []Asset
	Replayed  bool
}

type CommerceTx struct {
	ctx             context.Context
	tx              *sql.Tx
	account         string
	Character       Character
	Inventory       Inventory
	Granted         Asset
	Grants          []Asset
	before          Inventory
	accountCapacity byte
	slotsPurchased  bool
	noChange        bool
	gift            *GiftResult
	luckyRewards    []LuckyBoxPayout
	craft           *CraftResult
	summon          *PotionSummonAuthorization
	specialValue    uint32
	conquerorExpiry int64
	lease           Lease
	moveDestination uint16
	moveCount       uint32
}

// Commerce owns the live lease and its inventory revision through planning,
// asset changes, persistence, and final readback.
func (s *Store) Commerce(ctx context.Context, l Lease, op, seq uint16, body []byte, replay CommerceReplay, apply func(*CommerceTx) error) (CommerceResult, error) {
	return s.CommerceScene(ctx, l, op, seq, body, replay, replay.Scene, nil, apply)
}

// CommerceScene verifies a scoped merchant inside the owner transaction before
// replaying a reply or changing any asset.
func (s *Store) CommerceScene(ctx context.Context, l Lease, op, seq uint16, body []byte, replay CommerceReplay, scene string,
	verify func(*CommerceTx) error, apply func(*CommerceTx) error) (CommerceResult, error) {
	return writeTx(ctx, s.db, func(tx *sql.Tx) (CommerceResult, error) {
		var out CommerceResult
		r, err := lockAccount(ctx, tx, l, true)
		if err != nil {
			return out, err
		}
		if r.Phase != TownReady || r.SelectedID == 0 {
			return out, ErrPhase
		}
		c, err := mailCharacter(ctx, tx, l.Account, r.SelectedID)
		if err != nil {
			return out, err
		}
		in, err := readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		if err = commerceWorn(&c, in); err != nil {
			return out, err
		}
		before := in
		before.Items = slices.Clone(in.Items)
		edit := CommerceTx{ctx: ctx, tx: tx, account: l.Account, Character: c, Inventory: in, before: before, accountCapacity: r.Capacity, conquerorExpiry: r.ConquerorExpiresAt, lease: l}
		if verify != nil {
			if err := verify(&edit); err != nil {
				return out, err
			}
		}
		check := CommerceReplay{Opcode: op, Sequence: seq, CharacterID: c.ID, Revision: c.Revision, AssetRevision: r.AssetRevision, Body: string(body), Scene: scene}
		if replay.Opcode == op && replay.Sequence == seq && replay.CharacterID == c.ID && replay.Revision == c.Revision && replay.AssetRevision == r.AssetRevision && replay.Body == string(body) && replay.Scene == scene {
			return CommerceResult{c, in, replay, replay.Granted, replay.Grants, true}, nil
		}
		if err = apply(&edit); err != nil {
			return out, err
		}
		if edit.noChange {
			return CommerceResult{Character: c, Inventory: in, Replay: check}, nil
		}
		c, in = edit.Character, edit.Inventory
		c.Gold, c.Coins = in.Gold, in.Coins
		if in.Gold < 0 || in.Gold > math.MaxUint32 || in.Coins < 0 || in.Coins > math.MaxUint32 || in.Cera < 0 || in.Cera > math.MaxInt32 || in.CargoGold < 0 || in.CargoGold > math.MaxInt32 {
			return out, ErrFunds
		}
		c, err = scanCharacter(tx.QueryRowContext(ctx, `UPDATE s9a1.characters
			SET gameplay_json=$4::json,gold=$5,coin=$6,bag_expansion=$7,personal_capacity=$8,revision=revision+1
			WHERE account=$1 AND wire_id=$2 AND revision=$3 RETURNING `+characterColumns,
			l.Account, c.ID, c.Revision, string(c.Gameplay), in.Gold, in.Coins, in.BagExpansion, in.PersonalCapacity))
		if err != nil {
			return out, err
		}
		if in.AssetRevision != before.AssetRevision || in.Cera != before.Cera || in.CargoGold != before.CargoGold || in.CargoCapacity != before.CargoCapacity || in.TreasureGauge != before.TreasureGauge || in.UniqueAvatarGauge != before.UniqueAvatarGauge {
			_, err = tx.ExecContext(ctx, `UPDATE s9a1.accounts SET cera=$2,cargo_gold=$3,cargo_capacity=$4,treasure_gauge=$5,unique_avatar_gauge=$6,asset_revision=asset_revision+1 WHERE account=$1`, l.Account, in.Cera, in.CargoGold, in.CargoCapacity, in.TreasureGauge, in.UniqueAvatarGauge)
			if err != nil {
				return out, err
			}
			r.AssetRevision++
			r.Cera, r.CargoGold = in.Cera, in.CargoGold
			r.TreasureGauge = in.TreasureGauge
			r.UniqueAvatarGauge = in.UniqueAvatarGauge
		}
		in, err = readInventory(ctx, tx, l.Account, r)
		if err != nil {
			return out, err
		}
		if err = commerceWorn(&c, in); err != nil {
			return out, err
		}
		check.Revision, check.AssetRevision, check.Granted = c.Revision, in.AssetRevision, edit.Granted
		check.Grants = edit.Grants
		check.Gift = edit.gift
		check.LuckyRewards = edit.luckyRewards
		check.Craft = edit.craft
		check.Summon = edit.summon
		if check.Craft != nil && (op == 80 || op == 205 || op == 1734) {
			check.Craft.Changes, check.Craft.Cleared = craftBagChanges(before, in)
		}
		check.MoveDestination = edit.moveDestination
		check.MoveCount = edit.moveCount
		return CommerceResult{c, in, check, edit.Granted, edit.Grants, false}, nil
	})
}

func commerceWorn(c *Character, in Inventory) error {
	c.Worn = nil
	for _, a := range in.Items {
		if a.Location != 3 || a.CharacterID != c.ID {
			continue
		}
		e, err := a.WornEquipment(*c)
		if err != nil {
			return err
		}
		c.Worn = append(c.Worn, e)
	}
	return nil
}

func (x *CommerceTx) SpendGoldExpansion(stage byte) error {
	var profile struct{ GoldExpansion byte }
	if err := json.Unmarshal(x.Character.Gameplay, &profile); err != nil {
		return err
	}
	if x.Character.Level < 60 || stage == 0 || stage > 8 || stage != profile.GoldExpansion+1 {
		return ErrInvalid
	}
	if x.Inventory.Gold < 5_000_000 {
		return ErrFunds
	}
	x.Inventory.Gold -= 5_000_000
	return replaceMailFields(&x.Character, map[string]any{"GoldExpansion": stage})
}

func (x *CommerceTx) AccountCargo(op uint16, amount uint32, catalog CargoCatalog, limits map[byte]int64) error {
	in := &x.Inventory
	if op == 305 || op == 306 {
		open := op == 305
		if open != (in.CargoCapacity == 0) || open && x.Character.Level < catalog.RequiredLevel {
			return ErrPhase
		}
		index := slices.IndexFunc(catalog.Stages, func(s CargoStage) bool { return s.Previous == in.CargoCapacity })
		if index < 0 {
			return ErrInvalid
		}
		stage := catalog.Stages[index]
		if stage.Commodity != -1 || stage.Capacity <= stage.Previous || stage.Capacity > 256 {
			return ErrInvalid
		}
		if int64(stage.Gold) > in.Gold {
			return ErrFunds
		}
		if err := x.spendMaterial(stage.Material, int64(stage.MaterialCount)); err != nil {
			return err
		}
		in.Gold -= int64(stage.Gold)
		in.CargoCapacity = stage.Capacity
		return nil
	}
	if in.CargoCapacity == 0 || amount == 0 || amount > math.MaxInt32 {
		return CommerceRejection(10)
	}
	index := slices.IndexFunc(catalog.Stages, func(s CargoStage) bool { return s.Capacity == in.CargoCapacity })
	if index < 0 {
		return CommerceRejection(10)
	}
	n := int64(amount)
	if op == 307 {
		if in.Gold < n {
			return CommerceRejection(10)
		}
		if in.CargoGold+n > int64(min(catalog.Stages[index].GoldLimit, math.MaxInt32)) {
			return CommerceRejection(95)
		}
		in.Gold -= n
		in.CargoGold += n
		return nil
	}
	if op != 308 || in.CargoGold < n {
		return CommerceRejection(10)
	}
	limit, err := MailGoldLimit(x.Character, limits)
	if err != nil {
		return err
	}
	if in.Gold+n > limit {
		return CommerceRejection(94)
	}
	in.Gold += n
	in.CargoGold -= n
	return nil
}

func (x *CommerceTx) debitAsset(i int, quantity uint32) error {
	a := &x.Inventory.Items[i]
	if quantity == 0 || quantity > a.Count {
		return ErrInvalid
	}
	if a.CharacterID == 0 {
		x.Inventory.AssetRevision++
	}
	var err error
	if quantity == a.Count {
		_, err = x.tx.ExecContext(x.ctx, "DELETE FROM s9a1.assets WHERE instance_id=$1", a.UID)
	} else {
		_, err = x.tx.ExecContext(x.ctx, "UPDATE s9a1.assets SET count=count-$2 WHERE instance_id=$1", a.UID, quantity)
	}
	if err == nil {
		a.Count -= quantity
	}
	return err
}

func (x *CommerceTx) spendMaterial(template uint32, quantity int64) error {
	if quantity < 0 || template == 0 && quantity != 0 {
		return ErrInvalid
	}
	now := time.Now().Unix()
	for i, a := range x.Inventory.Items {
		if quantity == 0 {
			return nil
		}
		if a.Location != 0 || a.TemplateID != template || a.Count == 0 {
			continue
		}
		var t ordinaryTemplate
		attr, err := movementAttributes(&a)
		if err != nil || json.Unmarshal(a.Template, &t) != nil {
			return ErrInvalid
		}
		if t.Kind != 2 || attr.Locked || attr.ExpiresAt != nil && *attr.ExpiresAt <= now {
			continue
		}
		take := uint32(min(int64(a.Count), quantity))
		if err = x.debitAsset(i, take); err != nil {
			return err
		}
		quantity -= int64(take)
	}
	if quantity != 0 {
		return ErrFunds
	}
	return nil
}
