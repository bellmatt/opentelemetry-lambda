// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/open-telemetry/opentelemetry-collector-contrib/confmap/provider/s3provider"
	"github.com/open-telemetry/opentelemetry-lambda/collector/internal/confmap/converter/disablequeuedretryconverter"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/confmap/converter/expandconverter"
	"go.opentelemetry.io/collector/confmap/provider/envprovider"
	"go.opentelemetry.io/collector/confmap/provider/fileprovider"
	"go.opentelemetry.io/collector/confmap/provider/httpprovider"
	"go.opentelemetry.io/collector/confmap/provider/yamlprovider"
	"go.opentelemetry.io/collector/service"
	"go.uber.org/zap"
)

var (
	// Version variable will be replaced at link time after `make` has been run.
	Version = "latest"

	// GitHash variable will be replaced at link time after `make` has been run.
	GitHash = "<NOT PROPERLY GENERATED>"
)

// Collector implements the OtelcolRunner interfaces running a single otelcol as a go routine within the
// same process as the test executor.
type Collector struct {
	factories      component.Factories
	configProvider service.ConfigProvider
	svc            *service.Collector
	appDone        chan struct{}
	stopped        bool
}

func getConfig(logger *zap.Logger) string {
	val, ex := os.LookupEnv("OPENTELEMETRY_COLLECTOR_CONFIG_FILE")
	if !ex {
		return "/opt/collector-config/config.yaml"
	}
	logger.Info("Using config URI from environment", zap.String("uri", val))
	return val
}

func NewCollector(logger *zap.Logger, factories component.Factories) *Collector {
	l := logger.Named("NewCollector")
	providers := []confmap.Provider{fileprovider.New(), envprovider.New(), yamlprovider.New(), httpprovider.New(), s3provider.New()}
	mapProvider := make(map[string]confmap.Provider, len(providers))

	for _, provider := range providers {
		mapProvider[provider.Scheme()] = provider
	}

	cfgSet := service.ConfigProviderSettings{
		ResolverSettings: confmap.ResolverSettings{
			URIs:       []string{getConfig(l)},
			Providers:  mapProvider,
			Converters: []confmap.Converter{expandconverter.New(), disablequeuedretryconverter.New()},
		},
	}
	cfgProvider, err := service.NewConfigProvider(cfgSet)

	if err != nil {
		l.Fatal("error creating config provider", zap.Error(err))
	}

	col := &Collector{
		factories:      factories,
		configProvider: cfgProvider,
	}
	return col
}

func (c *Collector) Start(ctx context.Context) error {
	params := service.CollectorSettings{
		BuildInfo: component.BuildInfo{
			Command:     "otelcol-lambda",
			Description: "Lambda Collector",
			Version:     Version,
		},
		ConfigProvider: c.configProvider,
		Factories:      c.factories,
	}
	var err error
	c.svc, err = service.New(params)
	if err != nil {
		return err
	}

	c.appDone = make(chan struct{})

	go func() {
		defer close(c.appDone)
		appErr := c.svc.Run(ctx)
		if appErr != nil {
			err = appErr
		}
	}()

	for {
		state := c.svc.GetState()

		// While waiting for collector start, an error was found. Most likely
		// an invalid custom collector configuration file.
		if err != nil {
			return err
		}

		switch state {
		case service.StateStarting:
			// NoOp
		case service.StateRunning:
			return nil
		default:
			err = fmt.Errorf("unable to start, otelcol state is %d", state)
		}
	}
}

func (c *Collector) Stop() error {
	if !c.stopped {
		c.stopped = true
		c.svc.Shutdown()
	}
	<-c.appDone
	return nil
}
