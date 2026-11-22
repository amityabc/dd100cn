package storage

import (
	"context"
	"slices"
)

type inventoryProjectionKey struct{}

// InventoryProjection holds the inventory reads that belong to one serial
// client request. Transaction entry points still own writes and retries.
type InventoryProjection struct {
	Before, Current *Inventory
}

func TrackInventoryProjection(ctx context.Context) (context.Context, *InventoryProjection) {
	p := new(InventoryProjection)
	return context.WithValue(ctx, inventoryProjectionKey{}, p), p
}

func observeInventoryProjection(ctx context.Context, in Inventory) {
	p, _ := ctx.Value(inventoryProjectionKey{}).(*InventoryProjection)
	if p == nil {
		return
	}
	in.Items = slices.Clone(in.Items)
	if p.Before == nil {
		p.Before = &in
	}
	p.Current = &in
}
