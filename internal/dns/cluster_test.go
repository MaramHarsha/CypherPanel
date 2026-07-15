package dns

import (
	"context"
	"errors"
	"testing"
)

// fakeProvider records the writes it receives and can be made to fail.
type fakeProvider struct {
	upserts []string
	fail    bool
}

func (f *fakeProvider) EnsureZone(context.Context, string, []string, []Record) error {
	if f.fail {
		return errors.New("down")
	}
	return nil
}
func (f *fakeProvider) DeleteZone(context.Context, string) error { return nil }
func (f *fakeProvider) UpsertRecord(_ context.Context, _ string, r Record) error {
	if f.fail {
		return errors.New("down")
	}
	f.upserts = append(f.upserts, r.Name)
	return nil
}
func (f *fakeProvider) DeleteRecord(context.Context, string, string, string) error { return nil }
func (f *fakeProvider) ListRecords(context.Context, string) ([]Record, error)      { return nil, nil }

func TestClustered_FanOutToAllNodes(t *testing.T) {
	primary := &fakeProvider{}
	s1, s2 := &fakeProvider{}, &fakeProvider{}
	c := NewClustered(primary, []Provider{s1, s2})

	rec := Record{Name: "www.example.com.", Type: "A", Contents: []string{"1.2.3.4"}}
	if err := c.UpsertRecord(context.Background(), "example.com", rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for name, p := range map[string]*fakeProvider{"primary": primary, "s1": s1, "s2": s2} {
		if len(p.upserts) != 1 {
			t.Errorf("%s did not receive the write (%d)", name, len(p.upserts))
		}
	}
}

func TestClustered_SecondaryFailureIsNonFatal(t *testing.T) {
	primary := &fakeProvider{}
	badSecondary := &fakeProvider{fail: true}
	c := NewClustered(primary, []Provider{badSecondary})

	// A down secondary must NOT fail the edit — the primary took the write.
	if err := c.UpsertRecord(context.Background(), "example.com",
		Record{Name: "a.example.com.", Type: "A", Contents: []string{"1.1.1.1"}}); err != nil {
		t.Fatalf("secondary failure should be non-fatal, got %v", err)
	}
	if len(primary.upserts) != 1 {
		t.Fatal("primary should still have received the write")
	}
}

func TestClustered_PrimaryFailureAborts(t *testing.T) {
	primary := &fakeProvider{fail: true}
	c := NewClustered(primary, []Provider{&fakeProvider{}})
	if err := c.UpsertRecord(context.Background(), "example.com",
		Record{Name: "x.example.com.", Type: "A", Contents: []string{"1.1.1.1"}}); err == nil {
		t.Fatal("a primary failure must abort the operation")
	}
}
