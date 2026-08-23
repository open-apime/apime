package handler

import (
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

// Moving the send routes into a sub-group must not change a single path.
func TestSendRoutesKeepTheirPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewMessageHandler(nil, nil).Register(r.Group("/api"))

	var got []string
	for _, route := range r.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)

	want := []string{
		"GET /api/instances/:id/messages",
		"POST /api/instances/:id/messages",
		"POST /api/instances/:id/messages/audio",
		"POST /api/instances/:id/messages/contact",
		"POST /api/instances/:id/messages/document",
		"POST /api/instances/:id/messages/location",
		"POST /api/instances/:id/messages/media",
		"POST /api/instances/:id/messages/text",
	}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("rotas registradas: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rota %d: got %q want %q", i, got[i], want[i])
		}
	}
}
