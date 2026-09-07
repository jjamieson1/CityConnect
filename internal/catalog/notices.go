package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/domain"
	"github.com/jjamieson1/CityConnect/internal/store"
)

// CollectionNoticeFor returns the notice a service should show, falling back to
// the municipality's default.
//
// Returns nil when nothing is configured. A deployment with no notice is a
// compliance problem rather than a runtime one, so this reports the absence
// upward instead of inventing wording on a City's behalf — a collection notice
// we wrote is a legal statement they never approved.
func (s *Service) CollectionNoticeFor(ctx context.Context, serviceTypeID string) (*domain.CollectionNotice, error) {
	if serviceTypeID != "" {
		notice, err := s.currentNotice(ctx, serviceTypeID)
		if err == nil {
			return notice, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	notice, err := s.currentNotice(ctx, "")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return notice, err
}

func (s *Service) currentNotice(ctx context.Context, serviceTypeID string) (*domain.CollectionNotice, error) {
	var n domain.CollectionNotice
	err := s.db.WithContext(ctx).
		Where("service_type_id = ? AND superseded = ?", serviceTypeID, false).
		Order("version DESC").First(&n).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, store.Translate(err)
	}
	return &n, nil
}

// NoticeVersion loads one specific version, for recording what a submission was
// actually shown.
func (s *Service) NoticeVersion(ctx context.Context, id string) (*domain.CollectionNotice, error) {
	var n domain.CollectionNotice
	err := s.db.WithContext(ctx).First(&n, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &n, store.Translate(err)
}

// SaveCollectionNotice publishes new wording.
//
// Always a new version, never an edit. Rewriting the text in place would change
// what past submissions were shown, and "we told them" is worth nothing if the
// telling can be altered afterwards. The previous version is superseded rather
// than deleted, because the submissions that were shown it still point at it.
func (s *Service) SaveCollectionNotice(ctx context.Context, actor audit.Actor, serviceTypeID, body string) (*domain.CollectionNotice, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("%w: a collection notice needs wording", ErrInvalidInput)
	}

	var created *domain.CollectionNotice
	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		version := 1
		var previous domain.CollectionNotice
		err := tx.Where("service_type_id = ?", serviceTypeID).
			Order("version DESC").First(&previous).Error
		switch {
		case err == nil:
			if strings.TrimSpace(previous.Body) == body && !previous.Superseded {
				// Identical wording. Publishing it again would create a version
				// that says nothing new and split the evidence trail in two.
				created = &previous
				return nil
			}
			version = previous.Version + 1
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}

		if err := tx.Model(&domain.CollectionNotice{}).
			Where("service_type_id = ? AND superseded = ?", serviceTypeID, false).
			UpdateColumn("superseded", true).Error; err != nil {
			return err
		}

		notice := &domain.CollectionNotice{
			ServiceTypeID: serviceTypeID, Version: version, Body: body,
			EffectiveAt: time.Now().UTC(),
		}
		if err := tx.Create(notice).Error; err != nil {
			return err
		}
		created = notice
		return nil
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "catalog.collection_notice_published", TargetType: "collection_notice",
		TargetID: created.ID,
		Summary:  fmt.Sprintf("version %d", created.Version),
	})
	return created, nil
}

// ListCollectionNotices returns every version, newest first, so an operator can
// see what was in force when.
func (s *Service) ListCollectionNotices(ctx context.Context) ([]domain.CollectionNotice, error) {
	var out []domain.CollectionNotice
	err := s.db.WithContext(ctx).
		Order("service_type_id ASC, version DESC").Find(&out).Error
	return out, store.Translate(err)
}
