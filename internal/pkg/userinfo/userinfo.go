package userinfo

import "go.mau.fi/whatsmeow/types"

// Response keeps every UserInfo key as before and adds a flat businessName,
// so clients do not have to dig through the verified name certificate.
type Response struct {
	types.UserInfo
	BusinessName string `json:"businessName,omitempty"`
}

func New(info types.UserInfo) Response {
	resp := Response{UserInfo: info}
	if vn := info.VerifiedName; vn != nil && vn.Details != nil {
		resp.BusinessName = vn.Details.GetVerifiedName()
	}
	return resp
}
