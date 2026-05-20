package bridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gitsang/agent-bridge/internal/agent"
)

func TestHandlePromptPassesMessageOutputOptions(t *testing.T) {
	doneCh := make(chan struct{})
	close(doneCh)
	errCh := make(chan error)
	client := &fakeAgentClient{
		promptHandle: agent.NewPromptHandle(doneCh, errCh),
		pollMessages: []*agent.Message{{
			SessionID:   "agent-session",
			Content:     "hello",
			CompletedAt: 1,
		}},
	}
	output := agent.MessageOutputOptions{
		Include: []agent.MessageContentKind{agent.MessageContentAnswer},
	}
	bridge := New(
		WithAgentClient(client),
		WithMessageOutputOptions(output),
	)

	var replies []*Message
	err := bridge.Handle(context.Background(), &Message{Content: "hi", Chat: ChatContext{SessionID: "chat-session"}}, func(msg *Message) error {
		replies = append(replies, msg)
		return nil
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if got, want := len(replies), 1; got != want {
		t.Fatalf("reply count = %d, want %d", got, want)
	}
	if got, want := client.pollOutput.Include[0], agent.MessageContentAnswer; got != want {
		t.Fatalf("PollMessagesAfter() include[0] = %q, want %q", got, want)
	}
}

func TestAdvanceCompletedCursorKeepsNewestCompletedResult(t *testing.T) {
	after := float64(5)

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 0})
	if got, want := after, float64(5); got != want {
		t.Fatalf("advanceCompletedCursor() with unfinished result = %v, want %v", got, want)
	}

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 4})
	if got, want := after, float64(5); got != want {
		t.Fatalf("advanceCompletedCursor() with older result = %v, want %v", got, want)
	}

	after = advanceCompletedCursor(after, &agent.Message{CompletedAt: 7})
	if got, want := after, float64(7); got != want {
		t.Fatalf("advanceCompletedCursor() with newer result = %v, want %v", got, want)
	}
}

func TestHandlePermissionCommandAutoTargetsSinglePendingRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingPermissions: []agent.PermissionRequest{{ID: "perm-1", SessionID: "s1", Permission: "edit"}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/permission once")

	if got, want := client.replyPermissionSessionID, "s1"; got != want {
		t.Fatalf("ReplyPermission() session = %q, want %q", got, want)
	}
	if got, want := client.replyPermissionRequestID, "perm-1"; got != want {
		t.Fatalf("ReplyPermission() request = %q, want %q", got, want)
	}
	if got, want := client.replyPermissionReply, agent.PermissionReplyOnce; got != want {
		t.Fatalf("ReplyPermission() reply = %q, want %q", got, want)
	}
	if got := replies[0].Content; !strings.Contains(got, "Permission request perm-1 replied with once") {
		t.Fatalf("reply content = %q, want permission success", got)
	}
}

func TestHandlePermissionCommandRequiresTargetWhenMultiplePending(t *testing.T) {
	client := &fakeAgentClient{
		pendingPermissions: []agent.PermissionRequest{
			{ID: "perm-1", SessionID: "s1", Permission: "edit"},
			{ID: "perm-2", SessionID: "s1", Permission: "bash"},
		},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/permission once")

	if client.replyPermissionCalls != 0 {
		t.Fatalf("ReplyPermission() calls = %d, want 0", client.replyPermissionCalls)
	}
	if got := replies[0].Content; !strings.Contains(got, "Multiple pending permission requests") || !strings.Contains(got, "perm-2") {
		t.Fatalf("reply content = %q, want multiple pending list", got)
	}
}

func TestHandlePermissionCommandTargetsIndex(t *testing.T) {
	client := &fakeAgentClient{
		pendingPermissions: []agent.PermissionRequest{
			{ID: "perm-1", SessionID: "s1", Permission: "edit"},
			{ID: "perm-2", SessionID: "s1", Permission: "bash"},
		},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/permission reject 2")

	if got, want := client.replyPermissionRequestID, "perm-2"; got != want {
		t.Fatalf("ReplyPermission() request = %q, want %q", got, want)
	}
	if got, want := client.replyPermissionReply, agent.PermissionReplyReject; got != want {
		t.Fatalf("ReplyPermission() reply = %q, want %q", got, want)
	}
}

func TestHandlePermissionCommandTargetsID(t *testing.T) {
	client := &fakeAgentClient{
		pendingPermissions: []agent.PermissionRequest{
			{ID: "perm-1", SessionID: "s1", Permission: "edit"},
			{ID: "perm-2", SessionID: "s1", Permission: "bash"},
		},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/permission reject perm-2")

	if got, want := client.replyPermissionRequestID, "perm-2"; got != want {
		t.Fatalf("ReplyPermission() request = %q, want %q", got, want)
	}
}

func TestHandlePermissionCommandReportsStaleRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingPermissions: []agent.PermissionRequest{{ID: "perm-1", SessionID: "s1", Permission: "edit"}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/permission once missing")

	if client.replyPermissionCalls != 0 {
		t.Fatalf("ReplyPermission() calls = %d, want 0", client.replyPermissionCalls)
	}
	if got := replies[0].Content; !strings.Contains(got, "Permission request no longer pending: missing") {
		t.Fatalf("reply content = %q, want stale request message", got)
	}
}

func TestHandleQuestionCommandAutoTargetsSinglePendingRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "question-1",
			SessionID: "s1",
			Questions: []agent.Question{{Text: "Environment?", Options: []string{"staging", "production"}}},
		}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/question production")

	if got, want := client.replyQuestionSessionID, "s1"; got != want {
		t.Fatalf("ReplyQuestion() session = %q, want %q", got, want)
	}
	if got, want := client.replyQuestionRequestID, "question-1"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "production" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[production]]", client.replyQuestionAnswers)
	}
	if got := replies[0].Content; !strings.Contains(got, "Question request question-1 answered") {
		t.Fatalf("reply content = %q, want question success", got)
	}
}

