package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	dbmodel "text-annotation-platform/internal/model/relational"
	"text-annotation-platform/internal/repository/iface"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// AuthService handles user authentication and JWT token management.
type AuthService struct {
	repo      iface.DBUserRepo
	jwtSecret string
	// TokenExpiry controls how long issued tokens remain valid.
	// Defaults to 24 hours if not set explicitly.
	TokenExpiry time.Duration
	// authCache 缓存 userID → 实时角色/状态（PH-5），短 TTL + 显式失效。
	authCache sync.Map
}

// NewAuthService creates an AuthService with the given repository and JWT secret.
func NewAuthService(repo iface.DBUserRepo, jwtSecret string) *AuthService {
	return &AuthService{
		repo:        repo,
		jwtSecret:   jwtSecret,
		TokenExpiry: 24 * time.Hour,
	}
}

// jwtClaims defines the custom JWT claims used by the platform.
type jwtClaims struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Register creates a new user with the given username and password.
// The password is automatically hashed before storing. Default role is 'annotator'.
func (s *AuthService) Register(ctx context.Context, username, password, inviteCode string) error {
	// 0. 校验邀请码：从环境变量读取；未配置则注册关闭（移除硬编码 admin123，PH-12）。
	want := os.Getenv("REGISTER_INVITE_CODE")
	if want == "" || inviteCode != want {
		return errors.New("注册邀请码错误或注册未开放 / invalid invite code or registration closed")
	}

	// 1. Check if user currently exists
	_, err := s.repo.FindUserByUsername(ctx, username)
	if err == nil {
		return errors.New("用户名已存在 / Username already exists")
	}

	// 2. Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// 3. Create user struct
	newUser := &dbmodel.User{
		Username:     username,
		PasswordHash: string(hashedPassword),
		Role:         "annotator", // By default all fresh registrations are annotators
	}

	// 4. Persist
	if err := s.repo.CreateUser(ctx, newUser); err != nil {
		return fmt.Errorf("create user failed: %w", err)
	}

	return nil
}

// Login verifies the username and password, returning a JWT token and user info on success.
// In the current single-user scenario the role stored in the DB is used; the
// default admin user is expected to have role "admin".
func (s *AuthService) Login(ctx context.Context, username, password string) (string, *dbmodel.User, error) {
	user, err := s.repo.FindUserByUsername(ctx, username)
	if err != nil {
		return "", nil, errors.New("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", nil, errors.New("invalid username or password")
	}

	// Check if user is disabled
	if user.Status == "disabled" {
		return "", nil, errors.New("账号已被封停")
	}

	now := time.Now()
	claims := jwtClaims{
		UserID: user.ID,
		Role:   user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.TokenExpiry)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(s.jwtSecret))
	if err != nil {
		return "", nil, fmt.Errorf("failed to sign token: %w", err)
	}

	return tokenStr, user, nil
}

// ValidateToken parses and validates a JWT token string, returning the userID
// and role embedded in the claims. Returns an error for expired, malformed, or
// otherwise invalid tokens.
func (s *AuthService) ValidateToken(tokenStr string) (uint, string, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &jwtClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(s.jwtSecret), nil
	})
	if err != nil {
		return 0, "", fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*jwtClaims)
	if !ok || !token.Valid {
		return 0, "", errors.New("invalid token claims")
	}

	return claims.UserID, claims.Role, nil
}

// liveAuthTTL 限定每请求"实时角色/状态"检查的最大陈旧度。
const liveAuthTTL = 30 * time.Second

type authEntry struct {
	role   string
	active bool
	exp    time.Time
}

// CheckActive 返回用户当前角色 + 是否启用，带 30s 内存缓存。
// PH-5：把"封停/降级后旧 token 仍有效"的真空从 token 有效期(24h)收敛到 ≤30s；
// 配合 InvalidateAuth（改状态/角色时调用）可即时生效。用户不存在返回 error。
func (s *AuthService) CheckActive(ctx context.Context, userID uint) (string, bool, error) {
	if v, ok := s.authCache.Load(userID); ok {
		if e := v.(authEntry); time.Now().Before(e.exp) {
			return e.role, e.active, nil
		}
	}
	user, err := s.repo.FindUserByID(ctx, userID)
	if err != nil {
		return "", false, err
	}
	active := user.Status != "disabled"
	s.authCache.Store(userID, authEntry{role: user.Role, active: active, exp: time.Now().Add(liveAuthTTL)})
	return user.Role, active, nil
}

// InvalidateAuth 清除某用户的鉴权缓存，使状态/角色变更下次请求即时生效。
func (s *AuthService) InvalidateAuth(userID uint) {
	s.authCache.Delete(userID)
}
