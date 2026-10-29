package gotato

import "testing"

func TestAMessagePutBackKeepsItsPlaceAmongTheOthers(t *testing.T) {
	q := newControlQueue(4, 4, 4)
	q.add(control{message: Message{ID: "follow"}, source: MessageSourceFollowUp, kind: kindFollowUp})
	q.add(control{message: Message{ID: "steer"}, source: MessageSourceSteer, kind: kindSteer})

	// A Run between Turns takes the Steer and leaves the FollowUp, then fails
	// before it commits the Steer.
	taken := q.take(false)
	if len(taken.items) != 1 || taken.items[0].message.ID != "steer" {
		t.Fatalf("took %+v, want the Steer only", taken.items)
	}
	taken.putBack()

	var got []MessageID
	for _, c := range q.drain(true) {
		got = append(got, c.message.ID)
	}
	if len(got) != 2 || got[0] != "follow" || got[1] != "steer" {
		t.Fatalf("queue order %v, want [follow steer], the order they were accepted", got)
	}
}