func TestHandleQuestionCommandAcceptsExplicitIDForSinglePendingRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "question-1",
			SessionID: "s1",
			Questions: []agent.Question{{Text: "Environment?", Options: []string{"staging", "production"}}},
		}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/question question-1 2")

	if got, want := client.replyQuestionRequestID, "question-1"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "production" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[production]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionCommandAcceptsExplicitIDWithoutConversationBinding(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "que_e3fc14a980019F6xw5McDKtETi",
			SessionID: "s1",
			Questions: []agent.Question{{Text: "Continue?", Options: []string{"Yes", "No"}}},
		}},
	}
	bridge := New(WithAgentClient(client))

	replies := collectReplies(t, bridge, "/question que_e3fc14a980019F6xw5McDKtETi 1")

	if got, want := client.listPendingQuestionsSessionID, ""; got != want {
		t.Fatalf("ListPendingQuestions() session = %q, want global query", got)
	}
	if got, want := client.replyQuestionSessionID, "s1"; got != want {
		t.Fatalf("ReplyQuestion() session = %q, want %q", got, want)
	}
	if got, want := client.replyQuestionRequestID, "que_e3fc14a980019F6xw5McDKtETi"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "Yes" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[Yes]]", client.replyQuestionAnswers)
	}
	if got := replies[0].Content; !strings.Contains(got, "Question request que_e3fc14a980019F6xw5McDKtETi answered") {
		t.Fatalf("reply content = %q, want question success", got)
	}
}

func TestHandleQuestionCommandMapsOptionIndexForSinglePendingRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "question-1",
			SessionID: "s1",
			Questions: []agent.Question{{Text: "Environment?", Options: []string{"staging", "production"}}},
		}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/question 2")

	if got, want := client.replyQuestionRequestID, "question-1"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "production" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[production]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionCommandKeepsFreeTextAnswerTogether(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "question-1",
			SessionID: "s1",
			Questions: []agent.Question{{Text: "Reason?"}},
		}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/question deploy after approval")

	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "deploy after approval" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[deploy after approval]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionRejectAutoTargetsSinglePendingRequest(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{{ID: "question-1", SessionID: "s1", Questions: []agent.Question{{Text: "Continue?"}}}},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/question reject")

	if got, want := client.rejectQuestionSessionID, "s1"; got != want {
		t.Fatalf("RejectQuestion() session = %q, want %q", got, want)
	}
	if got, want := client.rejectQuestionRequestID, "question-1"; got != want {
		t.Fatalf("RejectQuestion() request = %q, want %q", got, want)
	}
	if got := replies[0].Content; !strings.Contains(got, "Question request question-1 rejected") {
		t.Fatalf("reply content = %q, want question reject success", got)
	}
}

func TestHandleQuestionCommandRequiresTargetWhenMultiplePending(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{
			{ID: "question-1", SessionID: "s1", Questions: []agent.Question{{Text: "Environment?"}}},
			{ID: "question-2", SessionID: "s1", Questions: []agent.Question{{Text: "Region?"}}},
		},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/question production")

	if client.replyQuestionCalls != 0 {
		t.Fatalf("ReplyQuestion() calls = %d, want 0", client.replyQuestionCalls)
	}
	if got := replies[0].Content; !strings.Contains(got, "Multiple pending question requests") || !strings.Contains(got, "question-2") {
		t.Fatalf("reply content = %q, want multiple pending list", got)
	}
}

