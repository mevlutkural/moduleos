package handler

import "sync"

type streamLimiter struct {
	mu        sync.Mutex
	maximum   int
	perAppMax int
	active    int
	perApp    map[string]int
}

type streamLease struct {
	limiter *streamLimiter
	appName string
	once    sync.Once
}

func newStreamLimiter(maximum, perAppMax int) *streamLimiter {
	return &streamLimiter{maximum: maximum, perAppMax: perAppMax, perApp: make(map[string]int)}
}

func (l *streamLimiter) acquire(appName string) (*streamLease, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active >= l.maximum || l.perApp[appName] >= l.perAppMax {
		return nil, false
	}
	l.active++
	l.perApp[appName]++
	return &streamLease{limiter: l, appName: appName}, true
}

func (l *streamLimiter) snapshot() (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, len(l.perApp)
}

func (l *streamLease) Release() {
	if l == nil || l.limiter == nil {
		return
	}
	l.once.Do(func() {
		l.limiter.mu.Lock()
		defer l.limiter.mu.Unlock()
		l.limiter.active--
		l.limiter.perApp[l.appName]--
		if l.limiter.perApp[l.appName] == 0 {
			delete(l.limiter.perApp, l.appName)
		}
	})
}
