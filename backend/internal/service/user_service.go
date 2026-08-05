package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"

	"text-annotation-platform/internal/cache"
	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository/iface"
)

const userCacheTTL = 10 * time.Minute

// UserService handles user management operations.
type UserService struct {
	repo  iface.DBUserRepo
	cache *cache.Cache // nil = no Redis, always read from DB
}

// NewUserService creates a UserService backed by the given repo.
func NewUserService(repo iface.DBUserRepo) *UserService {
	return &UserService{repo: repo}
}

// WithCache injects the Redis cache; call from main.go after construction.
func (s *UserService) WithCache(c *cache.Cache) *UserService {
	s.cache = c
	return s
}

// userCacheKey returns the Redis key for a user by ID.
func userCacheKey(userID uint) string {
	return "user:" + strconv.FormatUint(uint64(userID), 10)
}

// FindUserByID returns the user with the given ID, using Redis cache when available.
func (s *UserService) FindUserByID(ctx context.Context, userID uint) (*dbmodel.User, error) {
	if s.cache != nil {
		var u dbmodel.User
		if hit, _ := s.cache.GetJSON(ctx, userCacheKey(userID), &u); hit {
			return &u, nil
		}
	}
	user, err := s.repo.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if s.cache != nil {
		s.cache.SetJSON(ctx, userCacheKey(userID), user, userCacheTTL)
	}
	return user, nil
}

// invalidateUser removes the cached user entry.
func (s *UserService) invalidateUser(ctx context.Context, userID uint) {
	if s.cache != nil {
		s.cache.Delete(ctx, userCacheKey(userID))
	}
}

// CreateUser creates a new user with the given parameters.
func (s *UserService) CreateUser(ctx context.Context, username, password, displayName, role string, email, employeeID *string) (*dbmodel.User, error) {
	if _, err := s.repo.FindUserByUsername(ctx, username); err == nil {
		return nil, errors.New("username already exists")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	user := &dbmodel.User{
		Username:     username,
		PasswordHash: string(hashedPassword),
		Role:         role,
		DisplayName:  displayName,
		Status:       "active",
		Email:        email,
		EmployeeID:   employeeID,
	}

	if err := s.repo.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	return user, nil
}

// ListUsers returns a paginated list of users.
func (s *UserService) ListUsers(ctx context.Context, page, pageSize int) ([]dbmodel.User, int64, error) {
	return s.repo.ListUsersWithCount(ctx, page, pageSize)
}

// UpdateUser updates a user's basic information.
func (s *UserService) UpdateUser(ctx context.Context, userID uint, displayName, email, employeeId *string) (*dbmodel.User, error) {
	user, err := s.repo.FindUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}
	if displayName != nil {
		user.DisplayName = *displayName
	}
	if email != nil {
		user.Email = email
	}
	if employeeId != nil {
		user.EmployeeID = employeeId
	}
	if err := s.repo.SaveUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}
	s.invalidateUser(ctx, userID)
	return user, nil
}

// UpdateRole sets the user's role to newRole.
func (s *UserService) UpdateRole(ctx context.Context, userID uint, newRole string) error {
	if !isAssignableRole(newRole) {
		return errors.New("invalid role")
	}
	n, err := s.repo.UpdateUserFields(ctx, userID, map[string]interface{}{"role": newRole})
	if err != nil {
		return fmt.Errorf("failed to update role: %w", err)
	}
	if n == 0 {
		return errors.New("user not found")
	}
	s.invalidateUser(ctx, userID)
	return nil
}

// UpdateStatus sets the user's status to newStatus.
func (s *UserService) UpdateStatus(ctx context.Context, userID uint, newStatus string) error {
	if newStatus != "active" && newStatus != "disabled" {
		return errors.New("invalid status: must be 'active' or 'disabled'")
	}
	n, err := s.repo.UpdateUserFields(ctx, userID, map[string]interface{}{"status": newStatus})
	if err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	if n == 0 {
		return errors.New("user not found")
	}
	s.invalidateUser(ctx, userID)
	return nil
}

// ResetPassword sets a new password for the user.
func (s *UserService) ResetPassword(ctx context.Context, userID uint, newPassword string) error {
	if len(newPassword) < 6 {
		return errors.New("password must be at least 6 characters")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	n, err := s.repo.UpdateUserFields(ctx, userID, map[string]interface{}{"password_hash": string(hashed)})
	if err != nil {
		return fmt.Errorf("failed to reset password: %w", err)
	}
	if n == 0 {
		return errors.New("user not found")
	}
	s.invalidateUser(ctx, userID)
	return nil
}

// DeleteUser removes a user by id (PH-12).
func (s *UserService) DeleteUser(ctx context.Context, userID uint) error {
	n, err := s.repo.DeleteUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}
	if n == 0 {
		return errors.New("user not found")
	}
	s.invalidateUser(ctx, userID)
	return nil
}

// isAssignableRole 允许从管理界面创建/指派的角色（含各模态细分角色）。
func isAssignableRole(role string) bool {
	switch role {
	case dbmodel.RoleAdmin, dbmodel.RoleAnnotator, dbmodel.RoleReviewer,
		dbmodel.RoleImageAnnotator, dbmodel.RoleImageReviewer,
		dbmodel.RoleAudioAnnotator, dbmodel.RoleAudioReviewer,
		dbmodel.RoleVideoAnnotator, dbmodel.RoleVideoReviewer:
		return true
	}
	return false
}
