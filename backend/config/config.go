package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// LLMConfig holds configuration for the LLM service.
type LLMConfig struct {
	Endpoint           string        // LLM service endpoint URL
	Model              string        // Model name to use
	MaxRetries         int           // Maximum number of retries on failure
	InitialBackoff     time.Duration // Initial backoff duration for retries
	BackoffFactor      float64       // Multiplier for exponential backoff
	MaxBackoff         time.Duration // Maximum backoff duration cap (provider retry)
	Timeout            time.Duration // HTTP request timeout for LLM calls
	MinProviderTimeout time.Duration // Floor for DB-provisioned provider timeouts
}

// MultiModalConfig captures the multi-modal P0 settings: object store, AI
// worker tuning and LiteLLM gateway endpoint. Defaults are sensible for
// dev-on-laptop and can all be overridden via env vars.
type MultiModalConfig struct {
	ObjectStoreDriver string        // "local" or "minio"
	ObjectStoreRoot   string        // root directory for the local driver
	// MinIO driver settings (only used when ObjectStoreDriver == "minio")
	MinIOEndpoint  string // host:port, e.g. "localhost:9000"
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	MinIOUseSSL    bool
	WorkerEnabled     bool          // whether to start the AIWorker goroutine
	WorkerInterval    time.Duration // poll interval
	WorkerBatch       int           // tasks leased per tick
	WorkerConcurrency int           // PH-11: batch-internal concurrency limit
	WorkerLeaseTTL    time.Duration // lease ttl per task
	AIRetryMax        int           // retry budget per AI run before HUMAN_PENDING
	AITimeout         time.Duration // per-call timeout for OCR/VLM adapters
	LiteLLMEndpoint   string        // base URL of the LiteLLM proxy
	LiteLLMAPIKey     string        // master key for the LiteLLM proxy
	OCREndpoint       string        // PaddleOCR adapter base URL
	OCRAPIKey         string        // PaddleOCR adapter shared secret (optional)
	SegEndpoint       string        // YOLOv8-seg adapter base URL
	SegAPIKey         string        // segmentation adapter shared secret (optional)
	SAMEndpoint           string        // MobileSAM interactive seg adapter base URL
	SAMAPIKey             string        // SAM adapter shared secret (optional)
	ASREndpoint           string        // FunASR ASR adapter base URL (asr-server, A2)
	ASRAPIKey             string        // ASR adapter shared secret (optional)
	DetEndpoint           string        // det-server base URL (YOLO26x + ByteTrack/BoT-SORT, video.detect_track, B2)
	DetAPIKey             string        // det adapter shared secret (optional)
	SAM2Endpoint          string        // sam2-video base URL (SAM2 跨帧传播, video.sam2_propagate, B2.2)
	SAM2APIKey            string        // sam2-video adapter shared secret (optional)
	AudioEndpoint         string        // qwen-audio base URL (Qwen2.5-Omni 整段转写, audio.transcribe)
	AudioAPIKey           string        // qwen-audio adapter shared secret (optional)
	SigLIPEndpoint        string        // SigLIP2 semantic router adapter base URL
	SigLIPAPIKey          string        // SigLIP2 adapter shared secret (optional)
	// GPUMaxQueue bounds how many callers may wait for a GPU slot (detect_track /
	// sam2_propagate) before new ones get 429 instead of piling up (B2.8 成本闸门).
	// 0 = unbounded (the old, unbounded-mutex behaviour).
	GPUMaxQueue int
	LiteLLMConfigPath     string        // path to litellm-config.yaml for inline editor
	DefaultProviderTimeoutSeconds int   // default LLMProvider.TimeoutSeconds for new providers via UI
	AIWorkerRetryMaxBackoff time.Duration // max backoff cap for AIWorker retry scheduling
	// T0.3 derived-asset pipeline (media-worker). FFmpeg/FFprobe paths default
	// to PATH lookup; on dev they point at tools/bin.
	MediaWorkerEnabled bool
	FFmpegPath         string
	FFprobePath        string
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	URL string // Redis URL, e.g. "redis://:password@localhost:6379/0"
	DB  int    // Redis logical database index (0–15); only applied when DBSet
	// DBSet reports whether REDIS_DB was given explicitly. Without it, an
	// unconditional `opt.DB = cfg.Redis.DB` would silently discard the database
	// selected in REDIS_URL's path — two environments pointed at .../0 and .../1
	// would then share one cache, and one would serve the other's rows.
	DBSet    bool
	Password string // Redis AUTH password (overrides URL password if set)
}

