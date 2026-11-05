package codex

import (
	"reflect"
	"testing"

	adapterpkg "github.com/teamva-ai/teamva/node/provider"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
)

func TestTurnStartParamsCarriesImageInput(t *testing.T) {
	request := adapterpkg.RunRequest{Prompt: "inspect", Attachments: []nodeprotocol.Attachment{{
		MimeType: "image/png", Filename: "capture.png", DataURL: "data:image/png;base64,iVBORw0KGgo=",
	}}}
	got := turnStartParams(request, "thread-1")["input"]
	want := []map[string]any{
		{"type": "text", "text": "inspect"},
		{"type": "image", "url": "data:image/png;base64,iVBORw0KGgo="},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input=%#v want=%#v", got, want)
	}
}
