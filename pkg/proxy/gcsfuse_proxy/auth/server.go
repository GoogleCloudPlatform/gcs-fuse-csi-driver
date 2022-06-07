/*
Copyright 2022 The Kubernetes Authors.

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

package auth

import (
	"encoding/json"
	"net/http"
	"time"

	"k8s.io/klog"
)

func NewServer(tm *TokenManager) (s *http.Server, err error) {
	handler := &HTTPHandler{
		tm: tm,
	}
	s = &http.Server{
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	return
}

type HTTPHandler struct {
	tm *TokenManager
}

// ServeHTTP handles 1 request:
//   - GET from "?podID=xxx" for the access token for the bucket.
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.doGet(w, r)

	default:
		http.Error(w, "", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) doGet(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	klog.Infof("GET %s", params)
	podID := r.URL.Query().Get("podID")
	if podID == "" {
		http.Error(
			w,
			"invalid podID",
			http.StatusBadRequest,
		)
	}

	token, err := h.tm.GetToken(podID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	data, err := json.Marshal(token)
	if err != nil {
		http.Error(
			w,
			"token serialization: "+err.Error(),
			http.StatusInternalServerError,
		)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err = w.Write(data); err != nil {
		return
	}
}
