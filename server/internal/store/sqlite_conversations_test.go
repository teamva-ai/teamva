package store

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/teamva-ai/teamva/server/internal/domain"
)

func createConversationFixture(t *testing.T) (sqliteExecutionFixture, domain.ConversationGraphDetail) {
	t.Helper()
	f := newSQLiteExecutionFixture(t)
	detail, err := f.repository.CreateConversationGraph(context.Background(), f.user.ID,
		domain.ConversationGraph{ProjectID: f.project.ID, Title: "探索方案"},
		domain.ConversationNode{AgentID: f.agent.ID, Title: "根对话"},
	)
	if err != nil {
		t.Fatalf("create graph: %v", err)
	}
	if len(detail.Nodes) != 1 || detail.Nodes[0].ParentID != "" {
		t.Fatalf("root node = %+v", detail.Nodes)
	}
	return f, detail
}

func appendConversationMessage(t *testing.T, f sqliteExecutionFixture, nodeID, author, body string) domain.ConversationMessage {
	t.Helper()
	item, err := f.repository.CreateConversationMessage(context.Background(), f.user.ID, nodeID, domain.ConversationMessage{
		AuthorKind: author, Body: body,
	})
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
	return item
}

func TestSQLiteConversationForkRewritesTree(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()
	rootID := detail.Nodes[0].ID

	appendConversationMessage(t, f, rootID, domain.ConversationAuthorUser, "用户第一条")
	firstAgent := appendConversationMessage(t, f, rootID, domain.ConversationAuthorAgent, "Agent 第一条")
	appendConversationMessage(t, f, rootID, domain.ConversationAuthorUser, "用户第二条")
	appendConversationMessage(t, f, rootID, domain.ConversationAuthorAgent, "Agent 第二条")

	forked, err := f.repository.ForkConversationNode(ctx, f.user.ID, rootID, firstAgent.ID,
		domain.ConversationNode{AgentID: f.agent.ID, Title: "新分支"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.ParentID == "" || forked.ParentID == rootID {
		t.Fatalf("forked parent = %q", forked.ParentID)
	}

	updated, err := f.repository.GetConversationGraphDetail(ctx, f.user.ID, detail.Graph.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(updated.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(updated.Nodes))
	}
	byID := map[string]domain.ConversationNode{}
	for _, node := range updated.Nodes {
		byID[node.ID] = node
	}
	forkParent := byID[forked.ParentID]
	if forkParent.ParentID != "" {
		t.Fatalf("fork parent should be root, got parent %q", forkParent.ParentID)
	}
	if byID[rootID].ParentID != forkParent.ID {
		t.Fatalf("source node parent = %q, want %q", byID[rootID].ParentID, forkParent.ID)
	}
	if byID[rootID].MessageCount != 4 || forkParent.MessageCount != 2 || forked.MessageCount != 0 {
		t.Fatalf("message counts root=%d parent=%d fork=%d",
			byID[rootID].MessageCount, forkParent.MessageCount, forked.MessageCount)
	}
	// 完整延续分支保留分叉点前后的所有消息。
	rootMessages, err := f.repository.ListConversationMessages(ctx, f.user.ID, rootID)
	if err != nil {
		t.Fatalf("root messages: %v", err)
	}
	if len(rootMessages) != 4 || rootMessages[3].Body != "Agent 第二条" {
		t.Fatalf("root messages = %+v", rootMessages)
	}
	// 新父节点仅复制截至分叉点的历史。
	parentMessages, err := f.repository.ListConversationMessages(ctx, f.user.ID, forkParent.ID)
	if err != nil {
		t.Fatalf("parent messages: %v", err)
	}
	if len(parentMessages) != 2 || parentMessages[0].Body != "用户第一条" {
		t.Fatalf("parent messages = %+v", parentMessages)
	}
	// 只有新父副本冻结，两条子分支都可继续发送。
	if byID[rootID].Locked || !forkParent.Locked || forked.Locked {
		t.Fatalf("locked root=%v parent=%v fork=%v",
			byID[rootID].Locked, forkParent.Locked, forked.Locked)
	}
	// 原节点与新节点在右列上下对称，新父节点位于两者连线的中央高度。
	if byID[rootID].PositionX != forked.PositionX {
		t.Fatalf("original x=%v fork x=%v", byID[rootID].PositionX, forked.PositionX)
	}
	if !(byID[rootID].PositionY < forkParent.PositionY && forked.PositionY > forkParent.PositionY) {
		t.Fatalf("expected original above and fork below parent: %v / %v / %v",
			byID[rootID].PositionY, forkParent.PositionY, forked.PositionY)
	}
	if math.Abs((byID[rootID].PositionY+forked.PositionY)/2-forkParent.PositionY) > 0.001 {
		t.Fatalf("parent is not centered between original (%v) and fork (%v): parent y=%v",
			byID[rootID].PositionY, forked.PositionY, forkParent.PositionY)
	}
	// 新分支返回冻结的聊天记录快照，供界面展示。
	if len(forked.InheritedMessages) == 0 ||
		forked.InheritedMessages[len(forked.InheritedMessages)-1].Body != "Agent 第一条" {
		t.Fatalf("inherited = %+v", forked.InheritedMessages)
	}
}

func TestSQLiteConversationLockedNodeRejectsSend(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()
	rootID := detail.Nodes[0].ID
	appendConversationMessage(t, f, rootID, domain.ConversationAuthorUser, "问题")
	agentMessage := appendConversationMessage(t, f, rootID, domain.ConversationAuthorAgent, "回答")

	forked, err := f.repository.ForkConversationNode(ctx, f.user.ID, rootID, agentMessage.ID,
		domain.ConversationNode{AgentID: f.agent.ID, Title: "分支"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if _, _, err := f.repository.SendConversationMessage(ctx, f.user.ID, forked.ParentID, "locked-turn", "再问一句"); err == nil {
		t.Fatal("expected sending to a locked node to fail")
	}
	// 节点大小可以通过更新接口持久化。
	resized, err := f.repository.UpdateConversationNode(ctx, f.user.ID, forked.ID, forked.Version,
		func(node *domain.ConversationNode) {
			node.Width = 640
			node.Height = 800
		})
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if resized.Width != 640 || resized.Height != 800 {
		t.Fatalf("resized = %v x %v", resized.Width, resized.Height)
	}
	if _, err := f.repository.UpdateConversationNode(ctx, f.user.ID, forked.ID, resized.Version,
		func(node *domain.ConversationNode) { node.Width = 100 }); err == nil {
		t.Fatal("expected an out-of-range width to be rejected")
	}
}

func TestSQLiteConversationRejectsNonAgentFork(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	rootID := detail.Nodes[0].ID
	userMessage := appendConversationMessage(t, f, rootID, domain.ConversationAuthorUser, "只有用户消息")
	if _, err := f.repository.ForkConversationNode(context.Background(), f.user.ID, rootID, userMessage.ID,
		domain.ConversationNode{AgentID: f.agent.ID}); err == nil {
		t.Fatal("expected fork on a user message to fail")
	}
}

func TestSQLiteConversationDeleteSubtreeAndGraph(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	ctx := context.Background()
	rootID := detail.Nodes[0].ID
	appendConversationMessage(t, f, rootID, domain.ConversationAuthorUser, "一")
	agentMessage := appendConversationMessage(t, f, rootID, domain.ConversationAuthorAgent, "二")
	forked, err := f.repository.ForkConversationNode(ctx, f.user.ID, rootID, agentMessage.ID,
		domain.ConversationNode{AgentID: f.agent.ID, Title: "分支"})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	// 删除分支节点后，树上只剩分叉父节点和原节点。
	if err := f.repository.DeleteConversationNode(ctx, f.user.ID, forked.ID, forked.Version); err != nil {
		t.Fatalf("delete fork: %v", err)
	}
	updated, err := f.repository.GetConversationGraphDetail(ctx, f.user.ID, detail.Graph.ID)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(updated.Nodes) != 2 {
		t.Fatalf("nodes after delete = %d, want 2", len(updated.Nodes))
	}
	// 删除根节点等于删除整张图。
	root := updated.Nodes[0]
	for _, node := range updated.Nodes {
		if node.ParentID == "" {
			root = node
		}
	}
	if err := f.repository.DeleteConversationNode(ctx, f.user.ID, root.ID, root.Version); err != nil {
		t.Fatalf("delete root: %v", err)
	}
	if _, err := f.repository.GetConversationGraph(ctx, f.user.ID, detail.Graph.ID); err == nil {
		t.Fatal("expected graph to be deleted with its root")
	}
}

func TestSQLiteConversationDetailBudgetFailsExplicitly(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	originalNodes, originalMessages, originalBytes := maxConversationNodes, maxConversationMessages, maxConversationMessageBytes
	t.Cleanup(func() {
		maxConversationNodes, maxConversationMessages, maxConversationMessageBytes = originalNodes, originalMessages, originalBytes
	})
	maxConversationMessages = 2
	appendConversationMessage(t, f, detail.Nodes[0].ID, domain.ConversationAuthorUser, "one")
	appendConversationMessage(t, f, detail.Nodes[0].ID, domain.ConversationAuthorAgent, "two")
	appendConversationMessage(t, f, detail.Nodes[0].ID, domain.ConversationAuthorUser, "three")
	_, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, detail.Graph.ID)
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) || domainErr.Code != "conversation_too_large" {
		t.Fatalf("oversized graph read error=%v", err)
	}
	maxConversationMessages = originalMessages
	maxConversationMessageBytes = 4
	if _, err := f.repository.GetConversationGraphDetail(t.Context(), f.user.ID, detail.Graph.ID); err == nil {
		t.Fatal("oversized message bytes were not rejected")
	}
}

func TestSQLiteConversationIsolationBetweenUsers(t *testing.T) {
	f, detail := createConversationFixture(t)
	defer func() { _ = f.repository.Close() }()
	// 另一个用户无法读取或写入这张图。
	if _, err := f.repository.GetConversationGraphDetail(context.Background(), "usr_other", detail.Graph.ID); err == nil {
		t.Fatal("expected cross-user graph read to fail")
	}
	if _, err := f.repository.CreateConversationMessage(context.Background(), "usr_other",
		detail.Nodes[0].ID, domain.ConversationMessage{Body: "x"}); err == nil {
		t.Fatal("expected cross-user message write to fail")
	}
}
