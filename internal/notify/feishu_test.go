package notify

import (
	"context"
	"errors"
	"testing"
)

type stubFeishuConfigProvider struct {
	cfg FeishuCLIConfig
	err error
}

func (p stubFeishuConfigProvider) Parse() (FeishuCLIConfig, error) {
	if p.err != nil {
		return FeishuCLIConfig{}, p.err
	}
	return p.cfg, nil
}

type stubFeishuMessenger struct {
	creatorAppID    string
	creatorOpenID   string
	creatorCalled   bool
	sentReceiveType string
	sentReceiveID   string
	sentCard        map[string]any
	creatorErr      error
	sendErr         error
}

func (m *stubFeishuMessenger) CreatorOpenID(ctx context.Context, appID string) (string, error) {
	m.creatorCalled = true
	m.creatorAppID = appID
	if m.creatorErr != nil {
		return "", m.creatorErr
	}
	return m.creatorOpenID, nil
}

func (m *stubFeishuMessenger) SendCard(ctx context.Context, receiveIDType, receiveID string, card map[string]any) error {
	m.sentReceiveType = receiveIDType
	m.sentReceiveID = receiveID
	m.sentCard = card
	return m.sendErr
}

func TestFeishuSenderSendUsesCLIConfigAndCreator(t *testing.T) {
	provider := stubFeishuConfigProvider{
		cfg: FeishuCLIConfig{
			AppID:     "cli_app",
			AppSecret: "secret",
		},
	}
	messenger := &stubFeishuMessenger{creatorOpenID: "ou_creator"}
	sender := NewFeishuSender(provider, "")
	sender.newMessenger = func(appID, appSecret string) (feishuMessenger, error) {
		if appID != "cli_app" {
			t.Fatalf("appID = %q, want cli_app", appID)
		}
		if appSecret != "secret" {
			t.Fatalf("appSecret = %q, want secret", appSecret)
		}
		return messenger, nil
	}

	msg := Message{Event: "permission_required", SessionID: "session-123", Workspace: "/path/to/project", Title: "Claude Code 等待授权", Body: "项目: demo"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if messenger.creatorAppID != "cli_app" {
		t.Fatalf("creator appID = %q, want cli_app", messenger.creatorAppID)
	}
	if messenger.sentReceiveID != "ou_creator" {
		t.Fatalf("receiveOpenID = %q, want ou_creator", messenger.sentReceiveID)
	}
	if messenger.sentCard == nil {
		t.Fatal("sentCard is nil, want card")
	}
	// Verify card has header with title
	header, ok := messenger.sentCard["header"].(map[string]any)
	if !ok {
		t.Fatal("card header is missing")
	}
	title, ok := header["title"].(map[string]any)
	if !ok {
		t.Fatal("card header title is missing")
	}
	if title["content"] == nil {
		t.Fatal("card header title content is missing")
	}
}

func TestFeishuSenderSendReturnsConfigError(t *testing.T) {
	sender := NewFeishuSender(stubFeishuConfigProvider{err: errors.New("missing config")}, "")

	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send() error = nil, want config error")
	}
	if err.Error() != "missing config" {
		t.Fatalf("Send() error = %v, want missing config", err)
	}
}

func TestFeishuSenderSendUsesConfiguredChatID(t *testing.T) {
	messenger := &stubFeishuMessenger{}
	sender := NewFeishuSender(stubFeishuConfigProvider{cfg: FeishuCLIConfig{AppID: "cli_app", AppSecret: "secret"}}, "oc_chat")
	sender.newMessenger = func(appID, appSecret string) (feishuMessenger, error) {
		return messenger, nil
	}

	if err := sender.Send(context.Background(), Message{Title: "t", Body: "b"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if messenger.creatorCalled {
		t.Fatal("CreatorOpenID should be skipped when chat_id is configured")
	}
	if messenger.sentReceiveType != "chat_id" {
		t.Fatalf("receiveIDType = %q, want chat_id", messenger.sentReceiveType)
	}
	if messenger.sentReceiveID != "oc_chat" {
		t.Fatalf("receiveID = %q, want oc_chat", messenger.sentReceiveID)
	}
}

func TestFeishuSenderSendHintsOnPersonalTenantRestriction(t *testing.T) {
	messenger := &stubFeishuMessenger{
		creatorOpenID: "ou_creator",
		sendErr:       &feishuSendError{Code: 230101, Msg: "Sending messages to users is temporarily unavailable."},
	}
	sender := NewFeishuSender(stubFeishuConfigProvider{cfg: FeishuCLIConfig{AppID: "cli_app", AppSecret: "secret"}}, "")
	sender.newMessenger = func(appID, appSecret string) (feishuMessenger, error) {
		return messenger, nil
	}

	err := sender.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("Send() error = nil, want send error")
	}
	if !contains(err.Error(), "chat_id") {
		t.Fatalf("Send() error = %v, want hint mentioning chat_id", err)
	}
	if !contains(err.Error(), "code=230101") {
		t.Fatalf("Send() error = %v, want original code preserved", err)
	}
}

func TestBuildCardContainsBody(t *testing.T) {
	sender := &FeishuSender{}
	msg := Message{
		Event:     "permission_required",
		Title:     "测试标题",
		Body:      "这是测试消息内容",
		Workspace: "/test/path",
	}

	card := sender.buildCard(msg)

	// 验证 card 结构
	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatal("card elements should be a slice")
	}

	// 查找包含 Body 的元素
	found := false
	for _, el := range elements {
		if elMap, ok := el.(map[string]any); ok {
			if text, ok := elMap["text"].(map[string]any); ok {
				if content, ok := text["content"].(string); ok {
					if contains(content, "这是测试消息内容") {
						found = true
						break
					}
				}
			}
		}
	}

	if !found {
		t.Error("card should contain message body content")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestBuildCardFooterDoesNotHardcodeClaudeCode(t *testing.T) {
	sender := &FeishuSender{}
	card := sender.buildCard(Message{Event: "run_completed", Title: "Codex 运行完成", Body: "done"})

	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatal("card elements should be a slice")
	}

	foundClaudeCode := false
	for _, el := range elements {
		elMap, ok := el.(map[string]any)
		if !ok || elMap["tag"] != "note" {
			continue
		}
		noteElements, ok := elMap["elements"].([]any)
		if !ok {
			continue
		}
		for _, noteEl := range noteElements {
			noteMap, ok := noteEl.(map[string]any)
			if !ok {
				continue
			}
			content, _ := noteMap["content"].(string)
			if contains(content, "Claude Code") {
				foundClaudeCode = true
			}
		}
	}

	if foundClaudeCode {
		t.Fatal("card footer should not hardcode Claude Code")
	}
}

func TestBuildCardOmitsWorkspaceForCodexNotification(t *testing.T) {
	sender := &FeishuSender{}
	card := sender.buildCard(Message{Event: "run_completed", Title: "运行完成", Body: "done", Workspace: "/tmp/project", Agent: "codex"})

	elements, ok := card["elements"].([]any)
	if !ok {
		t.Fatal("card elements should be a slice")
	}

	for _, el := range elements {
		elMap, ok := el.(map[string]any)
		if !ok {
			continue
		}
		text, ok := elMap["text"].(map[string]any)
		if !ok {
			continue
		}
		content, _ := text["content"].(string)
		if contains(content, "**工作目录**") {
			t.Fatalf("card should omit workspace for Codex notification, got %q", content)
		}
	}
}
