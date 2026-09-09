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
// validateCheckpointShape rejects what the struct decode right after it would otherwise accept
// silently: a missing key or an explicit JSON null, both of which decode to a zero value with no
// error. Everything else - wrong JSON type, an empty or malformed plan_id/catalog_digest - is
// already reported by that struct decode and by parseDigest, so it is not repeated here.
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
		if bytes.Equal(bytes.TrimSpace(raw[key]), []byte("null")) {
			return fmt.Errorf("%w: checkpoint field %s is null", ErrInvalidWire, key)
		}
	}
	return nil
}
