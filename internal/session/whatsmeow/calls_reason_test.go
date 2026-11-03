package whatsmeow

import (
	"testing"

	"github.com/open-apime/apime/internal/voip"
)

func TestEndReason(t *testing.T) {
	cases := []struct {
		name          string
		active, local bool
		raw, hint     string
		want          string
	}{
		{"hint from the phone wins", false, false, "rejected", voip.ReasonRejectedElsewhere, voip.ReasonRejectedElsewhere},
		{"answered on another device", false, false, "accepted_elsewhere", "", voip.ReasonAcceptedElsewhere},
		{"group call closed", false, false, "group_call_ended", "", voip.ReasonGroupCallEnded},
		{"declined by the peer", false, false, "rejected", "", voip.ReasonRejected},
		{"hangup after answer", true, false, "", "", voip.ReasonHangup},
		{"this side gave up", false, true, "", "", voip.ReasonCancelled},
		{"rang until it dropped", false, false, "", "", voip.ReasonMissed},
	}
	for _, c := range cases {
		tc := &trackedCall{active: c.active, localEnd: c.local}
		if got := endReason(tc, c.raw, c.hint); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
