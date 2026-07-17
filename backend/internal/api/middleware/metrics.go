package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PH-6：Prometheus 指标。用路由模板(c.FullPath)做 label 避免高基数
// （/tasks/:id 而非 /tasks/123）。
var (
	httpRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP 请求总数（按方法 / 路由 / 状态码）。",
	}, []string{"method", "route", "status"})

	httpDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP 请求耗时（秒）。",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	httpInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "当前并发处理中的 HTTP 请求数。",
	})
)

// MetricsMiddleware 记录每个请求的计数 / 耗时 / 并发度。
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/metrics" {
			c.Next()
			return
		}
		httpInFlight.Inc()
		start := time.Now()
		c.Next()
		httpInFlight.Dec()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		httpRequests.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Inc()
		httpDuration.WithLabelValues(c.Request.Method, route).Observe(time.Since(start).Seconds())
	}
}

// MetricsHandler 暴露 Prometheus /metrics（含上面三项 + Go runtime / 进程指标）。
// 生产应置于内网 / 防火墙后，仅供 Prometheus 抓取。
func MetricsHandler() gin.HandlerFunc {
	return gin.WrapH(promhttp.Handler())
}
