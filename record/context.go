package record

import "github.com/jinhuang712/gotato/gotato"

// sessionHeader is the Header of an Event about the Session itself.
func sessionHeader(s SegmentStore) gotato.Header {
	return gotato.NewHeader(gotato.SessionID(s.ID()), "", 0)
}
