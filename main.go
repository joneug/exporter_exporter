// Copyright 2016 Qubit Ltd.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	addr  = flag.String("web.listen-address", ":9100", "The address to listen on for HTTP requests.")
	tPath = flag.String("web.telemetry-path", "/metrics", "The address to listen on for HTTP requests.")
	mPath = flag.String("web.proxy-path", "/proxy", "The address to listen on for HTTP requests.")

	proxyDuration = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Name: "expexp_proxy_duration_seconds",
			Help: "Duration of queries to the yahoo API",
		},
		[]string{"module"},
	)
	proxyErrorCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "expexp_proxy_errors_total",
			Help: "Counts of errors",
		},
		[]string{"module"},
	)
	proxyTimeoutCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "expexp_proxy_timeout_errors_total",
			Help: "Counts of the number of times a proxy timeout occurred",
		},
		[]string{"module"},
	)

	proxyMalformedCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "expexp_malformed_content_errors_total",
			Help: "Counts of unparsable scrape content errors",
		},
		[]string{"module"},
	)
)

func init() {
	// register the collector metrics in the default
	// registry.
	prometheus.MustRegister(proxyDuration)
	prometheus.MustRegister(proxyTimeoutCount)
	prometheus.MustRegister(proxyErrorCount)
	prometheus.MustRegister(proxyMalformedCount)
	prometheus.MustRegister(cmdStartsCount)
	prometheus.MustRegister(cmdFailsCount)
}

func main() {
	flag.Parse()

	r, err := os.Open("expexp.yaml")
	if err != nil {
		glog.Fatalf("%+v", err)
	}

	cfg, err := readConfig(r)
	if err != nil {
		glog.Fatalf("%+v", err)
	}

	http.HandleFunc("/proxy", cfg.doProxy)
	http.Handle("/metrics", promhttp.Handler())

	if err := http.ListenAndServe(*addr, nil); err != nil {
		glog.Fatalf("%+v", err)
	}
}

func (cfg *config) doProxy(w http.ResponseWriter, r *http.Request) {
	mod, ok := r.URL.Query()["module"]
	if !ok {
		glog.Infof("no module given")
		return
	}

	if len(mod) != 1 {
		glog.Infof("you must pass exactly one module parameter")
		return
	}

	if glog.V(3) {
		glog.Infof("running module %v\n", mod)
	}

	var h http.Handler
	if m, ok := cfg.Modules[mod[0]]; !ok {
		proxyErrorCount.WithLabelValues("unknown").Inc()
		glog.Infof("unknown module requested  %v\n", mod)
		http.Error(w, fmt.Sprintf("unknown module %v\n", mod), http.StatusNotFound)
		return
	} else {
		m.name = mod[0]
		h = m
	}

	h.ServeHTTP(w, r)
}

func (m moduleConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	st := time.Now()
	defer func() {
		proxyDuration.WithLabelValues(m.name).Observe(float64(time.Since(st)) / float64(time.Second))
	}()

	nr := r
	cancel := func() {}
	if m.Timeout != 0 {
		if glog.V(3) {
			glog.Infof("setting module %v timeout to %v", m.name, m.Timeout)
		}

		var ctx context.Context
		ctx, cancel = context.WithTimeout(r.Context(), m.Timeout)
		nr = r.WithContext(ctx)
	}
	defer cancel()

	switch m.Method {
	case "exec":
		m.Exec.mcfg = &m
		m.Exec.ServeHTTP(w, nr)
	case "http":
		m.HTTP.mcfg = &m
		m.HTTP.ServeHTTP(w, nr)
	default:
		glog.Infof("unknown module method  %v\n", m.Method)
		proxyErrorCount.WithLabelValues(m.name).Inc()
		http.Error(w, fmt.Sprintf("unknown module method %v\n", m.Method), http.StatusNotFound)
		return
	}
}