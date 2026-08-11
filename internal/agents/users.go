package agents

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

// UserFilter narrows a user listing.
type UserFilter struct {
	Query        string
	DepartmentID string
	Role         string
	Status       string
	QueueID      string
}

// ListUsers returns a page of staff users.
func (s *Service) ListUsers(ctx context.Context, f UserFilter, page store.Page) (store.Result[domain.User], error) {
	q := s.db.WithContext(ctx).Model(&domain.User{}).Preload("Department")

	if f.Query != "" {
		like := "%" + store.LikeEscape(f.Query) + "%"
		q = q.Where("name LIKE ? OR email LIKE ?", like, like)
	}
	if f.DepartmentID != "" {
		q = q.Where("department_id = ?", f.DepartmentID)
	}
	if f.Role != "" {
		q = q.Where("role = ?", f.Role)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.QueueID != "" {
		q = q.Where("id IN (SELECT user_id FROM queue_members WHERE queue_id = ?)", f.QueueID)
	}

	var rows []domain.User
	return store.Paginate(q, page, map[string]string{
		"name":      "name",
		"email":     "email",
		"role":      "role",
		"status":    "status",
		"createdAt": "created_at",
		"lastLogin": "last_login_at",
	}, "name", &rows)
}

// GetUser loads one user with their queues.
func (s *Service) GetUser(ctx context.Context, id string) (*domain.User, error) {
	var u domain.User
	err := s.db.WithContext(ctx).Preload("Department").Preload("Queues").First(&u, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &u, store.Translate(err)
}

// InviteInput describes a new staff invitation.
type InviteInput struct {
	Email           string
	Name            string
	Title           string
	Phone           string
	Role            domain.Role
	DepartmentID    string
	CrossDepartment bool
	QueueIDs        []string
}

// InviteUser creates a staff record that a C2 identity can bind to on first
// login. There is no local password to set — the invite exists purely so the
// deny-by-default check in resolveStaffUser has something to match.
func (s *Service) InviteUser(ctx context.Context, actor audit.Actor, in InviteInput) (*domain.User, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%w: a valid email is required", ErrInvalidInput)
	}
	if in.Role == "" {
		in.Role = domain.RoleAgent
	}
	if !in.Role.Valid() {
		return nil, fmt.Errorf("%w: unknown role %q", ErrInvalidInput, in.Role)
	}

	existing := domain.User{}
	err := s.db.WithContext(ctx).Where("LOWER(email) = ?", email).First(&existing).Error
	if err == nil {
		return nil, fmt.Errorf("%w: a user with that email already exists", ErrConflict)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, store.Translate(err)
	}

	now := time.Now().UTC()
	user := domain.User{
		Email:           email,
		Name:            strings.TrimSpace(in.Name),
		Title:           in.Title,
		Phone:           in.Phone,
		Status:          domain.UserInvited,
		Role:            in.Role,
		DepartmentID:    in.DepartmentID,
		CrossDepartment: in.CrossDepartment,
		InvitedByID:     actor.ID,
		InvitedAt:       &now,
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := s.setQueueMembership(ctx, tx, user.ID, in.QueueIDs); err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "user.invited", TargetType: "user", TargetID: user.ID,
			Summary: "invited " + email + " as " + string(in.Role),
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}
	return &user, nil
}

// UpdateUserInput carries an admin edit. Nil fields are left unchanged.
type UpdateUserInput struct {
	Name            *string
	Title           *string
	Phone           *string
	Role            *domain.Role
	DepartmentID    *string
	CrossDepartment *bool
	Status          *domain.UserStatus
	QueueIDs        *[]string
}

// UpdateUser applies an admin edit to a staff user.
func (s *Service) UpdateUser(ctx context.Context, actor audit.Actor, id string, in UpdateUserInput) (*domain.User, error) {
	user, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}

	changes := domain.JSONMap{}
	updates := map[string]any{}

	if in.Name != nil && *in.Name != user.Name {
		updates["name"], changes["name"] = *in.Name, []string{user.Name, *in.Name}
	}
	if in.Title != nil {
		updates["title"] = *in.Title
	}
	if in.Phone != nil {
		updates["phone"] = *in.Phone
	}
	if in.Role != nil && *in.Role != user.Role {
		if !in.Role.Valid() {
			return nil, fmt.Errorf("%w: unknown role %q", ErrInvalidInput, *in.Role)
		}
		if user.Role == domain.RoleAdmin && *in.Role != domain.RoleAdmin {
			if err := s.guardLastAdmin(ctx, user.ID); err != nil {
				return nil, err
			}
		}
		updates["role"], changes["role"] = *in.Role, []string{string(user.Role), string(*in.Role)}
	}
	if in.DepartmentID != nil && *in.DepartmentID != user.DepartmentID {
		updates["department_id"] = *in.DepartmentID
		changes["departmentId"] = []string{user.DepartmentID, *in.DepartmentID}
	}
	if in.CrossDepartment != nil {
		updates["cross_department"] = *in.CrossDepartment
	}
	if in.Status != nil && *in.Status != user.Status {
		if user.Role == domain.RoleAdmin && *in.Status != domain.UserActive {
			if err := s.guardLastAdmin(ctx, user.ID); err != nil {
				return nil, err
			}
		}
		updates["status"], changes["status"] = *in.Status, []string{string(user.Status), string(*in.Status)}
	}

	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if len(updates) > 0 {
			if err := tx.Model(&domain.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				return err
			}
		}
		if in.QueueIDs != nil {
			if err := s.setQueueMembership(ctx, tx, id, *in.QueueIDs); err != nil {
				return err
			}
		}
		if len(changes) == 0 {
			return nil
		}
		return s.audit.RecordTx(ctx, tx, actor, audit.Entry{
			Action: "user.updated", TargetType: "user", TargetID: id,
			Summary: "updated " + user.Email, Changes: changes,
		})
	})
	if err != nil {
		return nil, store.Translate(err)
	}

	// Suspension must take effect immediately, not at session expiry.
	if in.Status != nil && *in.Status == domain.UserSuspended {
		if _, err := s.RevokeUserSessions(ctx, id, "suspended"); err != nil {
			s.log.WarnContext(ctx, "could not revoke sessions for suspended user", "user", id, "error", err)
		}
	}
	return s.GetUser(ctx, id)
}