func TestHandleQuestionCommandTargetsIDWhenMultiplePending(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{
			{ID: "question-1", SessionID: "s1", Questions: []agent.Question{{Text: "Environment?", Options: []string{"staging", "production"}}}},
			{ID: "question-2", SessionID: "s1", Questions: []agent.Question{{Text: "Region?", Options: []string{"us", "eu"}}}},
		},
	}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	collectReplies(t, bridge, "/question question-2 2")

	if got, want := client.replyQuestionRequestID, "question-2"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "eu" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[eu]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionCommandTargetsQueIDWhenMultiplePendingWithoutBinding(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{
			{ID: "que_e3fc14a980019F6xw5McDKtETi", SessionID: "s1", Questions: []agent.Question{{Text: "如果今天只能选一种补给，你会选哪一个？", Options: []string{"第一", "第二"}}}},
			{ID: "que_e433cceeb001KUfhw7pLIFIWJ5", SessionID: "s2", Questions: []agent.Question{{Text: "如果今天只能做一件让自己更轻松的事，你会选哪一种？", Options: []string{"休息", "散步"}}}},
		},
	}
	bridge := New(WithAgentClient(client))

	collectReplies(t, bridge, "/question que_e433cceeb001KUfhw7pLIFIWJ5 1")

	if got, want := client.replyQuestionSessionID, "s2"; got != want {
		t.Fatalf("ReplyQuestion() session = %q, want %q", got, want)
	}
	if got, want := client.replyQuestionRequestID, "que_e433cceeb001KUfhw7pLIFIWJ5"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "休息" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[休息]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionCommandTargetsQueIDWithInvisibleFormatting(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{
			{ID: "que_e3fc14a980019F6xw5McDKtETi", SessionID: "s1", Questions: []agent.Question{{Text: "First?", Options: []string{"One"}}}},
			{ID: "que_e433cceeb001KUfhw7pLIFIWJ5", SessionID: "s2", Questions: []agent.Question{{Text: "Second?", Options: []string{"Two"}}}},
		},
	}
	bridge := New(WithAgentClient(client))

	collectReplies(t, bridge, "/question que_e433cceeb001​KUfhw7pLIFIWJ5 1")

	if got, want := client.replyQuestionRequestID, "que_e433cceeb001KUfhw7pLIFIWJ5"; got != want {
		t.Fatalf("ReplyQuestion() request = %q, want %q", got, want)
	}
	if len(client.replyQuestionAnswers) != 1 || len(client.replyQuestionAnswers[0]) != 1 || client.replyQuestionAnswers[0][0] != "Two" {
		t.Fatalf("ReplyQuestion() answers = %#v, want [[Two]]", client.replyQuestionAnswers)
	}
}

func TestHandleQuestionCommandExplicitUnknownQueIDDoesNotShowMultiplePending(t *testing.T) {
	client := &fakeAgentClient{
		pendingQuestions: []agent.QuestionRequest{
			{ID: "que_one", SessionID: "s1", Questions: []agent.Question{{Text: "First?"}}},
			{ID: "que_two", SessionID: "s2", Questions: []agent.Question{{Text: "Second?"}}},
		},
	}
	bridge := New(WithAgentClient(client))

	replies := collectReplies(t, bridge, "/question que_missing 1")

	if client.replyQuestionCalls != 0 {
		t.Fatalf("ReplyQuestion() calls = %d, want 0", client.replyQuestionCalls)
	}
	if got := replies[0].Content; !strings.Contains(got, "Question request no longer pending: que_missing") || strings.Contains(got, "Multiple pending") {
		t.Fatalf("reply content = %q, want stale explicit id message", got)
	}
}

func TestHandleQuestionCommandReportsNoPendingRequests(t *testing.T) {
	client := &fakeAgentClient{}
	bridge := New(WithAgentClient(client))
	bridge.conversationStore.PutBinding("chat-session", "s1")

	replies := collectReplies(t, bridge, "/question production")

	if client.replyQuestionCalls != 0 {
		t.Fatalf("ReplyQuestion() calls = %d, want 0", client.replyQuestionCalls)
	}
	if got := replies[0].Content; !strings.Contains(got, "No pending question requests") {
		t.Fatalf("reply content = %q, want no pending message", got)
	}
}

