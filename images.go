package firebasepush

import (
	"context"
	"fmt"
	"sync"
	"time"

	"cloud.google.com/go/storage"
	firebase "firebase.google.com/go/v4"
	"github.com/google/uuid"

	"go.viam.com/rdk/logging"
)

// imageTTL is both the signed URL lifetime and how long an uploaded image is
// kept. Devices fetch the image when they display the notification, which can
// be well after FCM accepts the send, so the object must outlive the send.
const imageTTL = 15 * time.Minute

// imageStore uploads media_base64 images to Firebase Storage and deletes each
// one once its signed URL has expired.
type imageStore struct {
	bucket     *storage.BucketHandle
	bucketName string
	accessID   string
	privateKey []byte
	logger     logging.Logger

	mu     sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

func newImageStore(ctx context.Context, app *firebase.App, sa *serviceAccount, logger logging.Logger) (*imageStore, error) {
	client, err := app.Storage(ctx)
	if err != nil {
		return nil, err
	}
	bucket, err := client.DefaultBucket()
	if err != nil {
		return nil, err
	}
	return &imageStore{
		bucket:     bucket,
		bucketName: bucket.BucketName(),
		accessID:   sa.ClientEmail,
		privateKey: []byte(sa.PrivateKey),
		logger:     logger,
		timers:     map[string]*time.Timer{},
	}, nil
}

// upload stores the image and returns a signed URL valid for imageTTL.
func (s *imageStore) upload(ctx context.Context, data []byte, mimeType string) (string, error) {
	name := "push_image_" + uuid.NewString()
	w := s.bucket.Object(name).NewWriter(ctx)
	w.ContentType = mimeType
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("uploading image: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("uploading image: %w", err)
	}

	url, err := s.bucket.SignedURL(name, &storage.SignedURLOptions{
		GoogleAccessID: s.accessID,
		PrivateKey:     s.privateKey,
		Method:         "GET",
		Expires:        time.Now().Add(imageTTL),
		Scheme:         storage.SigningSchemeV4,
	})
	if err != nil {
		s.remove(name)
		return "", fmt.Errorf("signing image URL: %w", err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.remove(name)
		return "", fmt.Errorf("service closed during image upload")
	}
	s.timers[name] = time.AfterFunc(imageTTL, func() { s.remove(name) })
	s.mu.Unlock()

	s.logger.Infow("uploaded base64 image", "object", name)
	return url, nil
}

func (s *imageStore) remove(name string) {
	s.mu.Lock()
	delete(s.timers, name)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.bucket.Object(name).Delete(ctx); err != nil {
		s.logger.Errorw("failed to delete temporary image from Firebase Storage", "object", name, "error", err)
		return
	}
	s.logger.Infow("deleted temporary image from Firebase Storage", "object", name)
}

func (s *imageStore) pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

// close deletes every image still waiting for its timer. The service is
// rebuilt on every reconfigure, so images sent just before a reconfigure lose
// their remaining lifetime; the alternative is objects left in the bucket
// forever when the module exits.
func (s *imageStore) close(ctx context.Context) {
	s.mu.Lock()
	s.closed = true
	names := make([]string, 0, len(s.timers))
	for name, t := range s.timers {
		if t.Stop() {
			names = append(names, name)
		}
	}
	s.mu.Unlock()
	for _, name := range names {
		s.remove(name)
	}
}
