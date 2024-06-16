/*
Copyright 2018 The Kubernetes Authors.
Copyright 2024 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"sync"
	"time"
)

type TokenSourceItem struct {
	tokenSource *GCPTokenSource
	lastAccess  time.Time
}

type TokenSourceStore struct {
	tokenSources map[string]*TokenSourceItem
	mu           sync.RWMutex
	ttl          time.Duration // Time-to-live for tokenSources
	cleanupFreq  time.Duration
}

func NewTokenSourceStore(ttl, cleanupFreq time.Duration) *TokenSourceStore {
	c := &TokenSourceStore{
		tokenSources: make(map[string]*TokenSourceItem),
		ttl:          ttl,
		cleanupFreq:  cleanupFreq,
	}

	go c.cleanup() // Start the cleanup goroutine

	return c
}

func (c *TokenSourceStore) Set(ns, name string, ts *GCPTokenSource) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokenSources[ns+"/"+name] = &TokenSourceItem{tokenSource: ts, lastAccess: time.Now()}
}

func (c *TokenSourceStore) Get(ns, name string) *GCPTokenSource {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ts, found := c.tokenSources[ns+"/"+name]
	if !found {
		return nil
	}

	ts.lastAccess = time.Now()

	return ts.tokenSource
}

func (c *TokenSourceStore) cleanup() {
	ticker := time.NewTicker(c.cleanupFreq)
	for range ticker.C {
		c.mu.Lock()
		for k, v := range c.tokenSources {
			if time.Since(v.lastAccess) > c.ttl {
				delete(c.tokenSources, k)
			}
		}
		c.mu.Unlock()
	}
}
