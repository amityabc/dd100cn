package host

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"testing"
)

type gmAPCTestArchive map[string]string

func (a gmAPCTestArchive) ReadIndex(category string, budget ...int) (map[int64]string, error) {
	if category != "aicharacter" || len(budget) != 1 || budget[0] != 1<<20 {
		return nil, fmt.Errorf("unexpected index request")
	}
	return map[int64]string{1: "valid.aic", 2: "empty.aic", 3: "missing.aic"}, nil
}

func (a gmAPCTestArchive) ReadText(path string, _ int) (string, error) {
	if text, ok := a[path]; ok {
		return text, nil
	}
	return "", os.ErrNotExist
}

func (a gmAPCTestArchive) Read(path string, budget int) ([]byte, error) {
	text, err := a.ReadText(path, budget)
	return []byte(text), err
}

func (a gmAPCTestArchive) ResolveString(int32) (string, error) { return "", os.ErrNotExist }

func TestGameMasterAPCSourceAndItemCount(t *testing.T) {
	if request, ok := gameMasterRequest("//item 3 4294967295"); !ok || request.Count != math.MaxUint32 {
		t.Fatalf("item count = %+v, %t", request, ok)
	}
	if _, ok := gameMasterRequest("//item 4294967295"); ok {
		t.Fatal("reserved ID accepted")
	}
	a := gmAPCTestArchive{"valid.aic": "[name]\nHero", "empty.aic": "\t "}
	if hash, err := gameMasterAPCSource(a, 1); err != nil || hash != fmt.Sprintf("%X", sha256.Sum256([]byte(a["valid.aic"]))) {
		t.Fatalf("APC hash = %s, %v", hash, err)
	}
	for id, want := range map[uint32]string{2: "APC资源为空", 3: "PVF resource not found", 4: "APC编号不存在，请使用aicharacter资源编号"} {
		if _, err := gameMasterAPCSource(a, id); err == nil || err.Error() != want {
			t.Errorf("APC %d error = %v, want %q", id, err, want)
		}
	}
}
