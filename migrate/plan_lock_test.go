package migrate

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanLockHashVectors(t *testing.T) {
	journal := framedPlanHash("rasql/changeplan/journal-id/v1",
		[]byte("1111111111111111111111111111111111111111111111111111111111111111"), []byte("op-1"))
	require.Equal(t, "d77de083b062bcc0a26db21bcc91f6c2db31e5f71a4c9c7373a159b0cd3d248b", journal)

	identity := make([]byte, 16)
	binary.BigEndian.PutUint64(identity[:8], 7)
	binary.BigEndian.PutUint64(identity[8:], 11)
	unixHash := framedPlanHash("rasql/changeplan/sqlite-lock/v1", []byte("unix"), identity,
		[]byte("rasql_schema_migrations"))
	require.Equal(t, "6abd0e09502d611eac34239809931cb7e3fd8fcca1d44181ba26040070eef24a", unixHash)
	windowsHash := framedPlanHash("rasql/changeplan/sqlite-lock/v1", []byte("windows"), identity,
		[]byte("rasql_schema_migrations"))
	require.Equal(t, "40df2be93b1eb6f865b320b895fd9c342bf4e13ab306b5ea4620ae84600f99c2", windowsHash)
}

type testChangePlanLocker struct{ lock ChangePlanLock }

func (l *testChangePlanLocker) Acquire(context.Context, string) (ChangePlanLock, error) {
	return l.lock, nil
}

func TestWithChangePlanLockerCopiesRunner(t *testing.T) {
	var nilLocker *testChangePlanLocker
	_, err := (Runner{}).WithChangePlanLocker(nilLocker)
	require.Error(t, err)
	locker := &testChangePlanLocker{lock: &testChangePlanLock{}}
	configured, err := (Runner{}).WithChangePlanLocker(locker)
	require.NoError(t, err)
	require.Same(t, locker, configured.changePlanLocker)
}

type testChangePlanLock struct{}

func (*testChangePlanLock) Release(context.Context) error { return nil }