func TestHandlePromptForwardsPendingPermissionBeforeAssistantReply(t *testing.T) {
	doneCh := make(chan struct{})
	errCh := make(chan error)
	client := &fakeAgentClient{
		promptHandle: agent.NewPromptHandle(doneCh, errCh),
		pollMessages: []*agent.Message{{
			SessionID:   "agent-session",
			Content:     "assistant reply",
			CompletedAt: 1,
		}},
		pendingPermissions: []agent.PermissionRequest{{ID: "perm-1", SessionID: "agent-session", Permission: "edit", Patterns: []string{"main.go"}}},
	}
	bridge := New(WithAgentClient(client))

	replies := runPromptWithDelayedDone(t, bridge, doneCh)

	if got, want := len(replies), 2; got != want {
		t.Fatalf("reply count = %d, want %d", got, want)
	}
	if got := replies[0].Content; !strings.Contains(got, "Permission request 1") || !strings.Contains(got, "perm-1") || !strings.Contains(got, "/permission once perm-1") {
		t.Fatalf("first reply = %q, want permission request", got)
	}
	if got, want := replies[1].Content, "assistant reply"; got != want {
		t.Fatalf("second reply = %q, want %q", got, want)
	}
}

func TestHandlePromptForwardsPendingQuestionBeforeAssistantReply(t *testing.T) {
	doneCh := make(chan struct{})
	errCh := make(chan error)
	client := &fakeAgentClient{
		promptHandle: agent.NewPromptHandle(doneCh, errCh),
		pollMessages: []*agent.Message{{
			SessionID:   "agent-session",
			Content:     "assistant reply",
			CompletedAt: 1,
		}},
		pendingQuestions: []agent.QuestionRequest{{
			ID:        "question-1",
			SessionID: "agent-session",
			Questions: []agent.Question{{Text: "Environment?", Options: []string{"staging", "production"}}},
		}},
	}
	bridge := New(WithAgentClient(client))

	replies := runPromptWithDelayedDone(t, bridge, doneCh)

	if got, want := len(replies), 2; got != want {
		t.Fatalf("reply count = %d, want %d", got, want)
	}
	if got := replies[0].Content; !strings.Contains(got, "Question request 1") || !strings.Contains(got, "Environment?") || !strings.Contains(got, "production") || !strings.Contains(got, "/question question-1 2") {
		t.Fatalf("first reply = %q, want question request", got)
	}
	if got, want := replies[1].Content, "assistant reply"; got != want {
		t.Fatalf("second reply = %q, want %q", got, want)
	}
}

func TestHandlePromptDoesNotRepeatSamePendingInteraction(t *testing.T) {
	doneCh := make(chan struct{})
	errCh := make(chan error)
	client := &fakeAgentClient{
		promptHandle: agent.NewPromptHandle(doneCh, errCh),
		pollMessages: []*agent.Message{{
			SessionID:   "agent-session",
			Content:     "assistant reply",
			CompletedAt: 1,
		}},
		pendingPermissions: []agent.PermissionRequest{{ID: "perm-1", SessionID: "agent-session", Permission: "edit"}},
	}
	bridge := New(WithAgentClient(client))

	var replies []*Message
	errChResult := make(chan error, 1)
	go func() {
		errChResult <- bridge.Handle(context.Background(), &Message{Content: "hi", Chat: ChatContext{SessionID: "chat-session"}}, func(msg *Message) error {
			replies = append(replies, msg)
			return nil
		})
	}()

	time.Sleep(4500 * time.Millisecond)
	close(doneCh)
	select {
	case err := <-errChResult:
		if err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Handle()")
	}

	permissionReplies := 0
	for _, reply := range replies {
		if strings.Contains(reply.Content, "Permission request") {
			permissionReplies++
		}
	}
	if got, want := permissionReplies, 1; got != want {
		t.Fatalf("permission reply count = %d, want %d", got, want)
	}
}

func TestHelpTextIncludesInteractionCommands(t *testing.T) {
	bridge := New(WithAgentClient(&fakeAgentClient{}))

	replies := collectReplies(t, bridge, "/help")
	if got := replies[0].Content; !strings.Contains(got, "/permission <once|always|reject> [id|index]") || !strings.Contains(got, "/question [id|index] <answer...>") {
		t.Fatalf("/help = %q, want interaction commands", got)
	}

	replies = collectReplies(t, bridge, "/help permission")
	if got := replies[0].Content; !strings.Contains(got, "Usage: /permission <once|always|reject> [id|index]") {
		t.Fatalf("/help permission = %q, want permission usage", got)
	}

	replies = collectReplies(t, bridge, "/help question")
	if got := replies[0].Content; !strings.Contains(got, "Usage: /question [id|index] <answer...>") {
		t.Fatalf("/help question = %q, want question usage", got)
	}
}

