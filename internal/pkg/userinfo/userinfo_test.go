package userinfo

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waVnameCert"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestNewAddsBusinessNameAndKeepsKeys(t *testing.T) {
	info := types.UserInfo{
		Status: "hi",
		VerifiedName: &types.VerifiedName{
			Details: &waVnameCert.VerifiedNameCertificate_Details{VerifiedName: proto.String("Acme")},
		},
	}
	raw, err := json.Marshal(New(info))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["businessName"] != "Acme" {
		t.Fatalf("businessName = %v", got["businessName"])
	}
	for _, key := range []string{"Status", "VerifiedName", "PictureID", "Devices", "LID"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing key %s", key)
		}
	}
}

func TestNewOmitsBusinessNameWithoutVerifiedName(t *testing.T) {
	raw, _ := json.Marshal(New(types.UserInfo{}))
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if _, ok := got["businessName"]; ok {
		t.Fatal("businessName should be omitted")
	}
}
