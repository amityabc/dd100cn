package host

import (
	"context"
	"encoding/json"

	"s9a1/server/internal/catalogprep"
	"s9a1/server/internal/resources"
)

func (s *Server) prepareGMItem(ctx context.Context, id uint32) (*resources.CharacterCatalog, error) {
	s.gmPreparation.Lock()
	defer s.gmPreparation.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current := s.itemsCatalog.Load()
	if _, ok := s.gmPrepared[id]; ok {
		return current, nil
	}
	next := *current
	if err := catalogprep.PrepareGMItem(ctx, &next, s.pvfPath, s.profilePath, id); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.DB.BindDailyExpiry(next.Commerce.DailyExpiryTemplates)
	s.itemsCatalog.Store(&next)
	s.gmPrepared[id] = struct{}{}
	return &next, nil
}

func (s *Server) prepareGMMail(ctx context.Context, id uint32) (json.RawMessage, error) {
	s.gmPreparation.Lock()
	defer s.gmPreparation.Unlock()
	current := s.itemsCatalog.Load()
	next := *current
	raw, err := catalogprep.PrepareGMMail(ctx, &next, s.pvfPath, s.profilePath, id, true)
	if err != nil {
		return nil, err
	}
	_, already := current.GM.PreparedGrant(id)
	_, added := next.GM.PreparedGrant(id)
	registered := len(current.Commerce.Templates[id]) == 0 && len(next.Commerce.Templates[id]) != 0
	if !already && added || registered {
		s.DB.BindDailyExpiry(next.Commerce.DailyExpiryTemplates)
		s.itemsCatalog.Store(&next)
	}
	return raw, nil
}
