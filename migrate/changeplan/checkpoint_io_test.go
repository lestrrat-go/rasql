package changeplan

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCheckpointAccessorsAndEncoding(t *testing.T) {
	var planID PlanID
	planID[31] = 1
	var catalogDigest Digest
	catalogDigest[0] = 2
	checkpoint, err := NewCheckpoint(planID, 0, catalogDigest)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.PlanID() != planID || checkpoint.NextIndex() != 0 || checkpoint.CatalogDigest() != catalogDigest {
		t.Fatal("checkpoint accessors returned unexpected values")
	}
	want := `{"plan_id":"0000000000000000000000000000000000000000000000000000000000000001","next_index":0,"catalog_digest":"0200000000000000000000000000000000000000000000000000000000000000"}` + "\n"
	encoded, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("encoded checkpoint = %q, want %q", encoded, want)
	}
	decoded, err := DecodeCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != checkpoint {
		t.Fatalf("decoded checkpoint = %#v, want %#v", decoded, checkpoint)
	}
	reencoded, err := EncodeCheckpoint(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatal("checkpoint re-encoding changed bytes")
	}
}

func TestCheckpointDecodeRejectsInvalidWire(t *testing.T) {
	validPlan := strings.Repeat("0", 62) + "ab"
	validCatalog := strings.Repeat("0", 64)
	base := `{"plan_id":"` + validPlan + `","next_index":2,"catalog_digest":"` + validCatalog + `"}`
	tests := []struct {
		name string
		data string
	}{
		{name: "missing plan", data: `{"next_index":2,"catalog_digest":"` + validCatalog + `"}`},
		{name: "missing index", data: `{"plan_id":"` + validPlan + `","catalog_digest":"` + validCatalog + `"}`},
		{name: "missing catalog", data: `{"plan_id":"` + validPlan + `","next_index":2}`},
		{name: "null plan", data: strings.Replace(base, `"`+validPlan+`"`, "null", 1)},
		{name: "null index", data: strings.Replace(base, "2", "null", 1)},
		{name: "null catalog", data: strings.Replace(base, `"`+validCatalog+`"`, "null", 1)},
		{name: "unknown field", data: strings.TrimSuffix(base, "}") + `,"extra":true}`},
		{name: "array", data: "[]"},
		{name: "string", data: `"checkpoint"`},
		{name: "plan type", data: strings.Replace(base, `"`+validPlan+`"`, "1", 1)},
		{name: "index type", data: strings.Replace(base, "2", `"2"`, 1)},
		{name: "catalog type", data: strings.Replace(base, `"`+validCatalog+`"`, "1", 1)},
		{name: "malformed", data: base[:len(base)-1]},
		{name: "short plan digest", data: strings.Replace(base, validPlan, "1", 1)},
		{name: "short catalog digest", data: strings.Replace(base, validCatalog, "1", 1)},
		{name: "uppercase plan digest", data: strings.Replace(base, validPlan, strings.ToUpper(validPlan), 1)},
		{name: "negative index", data: strings.Replace(base, "2", "-1", 1)},
		{name: "trailing json", data: base + "{}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeCheckpoint([]byte(test.data))
			if test.name == "negative index" {
				if !errors.Is(err, ErrInvalidPlan) {
					t.Fatalf("DecodeCheckpoint() error = %v, want ErrInvalidPlan", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidWire) {
				t.Fatalf("DecodeCheckpoint() error = %v, want ErrInvalidWire", err)
			}
		})
	}
	if _, err := DecodeCheckpoint([]byte(`{"plan_id":"` + strings.Repeat("0", 64) + `","next_index":0,"catalog_digest":"` + validCatalog + `"}`)); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("zero plan ID error = %v, want ErrInvalidPlan", err)
	}
}

func TestCheckpointZeroCatalogDigestIsValid(t *testing.T) {
	var planID PlanID
	planID[0] = 1
	checkpoint, err := NewCheckpoint(planID, 3, Digest{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != checkpoint {
		t.Fatalf("decoded checkpoint = %#v, want %#v", decoded, checkpoint)
	}
}

func TestCheckpointFileIO(t *testing.T) {
	var planID PlanID
	planID[0] = 1
	var catalog Digest
	catalog[31] = 3
	checkpoint, err := NewCheckpoint(planID, 4, catalog)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := filepath.Join(dir, "checkpoint.json")
	if err := WriteCheckpoint(name, checkpoint); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("written checkpoint bytes differ")
	}
	assertMode0600(t, name)
	readCheckpoint, err := ReadCheckpoint(name)
	if err != nil {
		t.Fatal(err)
	}
	if readCheckpoint != checkpoint {
		t.Fatalf("ReadCheckpoint() = %#v, want %#v", readCheckpoint, checkpoint)
	}
	assertNoTemporaryFiles(t, dir, ".migration-checkpoint-")

	if err := os.WriteFile(name, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCheckpoint(name, checkpoint); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, encoded) {
		t.Fatalf("replacement checkpoint bytes differ")
	}
	assertMode0600(t, name)

	if err := os.WriteFile(name, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCheckpoint(name, Checkpoint{}); err == nil {
		t.Fatal("WriteCheckpoint(Checkpoint{}) succeeded")
	}
	got, err = os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "preserve" {
		t.Fatalf("invalid checkpoint changed destination to %q", got)
	}
	assertNoTemporaryFiles(t, dir, ".migration-checkpoint-")

	if _, err := ReadCheckpoint(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("ReadCheckpoint(missing) succeeded")
	}
}

func TestCheckpointDigestFixtureUsesHex(t *testing.T) {
	var digest Digest
	digest[0] = 0xab
	if got := DigestHex(digest); got != "ab"+strings.Repeat("0", 62) {
		t.Fatalf("DigestHex() = %q", got)
	}
	decoded, err := hex.DecodeString(DigestHex(digest))
	if err != nil || !reflect.DeepEqual(decoded, digest[:]) {
		t.Fatalf("DigestHex() did not produce a decodable digest")
	}
}
