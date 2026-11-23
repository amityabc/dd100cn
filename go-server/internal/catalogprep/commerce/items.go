package commerce

import (
	"fmt"
	"slices"

	"s9a1/server/internal/catalogprep/gm"
)

type ItemSource = gm.ItemSource
type ItemTemplate = gm.ItemTemplate

type Items struct {
	source               gm.ItemArchive
	equipment, stackable map[int64]string
}

func NewItems(a gm.ItemArchive) (*Items, error) {
	equipment, err := a.ReadIndex("equipment")
	if err != nil {
		return nil, err
	}
	stackable, err := a.ReadIndex("stackable")
	if err != nil {
		return nil, err
	}
	return &Items{source: a, equipment: equipment, stackable: stackable}, nil
}

func (i *Items) Source(id uint32) (ItemSource, error) {
	path, equipment := i.equipment[int64(id)]
	if !equipment {
		path = i.stackable[int64(id)]
	}
	if path == "" {
		return ItemSource{}, fmt.Errorf("PVF asset template %d has no source", id)
	}
	return gm.ReadItemSource(i.source, id, path, equipment)
}

func (i *Items) Ordinary(id uint32) (*ItemTemplate, error) {
	src, err := i.Source(id)
	if err != nil {
		return nil, err
	}
	return src.Ordinary()
}

func (i *Items) Gift(id uint32) (*ItemTemplate, error) {
	src, err := i.Source(id)
	if err != nil {
		return nil, err
	}
	return src.Gift()
}

func (i *Items) Weight(id uint32) (int64, error) {
	src, err := i.Source(id)
	if err != nil {
		return 0, err
	}
	return src.Weight()
}

func (i *Items) EquipmentIds() []uint32 {
	return itemIndexIDs(i.equipment)
}

func (i *Items) StackableIds() []uint32 {
	return itemIndexIDs(i.stackable)
}

func itemIndexIDs(index map[int64]string) []uint32 {
	ids := make([]uint32, 0, len(index))
	for id := range index {
		if id > 0 && id < 1<<32-1 {
			ids = append(ids, uint32(id))
		}
	}
	slices.Sort(ids)
	return ids
}
