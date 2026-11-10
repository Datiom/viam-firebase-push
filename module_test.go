package firebasepush

import (
	"context"
	"errors"
	"testing"

	"firebase.google.com/go/v4/messaging"
	"go.viam.com/rdk/logging"
	"go.viam.com/test"
)

func TestValidate(t *testing.T) {
	_, _, err := (&Config{}).Validate("path")
	test.That(t, err, test.ShouldNotBeNil)

	_, _, err = (&Config{ServiceAccountFile: "/tmp/sa.json"}).Validate("path")
	test.That(t, err, test.ShouldBeNil)

	_, _, err = (&Config{ServiceAccountJSON: map[string]any{"project_id": "p"}}).Validate("path")
	test.That(t, err, test.ShouldBeNil)

	_, _, err = (&Config{ServiceAccountJSON: `{"project_id": "p"}`}).Validate("path")
	test.That(t, err, test.ShouldBeNil)

	_, _, err = (&Config{ServiceAccountJSON: "not json"}).Validate("path")
	test.That(t, err, test.ShouldNotBeNil)

	_, _, err = (&Config{ServiceAccountFile: "/tmp/sa.json", PresetMessages: "not json"}).Validate("path")
	test.That(t, err, test.ShouldNotBeNil)
}

func TestParseTokens(t *testing.T) {
	test.That(t, parseTokens(nil), test.ShouldBeNil)
	test.That(t, parseTokens(""), test.ShouldBeNil)
	test.That(t, parseTokens("abc"), test.ShouldResemble, []string{"abc"})
	test.That(t, parseTokens(`["a", "b"]`), test.ShouldResemble, []string{"a", "b"})
	test.That(t, parseTokens([]any{"a", " ", "b"}), test.ShouldResemble, []string{"a", "b"})
}

func TestParsePresets(t *testing.T) {
	want := map[string]preset{"alert": {Title: "Alert <<x>>", Body: "b"}}

	got, err := parsePresets(`{"alert": {"title": "Alert <<x>>", "body": "b"}}`)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, got, test.ShouldResemble, want)

	got, err = parsePresets(map[string]any{"alert": map[string]any{"title": "Alert <<x>>", "body": "b"}})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, got, test.ShouldResemble, want)
}

func TestResolveContent(t *testing.T) {
	presets := map[string]preset{
		"alert": {Title: "Alert: <<kind>>", Body: "Count <<n>>", ImageURL: "https://x/<<kind>>.png"},
	}

	title, body, img, err := resolveContent(map[string]interface{}{
		"preset":        "alert",
		"template_vars": map[string]interface{}{"kind": "fire", "n": 3.0},
		"title":         "ignored",
	}, presets, false)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, title, test.ShouldEqual, "Alert: fire")
	test.That(t, body, test.ShouldEqual, "Count 3")
	test.That(t, img, test.ShouldEqual, "https://x/fire.png")

	title, body, img, err = resolveContent(map[string]interface{}{"title": "t", "image_url": "u"}, presets, false)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, []string{title, body, img}, test.ShouldResemble, []string{"t", "", "u"})

	_, _, _, err = resolveContent(map[string]interface{}{"title": "t"}, presets, true)
	test.That(t, err, test.ShouldNotBeNil)

	_, _, _, err = resolveContent(map[string]interface{}{"preset": "missing"}, presets, false)
	test.That(t, err, test.ShouldNotBeNil)

	_, _, _, err = resolveContent(map[string]interface{}{}, presets, false)
	test.That(t, err, test.ShouldNotBeNil)
}

func TestParseData(t *testing.T) {
	d, err := parseData(map[string]interface{}{"k": "v"})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, d, test.ShouldResemble, map[string]string{"k": "v"})

	d, err = parseData("not a map")
	test.That(t, err, test.ShouldBeNil)
	test.That(t, d, test.ShouldBeNil)

	_, err = parseData(map[string]interface{}{"k": 1.0})
	test.That(t, err, test.ShouldNotBeNil)
}

func TestBuildMessageImage(t *testing.T) {
	msg := buildMessage("t", "b", "", nil)
	test.That(t, msg.Android, test.ShouldBeNil)

	msg = buildMessage("t", "b", "https://img", nil)
	test.That(t, msg.Notification.ImageURL, test.ShouldEqual, "https://img")
	test.That(t, msg.Android.Notification.ImageURL, test.ShouldEqual, "https://img")
	test.That(t, msg.APNS.FCMOptions.ImageURL, test.ShouldEqual, "https://img")
	test.That(t, msg.APNS.Payload.Aps.MutableContent, test.ShouldBeTrue)
	test.That(t, msg.Webpush.Notification.Image, test.ShouldEqual, "https://img")
}