// guardLastAdmin refuses a change that would leave the deployment with no
// active admin. With C2 SSO as the only login there is no password reset to
// fall back on: recovery would mean a `ccadm` shell on the server.
func (s *Service) guardLastAdmin(ctx context.Context, excludingID string) error {
	var n int64
	err := s.db.WithContext(ctx).Model(&domain.User{}).
		Where("role = ? AND status = ? AND id <> ?", domain.RoleAdmin, domain.UserActive, excludingID).
		Count(&n).Error
	if err != nil {
		return store.Translate(err)
	}
	if n == 0 {
		return ErrLastAdmin
	}
	return nil
}

func (s *Service) setQueueMembership(ctx context.Context, tx *gorm.DB, userID string, queueIDs []string) error {
	if err := tx.WithContext(ctx).Where("user_id = ?", userID).Delete(&domain.QueueMember{}).Error; err != nil {
		return err
	}
	if len(queueIDs) == 0 {
		return nil
	}
	rows := make([]domain.QueueMember, 0, len(queueIDs))
	now := time.Now().UTC()
	for _, qid := range queueIDs {
		if qid == "" {
			continue
		}
		rows = append(rows, domain.QueueMember{QueueID: qid, UserID: userID, JoinedAt: now})
	}
	if len(rows) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Create(&rows).Error
}

// ---------------------------------------------------------------------------
// Departments
// ---------------------------------------------------------------------------

// ListDepartments returns every department, ordered for display.
func (s *Service) ListDepartments(ctx context.Context, includeInactive bool) ([]domain.Department, error) {
	q := s.db.WithContext(ctx).Model(&domain.Department{})
	if !includeInactive {
		q = q.Where("active = ?", true)
	}
	var out []domain.Department
	err := q.Order("sort_order ASC, name ASC").Find(&out).Error
	return out, store.Translate(err)
}

