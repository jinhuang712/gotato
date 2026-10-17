package session

// StartLog turns r's log on, as storing r does.
func StartLog(r *Record) { r.startLog() }

// TakePending returns r's pending Entries and marks them stored.
func TakePending(r *Record) []Entry {
	entries, err := r.takePending()
	if err != nil {
		panic(err)
	}
	if len(entries) > 0 {
		r.stored(entries[len(entries)-1].Seq)
	}
	return entries
}
