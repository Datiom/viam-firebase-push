package firebasepush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/generic"
)

var FirebasePush = resource.NewModel("viam", "service", "firebase-push")

func init() {
	resource.RegisterService(generic.API, FirebasePush,
		resource.Registration[resource.Resource, *Config]{
			Constructor: newFirebasePush,
		},
	)
}

// Config accepts the same attributes as the Python module. Several fields are
// typed `any` because the Python module accepted either a JSON string or the
// native JSON value for them, and existing configs use both forms.
type Config struct {
	ServiceAccountJSON any    `json:"service_account_json,omitempty"`
	ServiceAccountFile string `json:"service_account_file,omitempty"`
	StorageBucketName  string `json:"storage_bucket_name,omitempty"`
	FCMTokens          any    `json:"fcm_tokens,omitempty"`
	PresetMessages     any    `json:"preset_messages,omitempty"`
	EnforcePreset      bool   `json:"enforce_preset,omitempty"`
}

// Validate ensures all parts of the config are valid and important fields exist.
// Returns three values:
//  1. Required dependencies: other resources that must exist for this resource to work.
//  2. Optional dependencies: other resources that may exist but are not required.
//  3. An error if any Config fields are missing or invalid.
func (cfg *Config) Validate(path string) ([]string, []string, error) {
	if isEmpty(cfg.ServiceAccountJSON) && cfg.ServiceAccountFile == "" {
		return nil, nil, errors.New("either service_account_json or service_account_file must be provided")
	}
	if !isEmpty(cfg.ServiceAccountJSON) {
		if _, err := parseServiceAccount(cfg.ServiceAccountJSON); err != nil {
			return nil, nil, err
		}
	}
	if _, err := parsePresets(cfg.PresetMessages); err != nil {
		return nil, nil, err
	}
	return nil, nil, nil
}

// serviceAccount holds the raw credentials plus the fields this module reads
// directly: the project ID for the Firebase app, and the email and key that
// sign image URLs.
type serviceAccount struct {
	raw         []byte
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func parseServiceAccount(v any) (*serviceAccount, error) {
	var raw []byte
	if s, ok := v.(string); ok {
		raw = []byte(s)
	} else {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("invalid service_account_json: %w", err)
		}
		raw = b
	}
	sa := &serviceAccount{raw: raw}
	if err := json.Unmarshal(raw, sa); err != nil {
		return nil, fmt.Errorf("invalid service_account_json: not a valid JSON object: %w", err)
	}
	return sa, nil
}

func (cfg *Config) serviceAccount() (*serviceAccount, error) {
	if !isEmpty(cfg.ServiceAccountJSON) {
		return parseServiceAccount(cfg.ServiceAccountJSON)
	}
	b, err := os.ReadFile(cfg.ServiceAccountFile)
	if err != nil {
		return nil, fmt.Errorf("reading service_account_file: %w", err)
	}
	sa, err := parseServiceAccount(string(b))
	if err != nil {
		return nil, fmt.Errorf("service_account_file %s: %w", cfg.ServiceAccountFile, err)
	}
	return sa, nil
}

type preset struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	ImageURL string `json:"image_url"`
}

func parsePresets(v any) (map[string]preset, error) {
	if isEmpty(v) {
		return map[string]preset{}, nil
	}
	raw, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("invalid preset_messages: %w", err)
		}
		raw = string(b)
	}
	presets := map[string]preset{}
	if err := json.Unmarshal([]byte(raw), &presets); err != nil {
		return nil, fmt.Errorf("invalid preset_messages: %w", err)
	}
	return presets, nil
}

// parseTokens accepts a list, a JSON-encoded list, or a single token string.
func parseTokens(v any) []string {
	var items []any
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		var list []any
		if err := json.Unmarshal([]byte(t), &list); err == nil {
			items = list
		} else {
			items = []any{t}
		}
	case []any:
		items = t
	case []string:
		for _, s := range t {
			items = append(items, s)
		}
	default:
		items = []any{t}
	}
	var tokens []string
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			s = fmt.Sprint(item)
		}
		if s = strings.TrimSpace(s); s != "" {
			tokens = append(tokens, s)
		}
	}
	return tokens
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// pushSender is the part of the FCM client this module uses; tests swap in a fake.
type pushSender interface {
	SendEachForMulticast(ctx context.Context, message *messaging.MulticastMessage) (*messaging.BatchResponse, error)
}

type firebasePush struct {
	resource.AlwaysRebuild
	resource.Named

	logger logging.Logger

	sender        pushSender
	images        *imageStore // nil when no storage bucket is reachable
	tokens        []string
	presets       map[string]preset
	enforcePreset bool
}

func newFirebasePush(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*Config](rawConf)
	if err != nil {
		return nil, err
	}
	return NewFirebasePush(ctx, rawConf.ResourceName(), conf, logger)
}

func NewFirebasePush(ctx context.Context, name resource.Name, conf *Config, logger logging.Logger) (resource.Resource, error) {
	sa, err := conf.serviceAccount()
	if err != nil {
		return nil, err
	}
	presets, err := parsePresets(conf.PresetMessages)
	if err != nil {
		return nil, err
	}

	app, err := firebase.NewApp(ctx,
		&firebase.Config{ProjectID: sa.ProjectID, StorageBucket: conf.StorageBucketName},
		option.WithAuthCredentialsJSON(option.ServiceAccount, sa.raw))
	if err != nil {
		return nil, fmt.Errorf("initializing Firebase app: %w", err)
	}
	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("initializing Firebase messaging: %w", err)
	}

	s := &firebasePush{
		Named:         name.AsNamed(),
		logger:        logger,
		sender:        client,
		tokens:        parseTokens(conf.FCMTokens),
		presets:       presets,
		enforcePreset: conf.EnforcePreset,
	}

	// Storage only backs media_base64 uploads, so a bad bucket degrades that
	// one feature instead of failing the whole service.
	if conf.StorageBucketName == "" {
		logger.Info("no storage_bucket_name configured; image upload via media_base64 is disabled")
	} else if s.images, err = newImageStore(ctx, app, sa, logger); err != nil {
		logger.Errorw("failed to connect to Firebase Storage bucket; image upload via media_base64 will not work", "error", err)
	} else {
		logger.Infow("connected to Firebase Storage bucket", "bucket", s.images.bucketName)
	}

	logger.Infow("Firebase Push configured", "default_tokens", len(s.tokens), "presets", len(s.presets))
	return s, nil
}

func (s *firebasePush) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	c, ok := cmd["command"]
	if !ok {
		return nil, errors.New("missing 'command' field in request")
	}
	switch c {
	case "send":
		return s.send(ctx, cmd)
	default:
		return nil, fmt.Errorf("unknown command: %v", c)
	}
}

func (s *firebasePush) Status(ctx context.Context) (map[string]interface{}, error) {
	names := make([]interface{}, 0, len(s.presets))
	keys := make([]string, 0, len(s.presets))
	for k := range s.presets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		names = append(names, k)
	}
	status := map[string]interface{}{
		"default_tokens": len(s.tokens),
		"presets":        names,
		"enforce_preset": s.enforcePreset,
		"storage_bucket": "",
		"pending_images": 0,
	}
	if s.images != nil {
		status["storage_bucket"] = s.images.bucketName
		status["pending_images"] = s.images.pending()
	}
	return status, nil
}

func (s *firebasePush) Close(ctx context.Context) error {
	if s.images != nil {
		s.images.close(ctx)
	}
	return nil
}
