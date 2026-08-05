package service

import (
	"context"
	"errors"
	"os"
	"testing"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// TestMain 为整个 service 测试包设置注册邀请码（PH-12：Register 改为 env 驱动）。
func TestMain(m *testing.M) {
	os.Setenv("REGISTER_INVITE_CODE", "admin123")
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Mock implementation of iface.DBUserRepo — no real DB required
// ---------------------------------------------------------------------------

type mockUserRepo struct {
	users map[string]*dbmodel.User // keyed by username
	byID  map[uint]*dbmodel.User

	// Controllable errors
	findByUsernameErr error
	createUserErr     error
	listErr           error
	findByIDErr       error
	saveErr           error
	updateFieldsErr   error
	updateFieldsRows  int64
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{
		users: make(map[string]*dbmodel.User),
		byID:  make(map[uint]*dbmodel.User),
	}
}

func (m *mockUserRepo) FindUserByUsername(_ context.Context, username string) (*dbmodel.User, error) {
	if m.findByUsernameErr != nil {
		return nil, m.findByUsernameErr
	}
	u, ok := m.users[username]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

func (m *mockUserRepo) FindUserByID(_ context.Context, id uint) (*dbmodel.User, error) {
	if m.findByIDErr != nil {
		return nil, m.findByIDErr
	}
	u, ok := m.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return u, nil
}

func (m *mockUserRepo) CreateUser(_ context.Context, user *dbmodel.User) error {
	if m.createUserErr != nil {
		return m.createUserErr
	}
	user.ID = uint(len(m.users) + 1)
	m.users[user.Username] = user
	m.byID[user.ID] = user
	return nil
}

func (m *mockUserRepo) ListUsersWithCount(_ context.Context, page, pageSize int) ([]dbmodel.User, int64, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var all []dbmodel.User
	for _, u := range m.users {
		all = append(all, *u)
	}
	return all, int64(len(all)), nil
}

func (m *mockUserRepo) SaveUser(_ context.Context, user *dbmodel.User) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.users[user.Username] = user
	m.byID[user.ID] = user
	return nil
}

func (m *mockUserRepo) UpdateUserFields(_ context.Context, id uint, updates map[string]interface{}) (int64, error) {
	if m.updateFieldsErr != nil {
		return 0, m.updateFieldsErr
	}
	return m.updateFieldsRows, nil
}

func (m *mockUserRepo) DeleteUser(_ context.Context, id uint) (int64, error) {
	if m.updateFieldsErr != nil {
		return 0, m.updateFieldsErr
	}
	return m.updateFieldsRows, nil
}

// ---------------------------------------------------------------------------
// AuthService tests
// ---------------------------------------------------------------------------

func TestAuthService_Login_Success(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewAuthService(repo, "test-secret-key")

	// Pre-create a user via Register
	if err := svc.Register(context.Background(), "alice", "password123", "admin123"); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	token, user, err := svc.Login(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if token == "" {
		t.Error("expected non-empty JWT token")
	}
	if user.Username != "alice" {
		t.Errorf("unexpected username: %s", user.Username)
	}
}

func TestAuthService_Login_WrongPassword(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewAuthService(repo, "test-secret-key")
	_ = svc.Register(context.Background(), "bob", "correctpass", "admin123")

	_, _, err := svc.Login(context.Background(), "bob", "wrongpass")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestAuthService_Login_UnknownUser(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewAuthService(repo, "test-secret-key")

	_, _, err := svc.Login(context.Background(), "nobody", "pass")
	if err == nil {
		t.Error("expected error for unknown user")
	}
}

func TestAuthService_ValidateToken_RoundTrip(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewAuthService(repo, "round-trip-secret")
	_ = svc.Register(context.Background(), "carol", "pass123", "admin123")

	token, user, _ := svc.Login(context.Background(), "carol", "pass123")
	uid, role, err := svc.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken failed: %v", err)
	}
	if uid != user.ID {
		t.Errorf("expected user_id %d, got %d", user.ID, uid)
	}
	if role != user.Role {
		t.Errorf("expected role %q, got %q", user.Role, role)
	}
}

func TestAuthService_Register_DuplicateUsername(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewAuthService(repo, "secret")
	_ = svc.Register(context.Background(), "dave", "pass123", "admin123")

	if err := svc.Register(context.Background(), "dave", "other123", "admin123"); err == nil {
		t.Error("expected error on duplicate username")
	}
}

// ---------------------------------------------------------------------------
// UserService tests
// ---------------------------------------------------------------------------

func TestUserService_CreateUser_Success(t *testing.T) {
	svc := NewUserService(newMockUserRepo())
	user, err := svc.CreateUser(context.Background(), "eve", "password123", "Eve Test", "annotator", nil, nil)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	if user.Username != "eve" {
		t.Errorf("unexpected username: %s", user.Username)
	}
}

func TestUserService_CreateUser_DuplicateUsername(t *testing.T) {
	svc := NewUserService(newMockUserRepo())
	_, _ = svc.CreateUser(context.Background(), "frank", "pass123", "Frank", "annotator", nil, nil)
	_, err := svc.CreateUser(context.Background(), "frank", "other123", "Frank2", "annotator", nil, nil)
	if err == nil {
		t.Error("expected error on duplicate username")
	}
}

func TestUserService_UpdateRole_InvalidRole(t *testing.T) {
	svc := NewUserService(newMockUserRepo())
	if err := svc.UpdateRole(context.Background(), 1, "superuser"); err == nil {
		t.Error("expected error for invalid role")
	}
}

func TestUserService_UpdateRole_UserNotFound(t *testing.T) {
	repo := newMockUserRepo()
	repo.updateFieldsRows = 0 // simulate no rows affected
	svc := NewUserService(repo)
	if err := svc.UpdateRole(context.Background(), 99, "admin"); err == nil {
		t.Error("expected error when user not found")
	}
}

func TestUserService_ResetPassword_TooShort(t *testing.T) {
	svc := NewUserService(newMockUserRepo())
	if err := svc.ResetPassword(context.Background(), 1, "123"); err == nil {
		t.Error("expected error for password < 6 chars")
	}
}

func TestUserService_ListUsers_NoRealDB(t *testing.T) {
	repo := newMockUserRepo()
	_ = repo.CreateUser(context.Background(), &dbmodel.User{Username: "u1", Role: "annotator"})
	_ = repo.CreateUser(context.Background(), &dbmodel.User{Username: "u2", Role: "admin"})

	svc := NewUserService(repo)
	users, total, err := svc.ListUsers(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}
