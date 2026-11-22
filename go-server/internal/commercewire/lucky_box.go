package commercewire

import (
	"math"

	"s9a1/server/internal/storage"
)

func ReadLuckyBox(b []byte) (storage.LuckyBoxRequest, error) {
	var q storage.LuckyBoxRequest
	if len(b) != 24 {
		return q, storage.ErrInvalid
	}
	q = storage.LuckyBoxRequest{Mode: b[13], Slot: le.Uint16(b[14:]), Template: le.Uint32(b[16:]), Selected: int32(le.Uint32(b[20:]))}
	if q.Slot == 0 || q.Slot == math.MaxUint16 || q.Template == 0 || q.Template > math.MaxInt32 ||
		q.Mode > 1 || q.Selected < 0 || q.Selected > 9 || q.Mode == 0 && q.Selected != 0 {
		return q, storage.ErrInvalid
	}
	return q, nil
}

func LuckyBoxOpened(q storage.LuckyBoxRequest, gauge uint32, rewards []storage.LuckyBoxPayout) ([]byte, error) {
	if gauge > math.MaxInt32 || q.Mode == 0 && len(rewards) != 1 || q.Mode == 1 && len(rewards) != 10 || q.Mode > 1 {
		return nil, storage.ErrInvalid
	}
	b := make([]byte, 6, 194)
	b[0], b[1] = 1, q.Mode
	b = le.AppendUint32(b, uint32(q.Selected))
	b = le.AppendUint32(b, gauge)
	for _, r := range rewards {
		b = append(b, r.BoxType, r.ResultGrade)
		b = le.AppendUint32(b, r.TemplateID)
		b = le.AppendUint32(b, r.Count)
		b = le.AppendUint32(b, r.LimitedTemplateID)
		b = le.AppendUint32(b, r.LimitedCount)
	}
	return append(b, make([]byte, 18*(10-len(rewards)))...), nil
}
