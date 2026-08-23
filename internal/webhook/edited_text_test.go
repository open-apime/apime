package webhook

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// buildEdit mirrors what `Client.BuildEdit` produces, which is the shape the
// decrypted payload actually has: the content sits under
// editedMessage -> protocolMessage.editedMessage, never at the root.
func buildEdit(inner *waE2E.Message) *waE2E.Message {
	return &waE2E.Message{
		EditedMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{
				ProtocolMessage: &waE2E.ProtocolMessage{
					Key:           &waCommon.MessageKey{ID: proto.String("3AD5F597BABB0A0357A7")},
					Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
					EditedMessage: inner,
				},
			},
		},
	}
}

func TestEditedTextReadsWrappedPlainText(t *testing.T) {
	// The 22/08 report: a plain text edit came through with no text at all,
	// because the old code read GetConversation() off the root.
	edit := buildEdit(&waE2E.Message{Conversation: proto.String("texto corrigido")})

	if got := edit.GetConversation(); got != "" {
		t.Fatalf("root should be empty, this is what fooled the old code: %q", got)
	}
	if got := editedText(edit); got != "texto corrigido" {
		t.Errorf("editedText = %q, want %q", got, "texto corrigido")
	}
}

func TestEditedTextReadsEveryCarrier(t *testing.T) {
	cases := []struct {
		name  string
		inner *waE2E.Message
		want  string
	}{
		{"conversation", &waE2E.Message{Conversation: proto.String("simples")}, "simples"},
		{"extendedText", &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("com link")},
		}, "com link"},
		{"imageCaption", &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{Caption: proto.String("legenda da foto")},
		}, "legenda da foto"},
		{"videoCaption", &waE2E.Message{
			VideoMessage: &waE2E.VideoMessage{Caption: proto.String("legenda do vídeo")},
		}, "legenda do vídeo"},
		{"documentCaption", &waE2E.Message{
			DocumentMessage: &waE2E.DocumentMessage{Caption: proto.String("legenda do arquivo")},
		}, "legenda do arquivo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := editedText(buildEdit(tc.inner)); got != tc.want {
				t.Errorf("editedText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditedTextHandlesExtraWrapper(t *testing.T) {
	// A disappearing chat wraps the edit in an ephemeralMessage on top.
	edit := &waE2E.Message{
		EphemeralMessage: &waE2E.FutureProofMessage{
			Message: buildEdit(&waE2E.Message{Conversation: proto.String("some depois")}),
		},
	}
	if got := editedText(edit); got != "some depois" {
		t.Errorf("editedText = %q, want %q", got, "some depois")
	}
}

func TestEditedTextEmptyWhenNothingToApply(t *testing.T) {
	cases := map[string]*waE2E.Message{
		"nil":            nil,
		"empty":          {},
		"no inner text":  buildEdit(&waE2E.Message{}),
		"unread carrier": buildEdit(&waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}),
	}
	for name, msg := range cases {
		t.Run(name, func(t *testing.T) {
			if got := editedText(msg); got != "" {
				t.Errorf("editedText = %q, want empty", got)
			}
		})
	}
}

func TestUnwrapEditedStopsOnCycle(t *testing.T) {
	// A self referencing payload must not hang the normalizer.
	loop := &waE2E.Message{}
	loop.EditedMessage = &waE2E.FutureProofMessage{Message: loop}
	if got := unwrapEdited(loop); got == nil {
		t.Error("unwrapEdited returned nil instead of bailing out")
	}
}

func TestMessageFieldNamesListsNamesNotValues(t *testing.T) {
	// The diagnostic log must never carry message content.
	names := messageFieldNames(&waE2E.Message{Conversation: proto.String("segredo")})
	if len(names) != 1 || names[0] != "conversation" {
		t.Fatalf("names = %v, want [conversation]", names)
	}
	for _, n := range names {
		if n == "segredo" {
			t.Error("field value leaked into the log")
		}
	}
}
