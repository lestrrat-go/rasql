package changeplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if err := validateCheckpointShape(data); err != nil {
		return Checkpoint{}, err
	}
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
func validateCheckpointShape(data []byte) error {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return fmt.Errorf("%w: checkpoint object", ErrInvalidWire)
	}
	if err := requireKeys(raw, "plan_id", "next_index", "catalog_digest"); err != nil {
		return err
	}
	for _, key := range []string{"plan_id", "next_index", "catalog_digest"} {
		value := bytes.TrimSpace(raw[key])
		if bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("%w: checkpoint field %s is null", ErrInvalidWire, key)
		}
	}
	var planID, catalogDigest string
	if err := json.Unmarshal(raw["plan_id"], &planID); err != nil || planID == "" {
		return fmt.Errorf("%w: checkpoint plan_id type", ErrInvalidWire)
	}
	if err := json.Unmarshal(raw["catalog_digest"], &catalogDigest); err != nil || catalogDigest == "" {
		return fmt.Errorf("%w: checkpoint catalog_digest type", ErrInvalidWire)
	}
	var next int
	if err := json.Unmarshal(raw["next_index"], &next); err != nil {
		return fmt.Errorf("%w: checkpoint next_index type", ErrInvalidWire)
	}
	return nil
}
