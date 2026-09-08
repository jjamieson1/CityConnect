package catalog

import (
	"context"
	"testing"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
)

func publish(t *testing.T, s *Service, serviceTypeID, body string) *domain.CollectionNotice {
	t.Helper()
	out, err := s.SaveCollectionNotice(context.Background(), audit.JobActor("test"), serviceTypeID, body)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return out
}

// The property the whole record rests on: editing the wording must not rewrite
// what past submissions were shown. "We told them" is worth nothing if the
// telling can be changed afterwards.
func TestPublishingNewWordingVersionsRatherThanEdits(t *testing.T) {
	s, db := newCategoryEnv(t)

	first := publish(t, s, "", "We collect your details to handle this report.")
	second := publish(t, s, "", "We collect your details to handle this report and to contact you.")

	if first.ID == second.ID {
		t.Fatal("the second publish edited the first in place")
	}
	if second.Version != 2 {
		t.Errorf("version = %d, want 2", second.Version)
	}

	// The old version survives, because submissions still point at it.
	var original domain.CollectionNotice
	if err := db.First(&original, "id = ?", first.ID).Error; err != nil {
		t.Fatalf("the superseded version was deleted: %v", err)
	}
	if original.Body != "We collect your details to handle this report." {
		t.Errorf("the superseded text changed to %q", original.Body)
	}
	if !original.Superseded {
		t.Error("the old version is still marked current")
	}
}

// Republishing identical wording would split the evidence trail across two
// versions that say the same thing.
func TestRepublishingIdenticalWordingIsANoOp(t *testing.T) {
	s, _ := newCategoryEnv(t)

	first := publish(t, s, "", "We collect your details to handle this report.")
	same := publish(t, s, "", "  We collect your details to handle this report.  ")

	if same.ID != first.ID {
		t.Errorf("identical wording created version %d", same.Version)
	}
}

func TestCurrentNoticeIsTheLatestVersion(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	publish(t, s, "", "First wording.")
	latest := publish(t, s, "", "Second wording.")

	got, err := s.CollectionNoticeFor(ctx, "")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got == nil || got.ID != latest.ID {
		t.Errorf("got %v, want the latest version", got)
	}
}

// A service may need its own wording; most will not, and must fall back.
func TestServiceNoticeOverridesTheDefault(t *testing.T) {
	s, _ := newCategoryEnv(t)
	ctx := context.Background()

	publish(t, s, "", "The City's default notice.")
	special := publish(t, s, "svc-1", "Wording specific to this service.")

	got, err := s.CollectionNoticeFor(ctx, "svc-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != special.ID {
		t.Errorf("service-specific notice was not preferred")
	}

	// A service with nothing of its own falls back to the default.
	fallback, err := s.CollectionNoticeFor(ctx, "svc-2")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if fallback == nil || fallback.Body != "The City's default notice." {
		t.Errorf("got %v, want the default", fallback)
	}
}

// A deployment with nothing configured reports the absence rather than
// inventing a legal statement the City never approved.
func TestNoNoticeConfiguredReturnsNothingRatherThanInventingOne(t *testing.T) {
	s, _ := newCategoryEnv(t)

	got, err := s.CollectionNoticeFor(context.Background(), "svc-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != nil {
		t.Errorf("got %q from an unconfigured deployment", got.Body)
	}
}

func TestEmptyWordingIsRefused(t *testing.T) {
	s, _ := newCategoryEnv(t)

	if _, err := s.SaveCollectionNotice(context.Background(), audit.JobActor("test"), "", "   "); err == nil {
		t.Error("an empty collection notice was published")
	}
}
