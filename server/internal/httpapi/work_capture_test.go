package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWorkCaptureNeverParsesPartialOversizedResponses(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	capture := &workCapture{ResponseWriter: context.Writer}
	_, _ = capture.WriteString(`{"data":`)
	_, _ = capture.WriteString(strings.Repeat("x", 1<<20))
	_, _ = capture.WriteString(`{"id":"must-not-be-reinterpreted"}}`)
	if !capture.overflow || capture.body.Len() != 0 {
		t.Fatal("oversized response left a partial metadata document")
	}
}
