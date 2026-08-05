package config

import (
	"os"
	"testing"
	"time"
)

// clearBaseEnv unsets all env vars touched by these tests to avoid cross-test leakage.
func clearBaseEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"APP_ENV",
		"DATABASE_URL", "JWT_SECRET",
		"JWT_EXPIRE_HOURS", "SERVER_PORT", "LLM_ENDPOINT", "LLM_MODEL",
		"LLM_MAX_RETRIES", "LLM_INITIAL_BACKOFF", "LLM_BACKOFF_FACTOR",
		"LLM_MAX_BACKOFF", "LLM_TIMEOUT", "ALLOWED_ORIGINS",
	}
	for _, k := range keys {
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			os.Unsetenv(k)
		}
	})
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "dev") // dev mode: no JWT_SECRET required, random key generated

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DatabaseURL != "postgres://postgres:postgres@localhost:5432/data_governance?sslmode=disable" {
		t.Errorf("unexpected database URL default: %s", cfg.DatabaseURL)
	}
	// In dev mode with no JWT_SECRET, a random key is generated — must be non-empty and not the insecure default.
	if cfg.JWTSecret == "" {
		t.Error("expected non-empty JWTSecret")
	}
	if cfg.JWTSecret == "default-secret-key" {
		t.Error("JWTSecret must not be the insecure default in dev mode")
	}
	if cfg.JWTExpireHours != 24 {
		t.Errorf("unexpected JWTExpireHours: %d", cfg.JWTExpireHours)
	}
	if cfg.ServerPort != ":8280" {
		t.Errorf("unexpected ServerPort: %s", cfg.ServerPort)
	}
	if cfg.LLM.Endpoint != "http://localhost:11434" {
		t.Errorf("unexpected LLM.Endpoint: %s", cfg.LLM.Endpoint)
	}
	if cfg.LLM.Model != "qwen2.5" {
		t.Errorf("unexpected LLM.Model: %s", cfg.LLM.Model)
	}
	if cfg.LLM.MaxRetries != 3 {
		t.Errorf("unexpected LLM.MaxRetries: %d", cfg.LLM.MaxRetries)
	}
	if cfg.LLM.InitialBackoff != time.Second {
		t.Errorf("unexpected LLM.InitialBackoff: %v", cfg.LLM.InitialBackoff)
	}
	if cfg.LLM.BackoffFactor != 2.0 {
		t.Errorf("unexpected LLM.BackoffFactor: %f", cfg.LLM.BackoffFactor)
	}
	if cfg.LLM.MaxBackoff != 30*time.Second {
		t.Errorf("unexpected LLM.MaxBackoff: %v", cfg.LLM.MaxBackoff)
	}
	if cfg.LLM.Timeout != 60*time.Second {
		t.Errorf("unexpected LLM.Timeout: %v", cfg.LLM.Timeout)
	}
}

