package service_test

import (
	"context"
	"testing"
)

// A Run that holds the Session lock may be the one that marked the Session
// (settle runs inside the lock). The repair pass finds the lock busy and must
// keep the mark, not forget the Session.
func TestRepairKeepsTheMarkOfASessionWhoseRunStillHoldsTheLock(t *testing.T) {
	runner, id, hold, done := holdBeforeRelease(t)
	runner.MarkUnreleased(id)
	runner.RepairReleases()
	if !runner.Unreleased(id) {
		t.Fatal("the mark was forgotten while a Run held the Session")
	}
	close(hold.proceed)
	<-done
	t.Cleanup(func() { _ = runner.Close(context.Background()) })
}