func collectReplies(t *testing.T, bridge *AgentBridge, content string) []*Message {
	t.Helper()
	var replies []*Message
	err := bridge.Handle(context.Background(), &Message{Content: content, Chat: ChatContext{SessionID: "chat-session"}}, func(msg *Message) error {
		replies = append(replies, msg)
		return nil
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(replies) == 0 {
		t.Fatalf("reply count = 0, want at least 1")
	}
	return replies
}

func runPromptWithDelayedDone(t *testing.T, bridge *AgentBridge, doneCh chan struct{}) []*Message {
	t.Helper()
	var replies []*Message
	errCh := make(chan error, 1)
	go func() {
		errCh <- bridge.Handle(context.Background(), &Message{Content: "hi", Chat: ChatContext{SessionID: "chat-session"}}, func(msg *Message) error {
			replies = append(replies, msg)
			return nil
		})
	}()

	time.Sleep(2200 * time.Millisecond)
	close(doneCh)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Handle() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Handle()")
	}
	return replies
}

type fakeAgentClient struct {
	promptHandle       *agent.PromptHandle
	pollMessages       []*agent.Message
	pollMessagesSent   bool
	pollOutput         agent.MessageOutputOptions
	pendingPermissions []agent.PermissionRequest
	pendingQuestions   []agent.QuestionRequest

	listPendingQuestionsSessionID string

	replyPermissionCalls     int
	replyPermissionSessionID string
	replyPermissionRequestID string
	replyPermissionReply     agent.PermissionReply

	replyQuestionCalls     int
	replyQuestionSessionID string
	replyQuestionRequestID string
	replyQuestionAnswers   [][]string

	rejectQuestionCalls     int
	rejectQuestionSessionID string
	rejectQuestionRequestID string
}

func (c *fakeAgentClient) ListModels(context.Context, string) ([]agent.ModelInfo, error) {
	return nil, nil
}

func (c *fakeAgentClient) ResolveModel(context.Context, string, string) (agent.ModelRef, error) {
	return agent.ModelRef{}, nil
}

func (c *fakeAgentClient) ListAgents(context.Context, string) ([]agent.AgentInfo, error) {
	return nil, nil
}

func (c *fakeAgentClient) ListSessions(context.Context, string) ([]agent.Session, error) {
	return nil, nil
}

func (c *fakeAgentClient) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, nil
}

func (c *fakeAgentClient) CreateSession(context.Context, agent.CreateSessionRequest) (*agent.Session, error) {
	return &agent.Session{ID: "agent-session"}, nil
}

func (c *fakeAgentClient) GetMessages(context.Context, string) ([]agent.Message, error) {
	return nil, nil
}

func (c *fakeAgentClient) GetLatestAssistantMessage(context.Context, string) (*agent.Message, error) {
	return nil, nil
}

func (c *fakeAgentClient) Prompt(context.Context, string, string, ...agent.PromptOptionFunc) (*agent.PromptHandle, error) {
	return c.promptHandle, nil
}

func (c *fakeAgentClient) PollMessagesAfter(_ context.Context, _ string, _ float64, output agent.MessageOutputOptions) ([]*agent.Message, error) {
	c.pollOutput = output
	if c.pollMessagesSent {
		return nil, nil
	}
	c.pollMessagesSent = true
	return c.pollMessages, nil
}

func (c *fakeAgentClient) ListPendingPermissions(context.Context, string) ([]agent.PermissionRequest, error) {
	return c.pendingPermissions, nil
}

func (c *fakeAgentClient) ReplyPermission(_ context.Context, sessionID string, requestID string, reply agent.PermissionReply) error {
	c.replyPermissionCalls++
	c.replyPermissionSessionID = sessionID
	c.replyPermissionRequestID = requestID
	c.replyPermissionReply = reply
	return nil
}

func (c *fakeAgentClient) ListPendingQuestions(_ context.Context, sessionID string) ([]agent.QuestionRequest, error) {
	c.listPendingQuestionsSessionID = sessionID
	return c.pendingQuestions, nil
}

func (c *fakeAgentClient) ReplyQuestion(_ context.Context, sessionID string, requestID string, answers [][]string) error {
	c.replyQuestionCalls++
	c.replyQuestionSessionID = sessionID
	c.replyQuestionRequestID = requestID
	c.replyQuestionAnswers = answers
	return nil
}

func (c *fakeAgentClient) RejectQuestion(_ context.Context, sessionID string, requestID string) error {
	c.rejectQuestionCalls++
	c.rejectQuestionSessionID = sessionID
	c.rejectQuestionRequestID = requestID
	return nil
}