func TestLoadConfig_FromEnv(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	os.Setenv("DATABASE_URL", "custom-dsn")
	os.Setenv("JWT_SECRET", "my-secret")
	os.Setenv("ALLOWED_ORIGINS", "https://app.example.com")
	os.Setenv("JWT_EXPIRE_HOURS", "48")
	os.Setenv("SERVER_PORT", ":9090")
	os.Setenv("LLM_ENDPOINT", "http://remote:8080")
	os.Setenv("LLM_MODEL", "gpt-4")
	os.Setenv("LLM_MAX_RETRIES", "5")
	os.Setenv("LLM_INITIAL_BACKOFF", "2s")
	os.Setenv("LLM_BACKOFF_FACTOR", "3.0")
	os.Setenv("LLM_MAX_BACKOFF", "1m")
	os.Setenv("LLM_TIMEOUT", "2m")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DatabaseURL != "custom-dsn" {
		t.Errorf("unexpected DatabaseURL: %s", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != "my-secret" {
		t.Errorf("unexpected JWTSecret: %s", cfg.JWTSecret)
	}
	if cfg.JWTExpireHours != 48 {
		t.Errorf("unexpected JWTExpireHours: %d", cfg.JWTExpireHours)
	}
	if cfg.ServerPort != ":9090" {
		t.Errorf("unexpected ServerPort: %s", cfg.ServerPort)
	}
	if cfg.LLM.Endpoint != "http://remote:8080" {
		t.Errorf("unexpected LLM.Endpoint: %s", cfg.LLM.Endpoint)
	}
	if cfg.LLM.Model != "gpt-4" {
		t.Errorf("unexpected LLM.Model: %s", cfg.LLM.Model)
	}
	if cfg.LLM.MaxRetries != 5 {
		t.Errorf("unexpected LLM.MaxRetries: %d", cfg.LLM.MaxRetries)
	}
	if cfg.LLM.InitialBackoff != 2*time.Second {
		t.Errorf("unexpected LLM.InitialBackoff: %v", cfg.LLM.InitialBackoff)
	}
	if cfg.LLM.BackoffFactor != 3.0 {
		t.Errorf("unexpected LLM.BackoffFactor: %f", cfg.LLM.BackoffFactor)
	}
	if cfg.LLM.MaxBackoff != time.Minute {
		t.Errorf("unexpected LLM.MaxBackoff: %v", cfg.LLM.MaxBackoff)
	}
	if cfg.LLM.Timeout != 2*time.Minute {
		t.Errorf("unexpected LLM.Timeout: %v", cfg.LLM.Timeout)
	}
}

func TestLoadConfig_InvalidEnvFallsBackToDefault(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "dev") // dev mode so missing JWT_SECRET doesn't fatal
	os.Setenv("JWT_EXPIRE_HOURS", "not-a-number")
	os.Setenv("LLM_MAX_RETRIES", "invalid")
	os.Setenv("LLM_BACKOFF_FACTOR", "bad")
	os.Setenv("LLM_INITIAL_BACKOFF", "not-duration")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.JWTExpireHours != 24 {
		t.Errorf("expected default JWTExpireHours 24, got %d", cfg.JWTExpireHours)
	}
	if cfg.LLM.MaxRetries != 3 {
		t.Errorf("expected default MaxRetries 3, got %d", cfg.LLM.MaxRetries)
	}
	if cfg.LLM.BackoffFactor != 2.0 {
		t.Errorf("expected default BackoffFactor 2.0, got %f", cfg.LLM.BackoffFactor)
	}
	if cfg.LLM.InitialBackoff != time.Second {
		t.Errorf("expected default InitialBackoff 1s, got %v", cfg.LLM.InitialBackoff)
	}
}

// --- TD-18: JWT_SECRET validation ---

func TestLoadConfig_JWT_ProdRejectsEmptySecret(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	// JWT_SECRET intentionally not set

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error when JWT_SECRET is empty in prod mode")
	}
}

func TestLoadConfig_JWT_ProdRejectsDefaultSecret(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	os.Setenv("JWT_SECRET", "default-secret-key")

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error when JWT_SECRET is the insecure default in prod mode")
	}
}

func TestLoadConfig_JWT_ProdAcceptsValidSecret(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	os.Setenv("JWT_SECRET", "a-strong-secret-for-testing-only")
	os.Setenv("ALLOWED_ORIGINS", "https://app.example.com")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error with valid secret in prod mode: %v", err)
	}
	if cfg.JWTSecret != "a-strong-secret-for-testing-only" {
		t.Errorf("unexpected JWTSecret: %s", cfg.JWTSecret)
	}
	if cfg.AppEnv != "prod" {
		t.Errorf("unexpected AppEnv: %s", cfg.AppEnv)
	}
}

func TestLoadConfig_JWT_DevGeneratesRandomSecret(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "dev")
	// JWT_SECRET intentionally not set

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error in dev mode without JWT_SECRET: %v", err)
	}
	if cfg.JWTSecret == "" {
		t.Error("expected non-empty generated JWT secret in dev mode")
	}
	if cfg.JWTSecret == "default-secret-key" {
		t.Error("JWT secret must not be the insecure default in dev mode")
	}
	// Two calls should produce different secrets (ephemeral)
	cfg2, _ := LoadConfig()
	if cfg.JWTSecret == cfg2.JWTSecret {
		t.Error("expected different ephemeral secrets across calls")
	}
}

