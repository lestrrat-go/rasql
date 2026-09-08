package changeplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Checkpoint struct {
	planID        PlanID
	nextIndex     int
	catalogDigest Digest
}

func NewCheckpoint(planID PlanID, nextIndex int, catalogDigest Digest) (Checkpoint, error) {
	if planID == (PlanID{}) || nextIndex < 0 {
		return Checkpoint{}, fmt.Errorf("%w: invalid checkpoint", ErrInvalidPlan)
	}
	return Checkpoint{planID: planID, nextIndex: nextIndex, catalogDigest: catalogDigest}, nil
}
func (c Checkpoint) PlanID() PlanID        { return c.planID }
func (c Checkpoint) NextIndex() int        { return c.nextIndex }
func (c Checkpoint) CatalogDigest() Digest { return c.catalogDigest }

type checkpointWire struct {
	PlanID        string `json:"plan_id"`
	NextIndex     int    `json:"next_index"`
	CatalogDigest string `json:"catalog_digest"`
}

func EncodeCheckpoint(c Checkpoint) ([]byte, error) {
	if c.planID == (PlanID{}) || c.nextIndex < 0 {
		return nil, fmt.Errorf("%w: invalid checkpoint", ErrInvalidPlan)
	}
	b, err := json.Marshal(checkpointWire{digestHex(Digest(c.planID)), c.nextIndex, digestHex(c.catalogDigest)})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
func DecodeCheckpoint(data []byte) (Checkpoint, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var w checkpointWire
	if err := d.Decode(&w); err != nil {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint: %v", ErrInvalidWire, err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Checkpoint{}, fmt.Errorf("%w: checkpoint trailing JSON", ErrInvalidWire)
	}
	p, err := parseDigest(w.PlanID)
	if err != nil {
		return Checkpoint{}, err
	}
	c, err := parseDigest(w.CatalogDigest)
	if err != nil {
		return Checkpoint{}, err
	}
	return NewCheckpoint(PlanID(p), w.NextIndex, c)
}
func ReadCheckpoint(name string) (Checkpoint, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Checkpoint{}, err
	}
	return DecodeCheckpoint(b)
}
func WriteCheckpoint(name string, c Checkpoint) error {
	b, err := EncodeCheckpoint(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".migration-checkpoint-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
