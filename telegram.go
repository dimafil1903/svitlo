package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type TelegramBot struct {
	token      string
	userIDs    []int64
	httpClient *http.Client
	offset     int64
}

func NewTelegramBot(token string, userIDs []int64) *TelegramBot {
	return &TelegramBot{
		token:   token,
		userIDs: userIDs,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (b *TelegramBot) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.token, method)
}

// --- Send Message ---

type sendMessageRequest struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

type telegramResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (b *TelegramBot) SendMessage(chatID int64, text string) error {
	body := sendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: "HTML",
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal sendMessage: %w", err)
	}

	resp, err := b.httpClient.Post(b.apiURL("sendMessage"), "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sendMessage request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read sendMessage response: %w", err)
	}

	var tgResp telegramResponse
	if err := json.Unmarshal(respBody, &tgResp); err != nil {
		return fmt.Errorf("unmarshal sendMessage response: %w", err)
	}

	if !tgResp.OK {
		return fmt.Errorf("telegram sendMessage failed: %s", tgResp.Description)
	}

	return nil
}

func (b *TelegramBot) Broadcast(text string) {
	for _, userID := range b.userIDs {
		if err := b.SendMessage(userID, text); err != nil {
			log.Printf("[telegram] failed to send to %d: %v", userID, err)
		}
	}
}

// --- Get Updates (long polling) ---

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type getUpdatesRequest struct {
	Offset  int64 `json:"offset"`
	Timeout int   `json:"timeout"`
}

type getUpdatesResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

func (b *TelegramBot) GetUpdates() ([]Update, error) {
	body := getUpdatesRequest{
		Offset:  b.offset,
		Timeout: 30,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal getUpdates: %w", err)
	}

	resp, err := b.httpClient.Post(b.apiURL("getUpdates"), "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("getUpdates request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read getUpdates response: %w", err)
	}

	var updResp getUpdatesResponse
	if err := json.Unmarshal(respBody, &updResp); err != nil {
		return nil, fmt.Errorf("unmarshal getUpdates response: %w", err)
	}

	if !updResp.OK {
		return nil, fmt.Errorf("getUpdates failed")
	}

	if len(updResp.Result) > 0 {
		b.offset = updResp.Result[len(updResp.Result)-1].UpdateID + 1
	}

	return updResp.Result, nil
}

func (b *TelegramBot) IsAllowedUser(chatID int64) bool {
	for _, id := range b.userIDs {
		if id == chatID {
			return true
		}
	}
	return false
}
