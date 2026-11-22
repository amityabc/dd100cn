// Package itemdata contains the saved resource template shared by the catalog and legacy importer.
package itemdata

type ItemTemplate struct {
	TemplateId                                                                                                        uint32
	Kind                                                                                                              byte
	FirstBagSlot, LastBagSlot, WornSlot                                                                               uint16
	StackLimit                                                                                                        uint32
	MaximumDurability                                                                                                 uint16
	MinimumLevel                                                                                                      byte
	UsableJobs                                                                                                        uint16
	ResourceHash                                                                                                      string
	WornSlotIsNative, CashPackage, CashPackageReward, PvfGiftItem, AccountAfterFirstTrade, MonsterCard, InitialSealed bool
	ResourceExpiration                                                                                                *int64
	FateCollection, AccountCommonInventory, PetConsumable, CharmEnergy                                                bool
	WeaponQualification                                                                                               WeaponQualification
	MythologyHash                                                                                                     string `json:",omitempty"`
	MythologyOptions                                                                                                  byte   `json:",omitempty"`
	RelativeLifetimeSeconds                                                                                           uint32
	DailyDeletion                                                                                                     bool
	WeeklyDeletionDay                                                                                                 *byte
	FatigueTimeMonthlyDeletion                                                                                        bool
}

type WeaponJob struct {
	Job   byte
	Skill uint16
}
type WeaponQualification struct {
	Verified bool
	Jobs     []WeaponJob
	Valid    bool
}
