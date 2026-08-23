package webhook

import (
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// How many wrapper layers to peel before giving up. The real chain is
// editedMessage -> protocolMessage.editedMessage, so four leaves room for an
// ephemeral or deviceSent wrapper around it without ever looping forever.
const maxUnwrapDepth = 4

// unwrapEdited peels the wrappers around an edit payload.
//
// `DecryptSecretEncryptedMessage` returns the raw protobuf and never calls
// `UnwrapRaw`, so the new content is NOT at the root: WhatsApp builds an edit as
// editedMessage -> protocolMessage{Key, Type: MESSAGE_EDIT, EditedMessage}, the
// same shape `BuildEdit` produces. Reading `GetConversation()` off the root
// yields "" for every edit, including plain text ones.
func unwrapEdited(msg *waE2E.Message) *waE2E.Message {
	for i := 0; i < maxUnwrapDepth && msg != nil; i++ {
		switch {
		case msg.GetEditedMessage().GetMessage() != nil:
			msg = msg.GetEditedMessage().GetMessage()
		case msg.GetProtocolMessage().GetEditedMessage() != nil:
			msg = msg.GetProtocolMessage().GetEditedMessage()
		case msg.GetDeviceSentMessage().GetMessage() != nil:
			msg = msg.GetDeviceSentMessage().GetMessage()
		case msg.GetEphemeralMessage().GetMessage() != nil:
			msg = msg.GetEphemeralMessage().GetMessage()
		case msg.GetViewOnceMessage().GetMessage() != nil:
			msg = msg.GetViewOnceMessage().GetMessage()
		case msg.GetViewOnceMessageV2().GetMessage() != nil:
			msg = msg.GetViewOnceMessageV2().GetMessage()
		case msg.GetDocumentWithCaptionMessage().GetMessage() != nil:
			msg = msg.GetDocumentWithCaptionMessage().GetMessage()
		default:
			return msg
		}
	}
	return msg
}

// editedText returns the new content of an edit, empty when there is none to apply.
//
// Editing a caption is as common as editing text, and both arrive through this
// same path, so every caption carrier is read too.
func editedText(decrypted *waE2E.Message) string {
	msg := unwrapEdited(decrypted)
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	}
	if text := msg.GetExtendedTextMessage().GetText(); text != "" {
		return text
	}
	if text := msg.GetImageMessage().GetCaption(); text != "" {
		return text
	}
	if text := msg.GetVideoMessage().GetCaption(); text != "" {
		return text
	}
	if text := msg.GetDocumentMessage().GetCaption(); text != "" {
		return text
	}
	return ""
}

// messageFieldNames lists which fields the payload actually carries, for the log
// that fires when an edit decrypts but yields no text. Names only, never values:
// this is message content, and it must not reach the log.
func messageFieldNames(msg *waE2E.Message) []string {
	if msg == nil {
		return nil
	}
	var names []string
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, string(fd.Name()))
		return true
	})
	return names
}