// fakeSender records each batch and fails the tokens listed in fail.
type fakeSender struct {
	batches  [][]string
	fail     map[string]bool
	batchErr error
}

func (f *fakeSender) SendEachForMulticast(ctx context.Context, m *messaging.MulticastMessage) (*messaging.BatchResponse, error) {
	f.batches = append(f.batches, m.Tokens) //nolint:staticcheck
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	resp := &messaging.BatchResponse{}
	for _, tok := range m.Tokens { //nolint:staticcheck
		if f.fail[tok] {
			resp.FailureCount++
			resp.Responses = append(resp.Responses, &messaging.SendResponse{Error: errors.New("bad token")})
		} else {
			resp.SuccessCount++
			resp.Responses = append(resp.Responses, &messaging.SendResponse{Success: true})
		}
	}
	return resp, nil
}

func newTestService(t *testing.T, sender pushSender, tokens []string) *firebasePush {
	return &firebasePush{logger: logging.NewTestLogger(t), sender: sender, tokens: tokens, presets: map[string]preset{}}
}

func TestSend(t *testing.T) {
	ctx := context.Background()

	t.Run("command tokens override configured tokens", func(t *testing.T) {
		f := &fakeSender{fail: map[string]bool{"bad": true}}
		s := newTestService(t, f, []string{"default"})
		res, err := s.DoCommand(ctx, map[string]interface{}{"command": "send", "title": "hi", "fcm_tokens": []any{"ok", "bad"}})
		test.That(t, err, test.ShouldBeNil)
		test.That(t, f.batches, test.ShouldResemble, [][]string{{"ok", "bad"}})
		test.That(t, res, test.ShouldResemble, map[string]interface{}{
			"success": true, "sent_count": 1, "failed_count": 1, "sent_to_tokens": 1,
		})
	})

	t.Run("batches of 500", func(t *testing.T) {
		tokens := make([]string, 1001)
		for i := range tokens {
			tokens[i] = "t"
		}
		f := &fakeSender{}
		s := newTestService(t, f, tokens)
		res, err := s.DoCommand(ctx, map[string]interface{}{"command": "send", "body": "b"})
		test.That(t, err, test.ShouldBeNil)
		test.That(t, len(f.batches), test.ShouldEqual, 3)
		test.That(t, len(f.batches[2]), test.ShouldEqual, 1)
		test.That(t, res["sent_count"], test.ShouldEqual, 1001)
	})

	t.Run("batch error counts every token as failed", func(t *testing.T) {
		f := &fakeSender{batchErr: errors.New("boom")}
		s := newTestService(t, f, []string{"a", "b"})
		res, err := s.DoCommand(ctx, map[string]interface{}{"command": "send", "body": "b"})
		test.That(t, err, test.ShouldBeNil)
		test.That(t, res["success"], test.ShouldBeFalse)
		test.That(t, res["failed_count"], test.ShouldEqual, 2)
	})

	t.Run("no tokens", func(t *testing.T) {
		s := newTestService(t, &fakeSender{}, nil)
		_, err := s.DoCommand(ctx, map[string]interface{}{"command": "send", "body": "b"})
		test.That(t, err, test.ShouldNotBeNil)
	})

	t.Run("media_base64 without a bucket still sends", func(t *testing.T) {
		f := &fakeSender{}
		s := newTestService(t, f, []string{"a"})
		res, err := s.DoCommand(ctx, map[string]interface{}{
			"command": "send", "body": "b", "media_base64": "aGk=", "media_mime_type": "image/png",
		})
		test.That(t, err, test.ShouldBeNil)
		test.That(t, res["sent_count"], test.ShouldEqual, 1)
	})

	t.Run("unknown and missing command", func(t *testing.T) {
		s := newTestService(t, &fakeSender{}, nil)
		_, err := s.DoCommand(ctx, map[string]interface{}{"command": "nope"})
		test.That(t, err, test.ShouldNotBeNil)
		_, err = s.DoCommand(ctx, map[string]interface{}{})
		test.That(t, err, test.ShouldNotBeNil)
	})
}