// GetDepartment loads one department.
func (s *Service) GetDepartment(ctx context.Context, id string) (*domain.Department, error) {
	var d domain.Department
	err := s.db.WithContext(ctx).First(&d, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &d, store.Translate(err)
}

// SaveDepartment creates or updates a department.
func (s *Service) SaveDepartment(ctx context.Context, actor audit.Actor, d *domain.Department) (*domain.Department, error) {
	d.Code = strings.ToUpper(strings.TrimSpace(d.Code))
	if d.Code == "" || strings.TrimSpace(d.Name) == "" {
		return nil, fmt.Errorf("%w: department code and name are required", ErrInvalidInput)
	}

	action := "department.created"
	if d.ID != "" {
		action = "department.updated"
	}
	if err := s.db.WithContext(ctx).Save(d).Error; err != nil {
		return nil, store.Translate(err)
	}

	s.audit.Record(ctx, actor, audit.Entry{
		Action: action, TargetType: "department", TargetID: d.ID, Summary: d.Name,
	})
	return d, nil
}

// DeleteDepartment soft-deletes a department that holds no users, queues or
// service types.
func (s *Service) DeleteDepartment(ctx context.Context, actor audit.Actor, id string) error {
	var users, queues, types int64
	s.db.WithContext(ctx).Model(&domain.User{}).Where("department_id = ?", id).Count(&users)
	s.db.WithContext(ctx).Model(&domain.Queue{}).Where("department_id = ?", id).Count(&queues)
	s.db.WithContext(ctx).Model(&domain.ServiceType{}).Where("department_id = ?", id).Count(&types)

	if users+queues+types > 0 {
		return fmt.Errorf("%w: department still has %d user(s), %d queue(s) and %d service type(s)",
			ErrConflict, users, queues, types)
	}
	if err := s.db.WithContext(ctx).Delete(&domain.Department{}, "id = ?", id).Error; err != nil {
		return store.Translate(err)
	}
	s.audit.Record(ctx, actor, audit.Entry{
		Action: "department.deleted", TargetType: "department", TargetID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

// Bootstrap grants the admin role to the configured C2 subjects.
//
// This is the only route to a first admin. With C2 SSO as the sole staff
// login, nobody can sign in to grant anybody else a role, so a fresh
// deployment is otherwise permanently locked out — the classic day-one
// blocker. It is idempotent and safe to run on every boot.
func (s *Service) Bootstrap(ctx context.Context) error {
	for _, sub := range s.cfg.BootstrapAdminSubs {
		sub = strings.TrimSpace(sub)
		if sub == "" {
			continue
		}

		var user domain.User
		err := s.db.WithContext(ctx).Where("c2_sub = ?", sub).First(&user).Error
		switch {
		case err == nil:
			if user.Role != domain.RoleAdmin || user.Status == domain.UserSuspended {
				s.db.WithContext(ctx).Model(&user).Updates(map[string]any{
					"role": domain.RoleAdmin, "status": domain.UserActive,
				})
				s.log.InfoContext(ctx, "bootstrap promoted existing user to admin", "sub", sub)
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			user = domain.User{
				C2Sub:           sub,
				Email:           fmt.Sprintf("bootstrap+%s@cityconnect.local", shortID(sub)),
				Name:            "Bootstrap Administrator",
				Status:          domain.UserInvited,
				Role:            domain.RoleAdmin,
				CrossDepartment: true,
			}
			if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
				return fmt.Errorf("agents: bootstrap admin %s: %w", sub, err)
			}
			s.log.InfoContext(ctx, "bootstrap created admin user", "sub", sub, "user_id", user.ID)
		default:
			return store.Translate(err)
		}
	}

	for _, email := range s.cfg.BootstrapAdminEmails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email == "" || !strings.Contains(email, "@") {
			continue
		}

		var user domain.User
		err := s.db.WithContext(ctx).Where("LOWER(email) = ?", email).First(&user).Error
		switch {
		case err == nil:
			// Promote, but never reactivate a suspended account: an admin
			// suspended it deliberately, and configuration should not
			// silently undo that.
			if user.Role != domain.RoleAdmin && user.Status != domain.UserSuspended {
				s.db.WithContext(ctx).Model(&user).Updates(map[string]any{
					"role": domain.RoleAdmin, "cross_department": true,
				})
				s.log.InfoContext(ctx, "bootstrap promoted an existing user to admin", "email", email)
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			now := time.Now().UTC()
			user = domain.User{
				Email: email, Name: "Administrator",
				Status: domain.UserInvited, Role: domain.RoleAdmin,
				CrossDepartment: true, InvitedAt: &now,
			}
			if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
				return fmt.Errorf("agents: bootstrap admin invitation for %s: %w", email, err)
			}
			s.log.InfoContext(ctx, "bootstrap created an admin invitation",
				"email", email,
				"note", "it binds to a C2 subject on first sign-in")
		default:
			return store.Translate(err)
		}
	}
	return nil
}

func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}
