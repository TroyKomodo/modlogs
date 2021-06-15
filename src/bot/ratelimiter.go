package bot

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/ratelimit"
)

type merger func(content string) bool

type mergerWrapper struct {
	id     string
	merger merger
}

type rateLimited struct {
	limiter ratelimit.Limiter
	limited []*mergerWrapper
	mtx     *sync.Mutex
}

type rateLimiter struct {
	limits map[string]*rateLimited
	mtx    *sync.Mutex
}

func newLimiter() *rateLimiter {
	return &rateLimiter{
		limits: map[string]*rateLimited{},
		mtx:    &sync.Mutex{},
	}
}

func (r *rateLimiter) Limit(key string, content string, merge merger) bool {
	r.mtx.Lock()
	v, ok := r.limits[key]
	if ok {
		r.mtx.Unlock()
	} else {
		v = &rateLimited{
			limiter: ratelimit.New(5, ratelimit.Per(10*time.Second), ratelimit.WithSlack(0)),
			limited: []*mergerWrapper{},
			mtx:     &sync.Mutex{},
		}
		r.limits[key] = v
		r.mtx.Unlock()
	}
	v.mtx.Lock()
	if len(v.limited) != 0 {
		for _, vl := range v.limited {
			if vl.merger(content) {
				v.mtx.Unlock()
				return false
			}
		}
	}
	id, _ := uuid.NewRandom()
	mw := &mergerWrapper{
		id:     id.String(),
		merger: merge,
	}
	v.limited = append(v.limited, mw)
	v.mtx.Unlock()
	v.limiter.Take()
	v.mtx.Lock()
	for i, mv := range v.limited {
		if mv.id == mw.id {
			v.limited = append(v.limited[:i], v.limited[i+1:]...)
			break
		}
	}
	v.mtx.Unlock()
	return true
}
