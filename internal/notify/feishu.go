package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hellolib/agent-notify/internal/feishucli"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type FeishuCLIConfig struct {
	AppID     string
	AppSecret string
}

type feishuConfigProvider interface {
	Parse() (FeishuCLIConfig, error)
}

type clientToolsConfigProvider struct{}

func (clientToolsConfigProvider) Parse() (FeishuCLIConfig, error) {
	cfg, err := feishucli.ParseConfig()
	if err != nil {
		return FeishuCLIConfig{}, err
	}
	return FeishuCLIConfig{
		AppID:     cfg.AppID,
		AppSecret: cfg.AppSecret,
	}, nil
}

type feishuMessenger interface {
	CreatorOpenID(ctx context.Context, appID string) (string, error)
	SendCard(ctx context.Context, receiveIDType, receiveID string, card map[string]any) error
}

type sdkFeishuMessenger struct {
	client *lark.Client
}

type FeishuSender struct {
	provider     feishuConfigProvider
	chatID       string
	newMessenger func(appID, appSecret string) (feishuMessenger, error)
}

func NewFeishuSender(provider feishuConfigProvider, chatID string) *FeishuSender {
	return &FeishuSender{
		provider:     provider,
		chatID:       strings.TrimSpace(chatID),
		newMessenger: newSDKFeishuMessenger,
	}
}

func NewDefaultFeishuSender(chatID string) *FeishuSender {
	return NewFeishuSender(clientToolsConfigProvider{}, chatID)
}

func (s *FeishuSender) Name() string { return "feishu" }

func (s *FeishuSender) Send(ctx context.Context, msg Message) error {
	cfg, err := s.provider.Parse()
	if err != nil {
		return err
	}

	messenger, err := s.newMessenger(cfg.AppID, cfg.AppSecret)
	if err != nil {
		return err
	}

	card := s.buildCard(msg)

	if s.chatID != "" {
		return messenger.SendCard(ctx, "chat_id", s.chatID, card)
	}

	creatorOpenID, err := messenger.CreatorOpenID(ctx, cfg.AppID)
	if err != nil {
		return err
	}
	if err := messenger.SendCard(ctx, "open_id", creatorOpenID, card); err != nil {
		return hintFeishuSendError(err)
	}
	return nil
}

// buildCard creates a rich interactive card for Feishu notification
func (s *FeishuSender) buildCard(msg Message) map[string]any {
	// Event emoji mapping
	eventEmoji := map[string]string{
		"permission_required": "🔐",
		"input_required":      "⌨️",
		"run_completed":       "✅",
		"run_failed":          "❌",
	}
	emoji := eventEmoji[msg.Event]
	if emoji == "" {
		emoji = "🔔"
	}

	// Event type mapping for display
	eventType := map[string]string{
		"permission_required": "等待授权",
		"input_required":      "等待输入",
		"run_completed":       "运行完成",
		"run_failed":          "运行失败",
	}
	eventName := eventType[msg.Event]
	if eventName == "" {
		eventName = msg.Event
	}

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	isCodex := msg.Agent == "codex"
	footerText := "🤖 Agent Notify"
	if isCodex {
		footerText = "🤖 Codex Agent Notify"
	}

	elements := []any{
		map[string]any{
			"tag": "div",
			"fields": []any{
				map[string]any{
					"is_short": true,
					"text": map[string]any{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**事件类型**\n%s", eventName),
					},
				},
				map[string]any{
					"is_short": true,
					"text": map[string]any{
						"tag":     "lark_md",
						"content": fmt.Sprintf("**时间**\n%s", timestamp),
					},
				},
			},
		},
		map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**消息内容**\n%s", msg.Body),
			},
		},
	}
	if msg.Workspace != "" && !isCodex {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**工作目录**\n`%s`", msg.Workspace),
			},
		})
	}
	elements = append(elements,
		map[string]any{
			"tag": "hr",
		},
		map[string]any{
			"tag": "note",
			"elements": []any{
				map[string]any{
					"tag":     "plain_text",
					"content": footerText,
				},
			},
		},
	)

	return map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": fmt.Sprintf("%s %s", emoji, msg.Title),
			},
			"template": s.getHeaderColor(msg.Event),
		},
		"elements": elements,
	}
}

// getHeaderColor returns the header color based on event type
func (s *FeishuSender) getHeaderColor(event string) string {
	switch event {
	case "permission_required":
		return "orange"
	case "input_required":
		return "blue"
	case "run_completed":
		return "green"
	case "run_failed":
		return "red"
	default:
		return "turquoise"
	}
}

func newSDKFeishuMessenger(appID, appSecret string) (feishuMessenger, error) {
	if appID == "" || appSecret == "" {
		return nil, errors.New("feishu app_id or app_secret is empty")
	}
	return &sdkFeishuMessenger{client: lark.NewClient(appID, appSecret)}, nil
}

func (m *sdkFeishuMessenger) CreatorOpenID(ctx context.Context, appID string) (string, error) {
	req := larkapplication.NewGetApplicationReqBuilder().
		AppId(appID).
		Lang("zh_cn").
		UserIdType("open_id").
		Build()

	resp, err := m.client.Application.V6.Application.Get(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu get application failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.App == nil || resp.Data.App.CreatorId == nil || *resp.Data.App.CreatorId == "" {
		return "", errors.New("feishu application creator open_id is empty")
	}

	return *resp.Data.App.CreatorId, nil
}

func (m *sdkFeishuMessenger) SendCard(ctx context.Context, receiveIDType, receiveID string, card map[string]any) error {
	content, err := json.Marshal(card)
	if err != nil {
		return err
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(receiveID).
			MsgType("interactive").
			Content(string(content)).
			Uuid(uuid.NewString()).
			Build()).
		Build()

	resp, err := m.client.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return &feishuSendError{Code: resp.Code, Msg: resp.Msg}
	}

	return nil
}

// feishuSendError 保留飞书 API 错误码，便于上层按码补充处置提示。
type feishuSendError struct {
	Code int
	Msg  string
}

func (e *feishuSendError) Error() string {
	return fmt.Sprintf("feishu send message failed: code=%d msg=%s", e.Code, e.Msg)
}

// 230101 未见于飞书官方错误码文档，实测为个人版租户对机器人 open_id
// 主动发消息的内部限制；同会话改按 chat_id 发送不受影响。
const errFeishuSendUnavailable = 230101

func hintFeishuSendError(err error) error {
	var sendErr *feishuSendError
	if !errors.As(err, &sendErr) || sendErr.Code != errFeishuSendUnavailable {
		return err
	}
	return fmt.Errorf("%w\n  飞书个人版租户禁止机器人主动给用户发消息：在 config.yaml 的 feishu 渠道配置 chat_id（oc_ 开头），或改用企业/团队租户", err)
}
