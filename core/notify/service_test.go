package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// identitySealer makes sealing observable and reversible in tests: the
// "ciphertext" is the plaintext prefixed, and Open strips the prefix.
type identitySealer struct{}

func (identitySealer) Seal(pt []byte) (ct, nonce []byte, err error) {
	return append([]byte("sealed:"), pt...), []byte("n"), nil
}

type svcStore struct {
	projects  map[string]bool
	notifiers map[string]domain.Notifier
}

func newSvcStore() *svcStore {
	return &svcStore{projects: map[string]bool{"prj_1": true}, notifiers: map[string]domain.Notifier{}}
}

func (s *svcStore) GetProject(_ context.Context, id string) (domain.Project, error) {
	if !s.projects[id] {
		return domain.Project{}, store.ErrNotFound
	}
	return domain.Project{ID: id}, nil
}
func (s *svcStore) CreateNotifier(_ context.Context, n domain.Notifier) (domain.Notifier, error) {
	s.notifiers[n.ID] = n
	return n, nil
}
func (s *svcStore) GetNotifier(_ context.Context, id string) (domain.Notifier, error) {
	n, ok := s.notifiers[id]
	if !ok {
		return domain.Notifier{}, store.ErrNotFound
	}
	return n, nil
}
func (s *svcStore) ListNotifiersByProject(_ context.Context, projectID string) ([]domain.Notifier, error) {
	var out []domain.Notifier
	for _, n := range s.notifiers {
		if n.ProjectID == projectID {
			out = append(out, n)
		}
	}
	return out, nil
}
func (s *svcStore) UpdateNotifier(_ context.Context, n domain.Notifier) (domain.Notifier, error) {
	s.notifiers[n.ID] = n
	return n, nil
}
func (s *svcStore) DeleteNotifier(_ context.Context, id string) error {
	delete(s.notifiers, id)
	return nil
}

func slackInput() CreateInput {
	return CreateInput{
		Name:    "team-slack",
		Channel: domain.NotifyChannelSlack,
		Config:  json.RawMessage(`{"webhook_url":"https://hooks.slack.com/services/T/B/xyz"}`),
		Events:  []string{domain.EventDeployFailed},
		Enabled: true,
	}
}

func TestCreateSealsConfigAndStrips(t *testing.T) {
	st := newSvcStore()
	svc := NewService(st, identitySealer{})
	n, err := svc.Create(context.Background(), "prj_1", slackInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !bytes.HasPrefix(n.ConfigCT, []byte("sealed:")) {
		t.Errorf("config not sealed via sealer: %q", n.ConfigCT)
	}
	if bytes.Contains(n.ConfigCT, []byte("hooks.slack.com")) == false {
		t.Errorf("sealed config should contain the (sealed) url payload")
	}
}

func TestCreateValidation(t *testing.T) {
	cases := map[string]func(*CreateInput){
		"empty name":       func(in *CreateInput) { in.Name = "" },
		"bad channel":      func(in *CreateInput) { in.Channel = "carrier-pigeon" },
		"no events":        func(in *CreateInput) { in.Events = nil },
		"unknown event":    func(in *CreateInput) { in.Events = []string{"deploy.exploded"} },
		"empty config":     func(in *CreateInput) { in.Config = nil },
		"non-http webhook": func(in *CreateInput) { in.Config = json.RawMessage(`{"webhook_url":"file:///etc/passwd"}`) },
		"unknown field":    func(in *CreateInput) { in.Config = json.RawMessage(`{"webhook_url":"https://x.io/h","extra":1}`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := newSvcStore()
			svc := NewService(st, identitySealer{})
			in := slackInput()
			mutate(&in)
			_, err := svc.Create(context.Background(), "prj_1", in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want ValidationError", err)
			}
		})
	}
}

func TestCreateUnknownProject(t *testing.T) {
	svc := NewService(newSvcStore(), identitySealer{})
	_, err := svc.Create(context.Background(), "prj_missing", slackInput())
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestCreateDedupesAndValidatesEmailConfig(t *testing.T) {
	svc := NewService(newSvcStore(), identitySealer{})
	in := CreateInput{
		Name:    "ops-email",
		Channel: domain.NotifyChannelEmail,
		Config:  json.RawMessage(`{"smtp_host":"smtp.acme.com","smtp_port":587,"from":"a@acme.com","to":"ops@acme.com","username":"a","password":"p"}`),
		Events:  []string{domain.EventDeployFailed, domain.EventDeployFailed, domain.EventBackupFailed},
		Enabled: true,
	}
	n, err := svc.Create(context.Background(), "prj_1", in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(n.Events) != 2 {
		t.Fatalf("events = %v, want deduped to 2", n.Events)
	}
}

func TestUpdateKeepsConfigWhenOmitted(t *testing.T) {
	st := newSvcStore()
	svc := NewService(st, identitySealer{})
	n, err := svc.Create(context.Background(), "prj_1", slackInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sealed := n.ConfigCT
	updated, err := svc.Update(context.Background(), n.ID, UpdateInput{
		Name:    "renamed",
		Events:  []string{domain.EventDeploySucceeded},
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !bytes.Equal(updated.ConfigCT, sealed) {
		t.Error("omitted config should be preserved, not cleared")
	}
	if updated.Name != "renamed" || updated.Enabled != false {
		t.Errorf("metadata not updated: %+v", updated)
	}
}

func TestConfigHintMasksWebhook(t *testing.T) {
	hint := ConfigHint(domain.NotifyChannelSlack, []byte(`{"webhook_url":"https://hooks.slack.com/services/T/B/secret123"}`))
	if bytes.Contains([]byte(hint), []byte("secret123")) {
		t.Errorf("hint leaked the webhook secret: %q", hint)
	}
	if hint == "" {
		t.Error("hint should be non-empty")
	}
}
