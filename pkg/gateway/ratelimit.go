package gateway

import (
	"os"
	"strconv"
	"sync"

	"golang.org/x/time/rate"
)

// EndpointLimiter applies optional global and per-identity token-bucket limits.
// A nil *EndpointLimiter allows all traffic.
type EndpointLimiter struct {
	global *rate.Limiter
	// userRPS is the per-identity sustained rate; zero disables per-identity limiting.
	userRPS   float64
	userBurst int
	users     sync.Map // string -> *rate.Limiter
}

// NewEndpointLimiter returns a limiter. Pass globalRPS<=0 to disable the global bucket,
// and userRPS<=0 to disable per-identity limiting (global may still apply).
func NewEndpointLimiter(globalRPS float64, globalBurst int, userRPS float64, userBurst int) *EndpointLimiter {
	var g *rate.Limiter
	if globalRPS > 0 && globalBurst > 0 {
		g = rate.NewLimiter(rate.Limit(globalRPS), globalBurst)
	}
	if userRPS <= 0 && g == nil {
		return nil
	}
	if userBurst <= 0 {
		userBurst = 1
	}
	return &EndpointLimiter{
		global:    g,
		userRPS:   userRPS,
		userBurst: userBurst,
	}
}

// Allow reports whether a request for identity key (e.g. OIDC sub) may proceed.
// When denied, scope is "global" or "user" for metrics/logging; otherwise scope is "".
func (e *EndpointLimiter) Allow(userKey string) (allowed bool, scope string) {
	if e == nil {
		return true, ""
	}
	if e.global != nil && !e.global.Allow() {
		return false, "global"
	}
	if e.userRPS <= 0 {
		return true, ""
	}
	limAny, _ := e.users.LoadOrStore(userKey, rate.NewLimiter(rate.Limit(e.userRPS), e.userBurst))
	if !limAny.(*rate.Limiter).Allow() {
		return false, "user"
	}
	return true, ""
}

// LoadEndpointLimiterFromEnv reads four env vars with the given prefix:
//
//	${prefix}GLOBAL_RPS, ${prefix}GLOBAL_BURST, ${prefix}PER_USER_RPS, ${prefix}PER_USER_BURST
//
// Missing or empty values default to 0 for RPS (unlimited) and 0 for burst (coerced in NewEndpointLimiter).
func LoadEndpointLimiterFromEnv(prefix string) *EndpointLimiter {
	globalRPS := parseFloatEnv(prefix + "GLOBAL_RPS")
	globalBurst := parseIntEnv(prefix + "GLOBAL_BURST")
	userRPS := parseFloatEnv(prefix + "PER_USER_RPS")
	userBurst := parseIntEnv(prefix + "PER_USER_BURST")
	return NewEndpointLimiter(globalRPS, globalBurst, userRPS, userBurst)
}

func parseFloatEnv(key string) float64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseIntEnv(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