// --- TD-19: ALLOWED_ORIGINS validation ---

func TestLoadConfig_CORS_DevDefaultsToLocalhost(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "dev")
	os.Setenv("JWT_SECRET", "any-secret")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "http://localhost:5173" {
		t.Errorf("expected default AllowedOrigins [http://localhost:5173], got %v", cfg.AllowedOrigins)
	}
}

func TestLoadConfig_CORS_DevReadsEnv(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "dev")
	os.Setenv("JWT_SECRET", "any-secret")
	os.Setenv("ALLOWED_ORIGINS", "http://localhost:5173,http://localhost:3000")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.AllowedOrigins) != 2 {
		t.Fatalf("expected 2 origins, got %d: %v", len(cfg.AllowedOrigins), cfg.AllowedOrigins)
	}
	if cfg.AllowedOrigins[0] != "http://localhost:5173" || cfg.AllowedOrigins[1] != "http://localhost:3000" {
		t.Errorf("unexpected AllowedOrigins: %v", cfg.AllowedOrigins)
	}
}

func TestLoadConfig_CORS_ProdRejectsEmptyOrigins(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	os.Setenv("JWT_SECRET", "a-strong-secret-for-testing-only")
	// ALLOWED_ORIGINS intentionally not set

	_, err := LoadConfig()
	if err == nil {
		t.Error("expected error when ALLOWED_ORIGINS is empty in prod mode")
	}
}

func TestLoadConfig_CORS_ProdAcceptsOrigins(t *testing.T) {
	clearBaseEnv(t)
	os.Setenv("APP_ENV", "prod")
	os.Setenv("JWT_SECRET", "a-strong-secret-for-testing-only")
	os.Setenv("ALLOWED_ORIGINS", "https://app.example.com")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "https://app.example.com" {
		t.Errorf("unexpected AllowedOrigins: %v", cfg.AllowedOrigins)
	}
}

// REDIS_URL 路径里的 db 不能被静默丢掉。此前 main.go 无条件 `opt.DB = cfg.Redis.DB`
// （默认 0），于是 redis://host:6379/1 变成 db 0——两个环境共用一份缓存，其中一个
// 会读到另一个的行。这是 e2e 净环境自检时撞出来的：净后端的 /assets/1 返回了开发
// 库的 1.png。
func TestLoadConfig_RedisDBOnlyOverriddenWhenExplicit(t *testing.T) {
	t.Run("未设 REDIS_DB → DBSet=false，URL 里的 db 说了算", func(t *testing.T) {
		t.Setenv("APP_ENV", "dev")
		t.Setenv("REDIS_URL", "redis://localhost:6379/1")
		os.Unsetenv("REDIS_DB")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Redis.DBSet {
			t.Error("未设 REDIS_DB 时 DBSet 应为 false（否则会覆盖 URL 里的 db）")
		}
	})

	t.Run("显式设了 REDIS_DB → DBSet=true 且取该值", func(t *testing.T) {
		t.Setenv("APP_ENV", "dev")
		t.Setenv("REDIS_URL", "redis://localhost:6379/1")
		t.Setenv("REDIS_DB", "3")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Redis.DBSet || cfg.Redis.DB != 3 {
			t.Errorf("DBSet=%v DB=%d, want true/3", cfg.Redis.DBSet, cfg.Redis.DB)
		}
	})

	// REDIS_DB=0 是合法的显式选择，不能被当成「没设」
	t.Run("REDIS_DB=0 也算显式设置", func(t *testing.T) {
		t.Setenv("APP_ENV", "dev")
		t.Setenv("REDIS_DB", "0")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Redis.DBSet {
			t.Error("REDIS_DB=0 是显式选择 db 0，DBSet 应为 true")
		}
	})
}
