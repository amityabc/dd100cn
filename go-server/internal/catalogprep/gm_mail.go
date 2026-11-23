package catalogprep

import (
	"context"
	"encoding/json"
	"errors"

	"s9a1/server/internal/catalogprep/commerce"
	"s9a1/server/internal/resources"
)

var ErrGmMailUnsupported = errors.New("PVF item or equipment ID is unknown or unsupported")

// PrepareGMMail prepares the ordinary acquisition template used by mail.
// Online calls also register newly discovered ordinary item behavior.
func PrepareGMMail(ctx context.Context, current *resources.CharacterCatalog, pvfPath, profilePath string, id uint32, online bool) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	row, lookupErr := current.GM.PreparedOrIndexedGrant(id)
	register := online && len(current.Commerce.Templates[id]) == 0
	var items *commerce.Items
	var source commerce.ItemSource
	if lookupErr != nil || register && gmmailOrdinary(row) {
		a, err := openItemPreparation(ctx, current, pvfPath, profilePath)
		if err != nil {
			return nil, err
		}
		defer a.Close()
		items, err = commerce.NewItems(a)
		if err != nil {
			return nil, err
		}
		source, err = items.Source(id)
		if err != nil {
			return nil, err
		}
		if lookupErr != nil {
			row, err = readGMMailGrant(source)
			if err != nil {
				return nil, err
			}
		}
	}
	if !gmmailOrdinary(row) {
		raw, err := current.GM.OrdinaryTemplate(id)
		if err != nil {
			return nil, ErrGmMailUnsupported
		}
		return raw, nil
	}
	next, err := current.GM.WithGrant(id, row)
	if err != nil {
		return nil, err
	}
	com := current.Commerce
	if register {
		additions, err := commerce.RegisterGmMailItem(items, source, row.Template)
		if err != nil {
			return nil, err
		}
		com, err = mergeOwnedCommerce(com, additions)
		if err != nil {
			return nil, err
		}
		next = next.WithOrdinary(com.Templates)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current.GM, current.Commerce = next, com
	return row.Template, nil
}

func gmmailOrdinary(row resources.GmGrant) bool {
	return row.Special == nil && len(row.Template) != 0 && string(row.Template) != "null"
}

func readGMMailGrant(src commerce.ItemSource) (resources.GmGrant, error) {
	var out resources.GmGrant
	item, special, err := src.GM()
	if err != nil || special || item == nil {
		return out, err
	}
	if weight, err := src.Weight(); err == nil {
		out.Weight = &weight
	}
	out.Template, err = json.Marshal(item)
	return out, err
}
