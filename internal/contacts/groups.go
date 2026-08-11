package contacts

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

// ListGroups returns contact groups with their member counts.
func (s *Service) ListGroups(ctx context.Context, includeInactive bool) ([]domain.ContactGroup, error) {
	q := s.db.WithContext(ctx).Model(&domain.ContactGroup{})
	if !includeInactive {
		q = q.Where("active = ?", true)
	}

	var groups []domain.ContactGroup
	if err := q.Order("name ASC").Find(&groups).Error; err != nil {
		return nil, store.Translate(err)
	}
	if len(groups) == 0 {
		return groups, nil
	}

	// One grouped count rather than a query per group.
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}
	type row struct {
		GroupID string
		N       int
	}
	var counts []row
	err := s.db.WithContext(ctx).Model(&domain.ContactGroupMember{}).
		Select("contact_group_id AS group_id, COUNT(*) AS n").
		Where("contact_group_id IN ?", ids).
		Group("contact_group_id").Scan(&counts).Error
	if err != nil {
		return nil, store.Translate(err)
	}

	byID := make(map[string]int, len(counts))
	for _, c := range counts {
		byID[c.GroupID] = c.N
	}
	for i := range groups {
		groups[i].MemberCount = byID[groups[i].ID]
	}
	return groups, nil
}

// SaveGroup creates or updates a contact group.
func (s *Service) SaveGroup(ctx context.Context, actor audit.Actor, g *domain.ContactGroup) (*domain.ContactGroup, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" {
		return nil, fmt.Errorf("%w: group name is required", ErrInvalidInput)
	}

	action := "group.created"
	if g.ID != "" {
		action = "group.updated"
	}
	if err := s.db.WithContext(ctx).Save(g).Error; err != nil {
		return nil, store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "contact_group", TargetID: g.ID, Summary: g.Name,
	})
	return g, nil
}

// DeleteGroup removes a group and its memberships.
func (s *Service) DeleteGroup(ctx context.Context, actor audit.Actor, id string) error {
	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Where("contact_group_id = ?", id).Delete(&domain.ContactGroupMember{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.ContactGroup{}, "id = ?", id).Error
	})
	if err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "group.deleted", TargetType: "contact_group", TargetID: id,
	})
	return nil
}

// AddToGroup adds contacts to a group, ignoring those already in it.
func (s *Service) AddToGroup(ctx context.Context, actor audit.Actor, groupID string, contactIDs []string) (int, error) {
	if len(contactIDs) == 0 {
		return 0, nil
	}

	var existing []domain.ContactGroupMember
	if err := s.db.WithContext(ctx).
		Where("contact_group_id = ? AND contact_id IN ?", groupID, contactIDs).
		Find(&existing).Error; err != nil {
		return 0, store.Translate(err)
	}
	already := make(map[string]bool, len(existing))
	for _, m := range existing {
		already[m.ContactID] = true
	}

	now := time.Now().UTC()
	rows := make([]domain.ContactGroupMember, 0, len(contactIDs))
	for _, cid := range contactIDs {
		if cid != "" && !already[cid] {
			rows = append(rows, domain.ContactGroupMember{
				ContactID: cid, GroupID: groupID, AddedAt: now, AddedByID: actor.ID,
			})
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := s.db.WithContext(ctx).Create(&rows).Error; err != nil {
		return 0, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: "group.members_added", TargetType: "contact_group", TargetID: groupID,
		Summary: fmt.Sprintf("added %d contact(s)", len(rows)),
	})
	return len(rows), nil
}

// RemoveFromGroup drops contacts from a group.
func (s *Service) RemoveFromGroup(ctx context.Context, actor audit.Actor, groupID string, contactIDs []string) (int64, error) {
	if len(contactIDs) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).
		Where("contact_group_id = ? AND contact_id IN ?", groupID, contactIDs).
		Delete(&domain.ContactGroupMember{})
	if res.Error != nil {
		return 0, store.Translate(res.Error)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "group.members_removed", TargetType: "contact_group", TargetID: groupID,
		Summary: fmt.Sprintf("removed %d contact(s)", res.RowsAffected),
	})
	return res.RowsAffected, nil
}

// GetGroup loads one group.
func (s *Service) GetGroup(ctx context.Context, id string) (*domain.ContactGroup, error) {
	var g domain.ContactGroup
	err := s.db.WithContext(ctx).First(&g, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &g, store.Translate(err)
}
