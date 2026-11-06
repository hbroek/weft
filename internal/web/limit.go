package web

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate limits. They keep one caller from using a server up: a flood of Webmentions, a script that posts or
// follows in a loop, a stream of bad tokens, a guessed password. They live in memory, one counter per caller,
// and are lost at a restart, which is fine for what they are for. Reading public pages is not limited here:
// that is the reverse proxy's job (see the README).

// rateLimiter is a token bucket per key: it holds up to burst tokens and gains perMinute/60 a second; a request
// takes one, and is refused when there is none.
type rateLimiter struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	m      map[string]*bucket
	now    func() time.Time
	sweeps time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute, burst int) *rateLimiter {
	return &rateLimiter{rate: float64(perMinute) / 60, burst: float64(burst), m: map[string]*bucket{}, now: time.Now}
}

// refill brings the key's bucket up to date and returns it. The caller holds the lock.
func (l *rateLimiter) refill(key string) *bucket {
	now := l.now()
	if now.Sub(l.sweeps) > time.Minute { // forget callers whose bucket has long been full again
		for k, b := range l.m {
			if now.Sub(b.last).Seconds()*l.rate+b.tokens >= l.burst {
				delete(l.m, k)
			}
		}
		l.sweeps = now
	}
	b := l.m[key]
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.m[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	return b
}

// wait is how long until the bucket holds a token, from tokens it has now.
func (l *rateLimiter) wait(b *bucket) time.Duration {
	return time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// take uses a token. If there is none it reports how long until there is.
func (l *rateLimiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key)
	if b.tokens < 1 {
		return false, l.wait(b)
	}
	b.tokens--
	return true, 0
}

// has reports whether a token is there, without using one.
func (l *rateLimiter) has(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key)
	if b.tokens < 1 {
		return false, l.wait(b)
	}
	return true, 0
}

// failLimiter counts wrong passwords. Past perKey failures from one address for one user, or perUser from all
// addresses together, the user's sign-in is refused until the window is over, a correct password included: a
// guesser must not be told the guess was right. A success clears the count of that address.
type failLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	perKey  int
	perUser int
	m       map[string]*failures
	now     func() time.Time
}

type failures struct {
	n     int
	since time.Time
}

func newFailLimiter(window time.Duration, perKey, perUser int) *failLimiter {
	return &failLimiter{window: window, perKey: perKey, perUser: perUser, m: map[string]*failures{}, now: time.Now}
}

// live returns the key's count if its window is still open. The caller holds the lock.
func (f *failLimiter) live(key string) *failures {
	r := f.m[key]
	if r != nil && f.now().Sub(r.since) >= f.window {
		delete(f.m, key)
		return nil
	}
	return r
}

// blocked reports whether a sign-in as user from ip is refused, and for how long.
func (f *failLimiter) blocked(user, ip string) (bool, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var wait time.Duration
	for key, limit := range map[string]int{user + "|" + ip: f.perKey, user: f.perUser} {
		if r := f.live(key); r != nil && r.n >= limit {
			wait = max(wait, f.window-f.now().Sub(r.since))
		}
	}
	return wait > 0, wait
}

// fail records a wrong password.
func (f *failLimiter) fail(user, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.m) > 50000 { // a flood of different names must not fill memory: start over
		f.m = map[string]*failures{}
	}
	for _, key := range []string{user + "|" + ip, user} {
		r := f.live(key)
		if r == nil {
			r = &failures{since: f.now()}
			f.m[key] = r
		}
		r.n++
	}
}

// ok records a right password: the count of this address starts again.
func (f *failLimiter) ok(user, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, user+"|"+ip)
}

// limitsOffInTests lets the package's tests, which post hundreds of items in a second, run without limits; a
// test of the limits turns one server's on with s.lim = newLimits().
var limitsOffInTests bool

// limits are the limits of one server.
type limits struct {
	logins   *failLimiter
	webment  *rateLimiter // POST /webmention, per address
	api      *rateLimiter // any API call, per address and user
	apiWrite *rateLimiter // an API call that changes something
	follow   *rateLimiter // following, or refreshing the follows: each fetches from other servers
	badToken *rateLimiter // API calls refused for a missing or wrong token, per address
	waitlist *rateLimiter // a request for the waiting list, per address
}

func newLimits() *limits {
	return &limits{
		logins:   newFailLimiter(15*time.Minute, 5, 100),
		webment:  newRateLimiter(30, 20),
		api:      newRateLimiter(600, 120),
		apiWrite: newRateLimiter(60, 30),
		follow:   newRateLimiter(10, 10),
		badToken: newRateLimiter(20, 20),
		waitlist: newRateLimiter(1, 3),
	}
}

// clientIP is the address a request comes from: for an IPv6 client its /64, which is what one subscriber
// holds, so that changing the low bits does not make a new caller. Behind a reverse proxy that the server
// trusts (-trust-proxy), it is the address the proxy appended to X-Forwarded-For.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				host = last
			}
		}
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return ip.Mask(net.CIDRMask(64, 128)).String()
	}
	return host
}

// tooMany answers a request that is over a limit: 429, and when to try again.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	http.Error(w, "Too many requests. Try again in "+wait.Round(time.Second).String()+".", http.StatusTooManyRequests)
}

// admit applies the limits that are known before the request is read: the routes that cost something. It
// reports false after answering 429. Reading public pages is left to the reverse proxy.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if s.lim == nil {
		return true
	}
	path, ip := r.URL.Path, s.clientIP(r)
	host, _ := splitHost(r.Host)
	caller := ip + "|" + host
	use := func(l *rateLimiter, key string) bool {
		ok, wait := l.take(key)
		if !ok {
			tooMany(w, wait)
		}
		return ok
	}
	switch {
	case r.Method == http.MethodPost && path == "/webmention":
		return use(s.lim.webment, ip)
	case strings.HasPrefix(path, "/api/"):
		if ok, wait := s.lim.badToken.has(ip); !ok { // too many wrong tokens from here
			tooMany(w, wait)
			return false
		}
		if !use(s.lim.api, caller) {
			return false
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !use(s.lim.apiWrite, caller) {
			return false
		}
		return !(r.Method == http.MethodPost && path == "/api/following") || use(s.lim.follow, caller)
	case r.Method == http.MethodPost && (path == "/media" || path == "/media/keep" || path == "/profile/avatar"):
		return use(s.lim.apiWrite, caller)
	case r.Method == http.MethodPost && (path == "/following" || path == "/following/refresh"):
		return use(s.lim.follow, caller)
	}
	return true
}

// Timeouts are how long a connection may take at each step. Without them a client that sends a byte a minute
// holds a connection, and its memory, for ever: a few hundred of them use the server up.
type Timeouts struct {
	ReadHeader, Read, Write, Idle time.Duration
}

// DefaultTimeouts suit this server: a request is a small form or a JSON body of at most a megabyte, and the
// slowest answer, a thread built from other servers' pages, takes about ten seconds.
var DefaultTimeouts = Timeouts{ReadHeader: 10 * time.Second, Read: 30 * time.Second, Write: 60 * time.Second, Idle: 2 * time.Minute}

// NewHTTPServer returns an http.Server for handler with the timeouts set, and a cap on the size of the headers.
func NewHTTPServer(addr string, handler http.Handler, t Timeouts) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: t.ReadHeader, ReadTimeout: t.Read,
		WriteTimeout: t.Write, IdleTimeout: t.Idle, MaxHeaderBytes: 64 << 10}
}
