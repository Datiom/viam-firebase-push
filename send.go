package firebasepush

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"firebase.google.com/go/v4/messaging"
)

// maxTokensPerBatch is the FCM limit for one SendEachForMulticast call.
const maxTokensPerBatch = 500

func (s *firebasePush) send(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	title, body, imageURL, err := resolveContent(cmd, s.presets, s.enforcePreset)
	if err != nil {
		return nil, err
	}
	data, err := parseData(cmd["data"])
	if err != nil {
		return nil, err
	}
	// Tokens in the command replace the configured defaults.
	tokens := parseTokens(cmd["fcm_tokens"])
	if len(tokens) == 0 {
		tokens = s.tokens
	}
	if len(tokens) == 0 {
		return nil, errors.New("no FCM tokens provided; either configure tokens or provide them in the command")
	}

	// An uploaded image overrides any image_url. A failed upload logs and
	// falls back to that image_url rather than failing the send.
	b64, mimeType := stringArg(cmd, "media_base64"), stringArg(cmd, "media_mime_type")
	if b64 != "" && mimeType != "" {
		if url, err := s.uploadImage(ctx, b64, mimeType); err != nil {
			s.logger.Errorw("error processing media_base64; proceeding without the uploaded image", "error", err)
		} else {
			imageURL = url
		}
	}

	msg := buildMessage(title, body, imageURL, data)
	sent, failed := 0, 0
	for start := 0; start < len(tokens); start += maxTokensPerBatch {
		batch := tokens[start:min(start+maxTokensPerBatch, len(tokens))]
		m := *msg
		// Tokens is deprecated in favor of Fids, but FIDs are a different
		// identifier from the FCM registration tokens callers pass in.
		m.Tokens = batch //nolint:staticcheck
		resp, err := s.sender.SendEachForMulticast(ctx, &m)
		if err != nil {
			s.logger.Errorw("error sending notification batch", "tokens", len(batch), "error", err)
			failed += len(batch)
			continue
		}
		sent += resp.SuccessCount
		failed += resp.FailureCount
		for i, r := range resp.Responses {
			if !r.Success {
				s.logger.Errorw("error sending notification", "token", abbreviate(batch[i]), "error", r.Error)
			}
		}
	}

	return map[string]interface{}{
		"success":      sent > 0,
		"sent_count":   sent,
		"failed_count": failed,
		// Duplicates sent_count; kept because Python-era callers read it.
		"sent_to_tokens": sent,
	}, nil
}

func (s *firebasePush) uploadImage(ctx context.Context, b64, mimeType string) (string, error) {
	if s.images == nil {
		return "", errors.New("storage bucket is not configured or not reachable")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decoding media_base64: %w", err)
	}
	return s.images.upload(ctx, raw, mimeType)
}

// resolveContent picks the title, body and image URL from a preset or from
// the command itself. A preset ignores the command's title, body and image_url.
func resolveContent(cmd map[string]interface{}, presets map[string]preset, enforce bool) (title, body, imageURL string, err error) {
	name := stringArg(cmd, "preset")
	if enforce && (name == "" || len(presets) == 0) {
		return "", "", "", errors.New("service is configured to enforce presets, but no preset was specified or no presets are configured")
	}
	if name != "" {
		p, ok := presets[name]
		if !ok {
			return "", "", "", fmt.Errorf("preset %q not found in configured presets", name)
		}
		vars, _ := cmd["template_vars"].(map[string]interface{})
		title, body, imageURL = fillTemplate(p.Title, vars), fillTemplate(p.Body, vars), fillTemplate(p.ImageURL, vars)
	} else {
		title, body, imageURL = stringArg(cmd, "title"), stringArg(cmd, "body"), stringArg(cmd, "image_url")
	}
	if title == "" && body == "" {
		return "", "", "", errors.New("either title or body must be provided")
	}
	return title, body, imageURL, nil
}

// fillTemplate replaces each <<key>> placeholder with its template_vars value.
func fillTemplate(s string, vars map[string]interface{}) string {
	for k, v := range vars {
		s = strings.ReplaceAll(s, "<<"+k+">>", fmt.Sprint(v))
	}
	return s
}

// parseData converts the optional data payload. FCM only carries string
// values, so anything else is rejected rather than silently stringified.
func parseData(v interface{}) (map[string]string, error) {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) == 0 {
		return nil, nil
	}
	data := make(map[string]string, len(m))
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("data value for %q must be a string, got %T", k, val)
		}
		data[k] = s
	}
	return data, nil
}

func buildMessage(title, body, imageURL string, data map[string]string) *messaging.MulticastMessage {
	msg := &messaging.MulticastMessage{
		Data:         data,
		Notification: &messaging.Notification{Title: title, Body: body, ImageURL: imageURL},
	}
	if imageURL != "" {
		// Each platform needs its own hint to render the image.
		msg.Android = &messaging.AndroidConfig{
			Notification: &messaging.AndroidNotification{ImageURL: imageURL},
		}
		msg.APNS = &messaging.APNSConfig{
			Payload:    &messaging.APNSPayload{Aps: &messaging.Aps{MutableContent: true}},
			FCMOptions: &messaging.APNSFCMOptions{ImageURL: imageURL},
		}
		msg.Webpush = &messaging.WebpushConfig{
			Notification: &messaging.WebpushNotification{Image: imageURL},
		}
	}
	return msg
}

func stringArg(cmd map[string]interface{}, key string) string {
	s, _ := cmd[key].(string)
	return s
}

// abbreviate keeps registration tokens, which are long-lived device
// credentials, out of the logs in full.
func abbreviate(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12] + "…"
}
