/*
Copyright 2014 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package restclient

import (
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/util/flowcontrol"
)

func newSlightlyStickyProvider(hosts []*url.URL) *slightlyStickyProvider {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var errorsPerSecond float32 = 0.02
	errorsBurst := 5
	return &slightlyStickyProvider{
		hosts:           hosts,
		cur:             rng.Intn(len(hosts)),
		rng:             rng,
		errorsPerSecond: errorsPerSecond,
		errorsBurst:     errorsBurst,
		ratelimiter:     flowcontrol.NewTokenBucketRateLimiter(errorsPerSecond, errorsBurst),
	}
}

type slightlyStickyProvider struct {
	sync.RWMutex
	hosts           []*url.URL
	cur             int
	rng             *rand.Rand
	errorsPerSecond float32
	errorsBurst     int
	ratelimiter     flowcontrol.RateLimiter
}

func (s *slightlyStickyProvider) get() *url.URL {
	s.RLock()
	defer s.RUnlock()
	return s.hosts[s.cur]
}

func (s *slightlyStickyProvider) next() {
	s.Lock()
	defer s.Unlock()
	s.cur = s.rng.Intn(len(s.hosts))
	s.ratelimiter = flowcontrol.NewTokenBucketRateLimiter(s.errorsPerSecond, s.errorsBurst)
}

func (s *slightlyStickyProvider) wrap(delegate http.RoundTripper) http.RoundTripper {
	return rtfunc(func(req *http.Request) (*http.Response, error) {
		resp, err := delegate.RoundTrip(req)
		glog.V(5).Infof("delegate.RoundTrip provider(%p) req(%p) resp(%p) err(%v)", s, req, resp, err)
		if err != nil {
			tryAccept := func() bool {
				s.RLock()
				defer s.RUnlock()
				return !s.ratelimiter.TryAccept()
			}
			if tryAccept() {
				glog.Warningf("provider(%p): before switch host: cur(%d) %s", s, s.cur, s.hosts[s.cur].String())
				s.next()
				glog.Warningf("provider(%p): after switch host: cur(%d) %s", s, s.cur, s.hosts[s.cur].String())
			}
		}
		return resp, err
	})
}

type rtfunc func(*http.Request) (*http.Response, error)

func (rt rtfunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt(req)
}
