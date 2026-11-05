package httpapi

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	nodeprotocol "github.com/teamva-ai/teamva/protocol/node"
	"github.com/teamva-ai/teamva/server/internal/domain"
	"github.com/teamva-ai/teamva/server/internal/store"
)

type idleIssueEventsStore struct {
	store.CoreLocalRepository
	reads atomic.Int64
}

func (s *idleIssueEventsStore) ListRunEventPage(ctx context.Context, userID, runID string, after int64, limit, maxBytes int) (domain.RunEventPage, error) {
	s.reads.Add(1)
	return s.CoreLocalRepository.ListRunEventPage(ctx, userID, runID, after, limit, maxBytes)
}

func TestIssueSSEDoesNotReadUnchangedRunHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo, user, issue, _, run := issueHistoryFixture(t)
	counted := &idleIssueEventsStore{CoreLocalRepository: repo}
	wake := make(chan struct{}, 1)
	a := &API{store: counted, runtime: &silentHistoryEvents{wake: wake}}
	router := gin.New()
	router.GET("/:issue_id", func(c *gin.Context) { c.Set(userKey, user); a.streamIssueEvents(c) })
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/"+issue.ID, nil)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			break
		}
	}
	initial := counted.reads.Load()
	if initial == 0 {
		t.Fatal("initial history was not delivered")
	}
	wake <- struct{}{}
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			break
		}
	}
	// Give the wake-triggered flush a chance to finish after its issue.changed frame.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if reads := counted.reads.Load(); reads != initial {
		t.Fatalf("unchanged history queried again: initial=%d now=%d", initial, reads)
	}
	if _, err := repo.ApplyLocalEvents(t.Context(), []nodeprotocol.RunEvent{{
		ID: "late-event", RunID: run.ID, LeaseEpoch: run.LeaseEpoch, Sequence: 1,
		Type: "output.delta", Payload: map[string]any{"text": "late-event-marker"}, OccurredAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	wake <- struct{}{}
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "late-event-marker") {
			return
		}
	}
	t.Fatalf("new events on an existing Run were stranded: %v", scanner.Err())
}