// Config holds all application configuration.
type Config struct {
	AppEnv            string           // "dev", "test", or "prod" (default "prod")
	DatabaseURL          string           // PostgreSQL connection URL (env DATABASE_URL)
	JWTSecret         string           // Secret key for JWT signing
	JWTExpireHours    int              // JWT token expiration in hours
	ServerPort        string           // HTTP server listen address
	AllowedOrigins    []string         // CORS allowlist; prod: required, dev: defaults to localhost:5173
	Timezone          string           // IANA timezone for user-facing time formatting (env TZ); default "Asia/Shanghai"
	DemoMode          bool             // Whether to return demo dashboard statistics
	DashboardCacheTTL time.Duration    // Dashboard cache TTL
	Redis             RedisConfig      // Redis cache settings; URL empty = cache disabled (dev fallback)
	LLM               LLMConfig        // LLM service configuration
	MultiModal        MultiModalConfig // Multi-modal P0 settings (image annotation track)
}

// LoadConfig reads configuration from environment variables with sensible defaults.
// Returns an error if the JWT secret is insecure in production mode (APP_ENV=prod).
func LoadConfig() (*Config, error) {
	appEnv := getEnv("APP_ENV", "prod")
	jwtSecret := os.Getenv("JWT_SECRET")

	allowedOrigins := getEnvStringSlice("ALLOWED_ORIGINS", nil)

	switch appEnv {
	case "prod":
		if jwtSecret == "" || jwtSecret == "default-secret-key" {
			return nil, fmt.Errorf("JWT_SECRET must be set to a strong non-default value when APP_ENV=prod")
		}
		if len(allowedOrigins) == 0 {
			return nil, fmt.Errorf("ALLOWED_ORIGINS must be set when APP_ENV=prod (comma-separated list of allowed origins)")
		}
	default: // "dev", "test"
		if jwtSecret == "" || jwtSecret == "default-secret-key" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return nil, fmt.Errorf("failed to generate ephemeral JWT secret: %w", err)
			}
			jwtSecret = hex.EncodeToString(b)
			log.Printf("[config] JWT secret randomly generated (ephemeral, restart will invalidate all sessions)")
		}
		if len(allowedOrigins) == 0 {
			allowedOrigins = []string{"http://localhost:5173"}
		}
	}

	return &Config{
		AppEnv: appEnv,
		// 关系库只有 Postgres 一种（执行方案-06 D1/M1）。默认值对准本地 dev 容器
		// data_governance_postgres（docker run … postgres:16，见 README/启动脚本）。
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/data_governance?sslmode=disable"),
		JWTSecret:         jwtSecret,
		JWTExpireHours:    getEnvInt("JWT_EXPIRE_HOURS", 24),
		ServerPort:        getEnv("SERVER_PORT", ":8280"),
		AllowedOrigins:    allowedOrigins,
		Timezone:          getEnv("TZ", "Asia/Shanghai"),
		DemoMode:          getEnvBool("DEMO_MODE", false),
		DashboardCacheTTL: getEnvDuration("DASHBOARD_CACHE_TTL", 5*time.Minute),
		Redis: RedisConfig{
			URL:      getEnv("REDIS_URL", ""),
			DB:       getEnvInt("REDIS_DB", 0),
			DBSet:    os.Getenv("REDIS_DB") != "",
			Password: getEnv("REDIS_PASSWORD", ""),
		},
		LLM: LLMConfig{
			Endpoint:           getEnv("LLM_ENDPOINT", "http://localhost:11434"),
			Model:              getEnv("LLM_MODEL", "qwen2.5"),
			MaxRetries:         getEnvInt("LLM_MAX_RETRIES", 3),
			InitialBackoff:     getEnvDuration("LLM_INITIAL_BACKOFF", time.Second),
			BackoffFactor:      getEnvFloat("LLM_BACKOFF_FACTOR", 2.0),
			MaxBackoff:         getEnvDuration("LLM_MAX_BACKOFF", 30*time.Second),
			Timeout:            getEnvDuration("LLM_TIMEOUT", 60*time.Second),
			MinProviderTimeout: getEnvDuration("LLM_MIN_PROVIDER_TIMEOUT", 180*time.Second),
		},
		MultiModal: MultiModalConfig{
			ObjectStoreDriver: getEnv("MM_OBJECT_STORE_DRIVER", "local"),
			ObjectStoreRoot:   getEnv("MM_OBJECT_STORE_ROOT", "./storage/assets"),
			MinIOEndpoint:     getEnv("MM_MINIO_ENDPOINT", ""),
			MinIOAccessKey:    getEnv("MM_MINIO_ACCESS_KEY", ""),
			MinIOSecretKey:    getEnv("MM_MINIO_SECRET_KEY", ""),
			MinIOBucket:       getEnv("MM_MINIO_BUCKET", "annotation-assets"),
			MinIOUseSSL:       getEnvBool("MM_MINIO_USE_SSL", false),
			WorkerEnabled:     getEnvBool("MM_WORKER_ENABLED", true),
			WorkerInterval:    getEnvDuration("MM_WORKER_INTERVAL", 2*time.Second),
			WorkerBatch:       getEnvInt("MM_WORKER_BATCH", 4),
			WorkerConcurrency: getEnvInt("MM_WORKER_CONCURRENCY", 4),
			WorkerLeaseTTL:    getEnvDuration("MM_WORKER_LEASE_TTL", 60*time.Second),
			MediaWorkerEnabled: getEnvBool("MM_MEDIA_WORKER_ENABLED", true),
			FFmpegPath:         getEnv("MM_FFMPEG_PATH", ""),
			FFprobePath:        getEnv("MM_FFPROBE_PATH", ""),
			AIRetryMax:        getEnvInt("MM_AI_RETRY_MAX", 2),
			AITimeout:         getEnvDuration("MM_AI_TIMEOUT", 90*time.Second),
			LiteLLMEndpoint:   getEnv("MM_LITELLM_ENDPOINT", ""),
			LiteLLMAPIKey:     getEnv("MM_LITELLM_API_KEY", ""),
			OCREndpoint:       getEnv("MM_OCR_ENDPOINT", ""),
			OCRAPIKey:         getEnv("MM_OCR_API_KEY", ""),
			SegEndpoint:       getEnv("MM_SEG_ENDPOINT", ""),
			SegAPIKey:         getEnv("MM_SEG_API_KEY", ""),
			SAMEndpoint:           getEnv("MM_SAM_ENDPOINT", ""),
			SAMAPIKey:             getEnv("MM_SAM_API_KEY", ""),
			ASREndpoint:           getEnv("MM_ASR_ENDPOINT", ""),
			ASRAPIKey:             getEnv("MM_ASR_API_KEY", ""),
			DetEndpoint:           getEnv("MM_DET_ENDPOINT", ""),
			DetAPIKey:             getEnv("MM_DET_API_KEY", ""),
			SAM2Endpoint:          getEnv("MM_SAM2_ENDPOINT", ""),
			SAM2APIKey:            getEnv("MM_SAM2_API_KEY", ""),
			AudioEndpoint:         getEnv("MM_AUDIO_ENDPOINT", ""),
			AudioAPIKey:           getEnv("MM_AUDIO_API_KEY", ""),
			GPUMaxQueue:           getEnvInt("MM_GPU_MAX_QUEUE", 4),
			SigLIPEndpoint:        getEnv("MM_SIGLIP_ENDPOINT", ""),
			SigLIPAPIKey:          getEnv("MM_SIGLIP_API_KEY", ""),
			LiteLLMConfigPath:     getEnv("MM_LITELLM_CONFIG_PATH", ""),
			DefaultProviderTimeoutSeconds: getEnvInt("MM_DEFAULT_PROVIDER_TIMEOUT_SECONDS", 90),
			AIWorkerRetryMaxBackoff:       getEnvDuration("MM_AI_WORKER_RETRY_MAX_BACKOFF", 30*time.Second),
		},
	}, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return defaultVal
}

func getEnvStringSlice(key string, defaultVal []string) []string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	parts := strings.Split(val, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return defaultVal
	}
	return result
}
