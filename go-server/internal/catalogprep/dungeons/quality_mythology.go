package dungeons

import (
	"errors"

	"s9a1/server/internal/catalogprep/commerce"
)

// BuildMythologyResolver shares the ordered native option-index rules with quality boxes.
// Equipment outside the Wisdom pool may have its own valid level-100 mythology profile.
func BuildMythologyResolver(a Archive) (commerce.MythologyResolver, error) {
	index, err := newMythology(a)
	if err != nil {
		return nil, err
	}
	return func(s commerce.ItemSource) (*commerce.QualityMythology, error) {
		opts, hash, err := index.profile(s.Text, s.Hash)
		if errors.Is(err, errMythologyShape) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		p := &commerce.QualityMythology{Hash: hash, Count: byte(len(opts))}
		for _, slot := range opts {
			row := make([]int, len(slot))
			for i, v := range slot {
				row[i] = int(v)
			}
			p.Options = append(p.Options, row)
		}
		return p, nil
	}, nil
}
