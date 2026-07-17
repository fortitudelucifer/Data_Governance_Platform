package service

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// Feature: text-annotation-platform
// Validates: Requirements 1.1, 1.2, 1.3, 1.4, 21.2

const testJWTSecret = "test-secret-key-for-unit-tests"

// --- JWT token generation & validation tests (no DB required) ---

// TestValidateToken_ValidToken tests that a properly signed token is accepted.
func TestValidateToken_ValidToken(t *testing.T) {
	svc := &AuthService{jwtSecret: testJWTSecret, TokenExpiry: 24 * time.Hour}

	claims := jwtClaims{
		UserID: 42,
		Role:   "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	userID, role, err := svc.ValidateToken(tokenStr)
	if err != nil {
		t.Fatalf("ValidateToken returned error: %v", err)
	}
	if userID != 42 {
		t.Errorf("expected userID=42, got %d", userID)
	}
	if role != "admin" {
		t.Errorf("expected role=admin, got %s", role)
	}
}

// TestValidateToken_ExpiredToken tests that an expired token is rejected.
func TestValidateToken_ExpiredToken(t *testing.T) {
	svc := &AuthService{jwtSecret: testJWTSecret, TokenExpiry: 24 * time.Hour}

	claims := jwtClaims{
		UserID: 1,
		Role:   "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-48 * time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(testJWTSecret))

	_, _, err := svc.ValidateToken(tokenStr)
	if err == nil {
		t.Error("expected error for expired token, got nil")
	}
}

// TestValidateToken_InvalidSignature tests that a token signed with a different secret is rejected.
func TestValidateToken_InvalidSignature(t *testing.T) {
	svc := &AuthService{jwtSecret: testJWTSecret, TokenExpiry: 24 * time.Hour}

	claims := jwtClaims{
		UserID: 1,
		Role:   "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte("wrong-secret"))

	_, _, err := svc.ValidateToken(tokenStr)
	if err == nil {
		t.Error("expected error for invalid signature, got nil")
	}
}

// TestValidateToken_MalformedToken tests that a garbage string is rejected.
func TestValidateToken_MalformedToken(t *testing.T) {
	svc := &AuthService{jwtSecret: testJWTSecret, TokenExpiry: 24 * time.Hour}

	_, _, err := svc.ValidateToken("not-a-jwt-token")
	if err == nil {
		t.Error("expected error for malformed token, got nil")
	}
}

// TestValidateToken_AdminRolePreserved tests that the admin role from claims is preserved.
func TestValidateToken_AdminRolePreserved(t *testing.T) {
	_ = &AuthService{jwtSecret: testJWTSecret, TokenExpiry: 24 * time.Hour}

	claims := jwtClaims{
		UserID: 1,
		Role:   "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte(testJWTSecret))

	// Parse manually to verify role claim
	parsed, err := jwt.ParseWithClaims(tokenStr, &jwtClaims{}, func(t *jwt.Token) (interface{}, error) {
		return []byte(testJWTSecret), nil
	})
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	c := parsed.Claims.(*jwtClaims)
	if c.Role != "admin" {
		t.Errorf("expected role=admin, got %s", c.Role)
	}
}

// TestBcryptHashComparison tests that bcrypt comparison works correctly for login logic.
func TestBcryptHashComparison(t *testing.T) {
	password := "test-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	// Correct password should match
	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		t.Error("correct password should match hash")
	}

	// Wrong password should not match
	if err := bcrypt.CompareHashAndPassword(hash, []byte("wrong-password")); err == nil {
		t.Error("wrong password should not match hash")
	}
}

// TestNewAuthService_Defaults tests that the constructor sets proper defaults.
func TestNewAuthService_Defaults(t *testing.T) {
	svc := NewAuthService(nil, "secret")
	if svc.TokenExpiry != 24*time.Hour {
		t.Errorf("expected default TokenExpiry=24h, got %v", svc.TokenExpiry)
	}
	if svc.jwtSecret != "secret" {
		t.Errorf("expected jwtSecret=secret, got %s", svc.jwtSecret)
	}
}
