//go:build !linux

package core

import (
	"context"
	"fmt"
)

type unsupportedFakeHTTPRawInjector struct{}

func newFakeHTTPRawInjector() fakeHTTPInjector {
	return &unsupportedFakeHTTPRawInjector{}
}

func (i *unsupportedFakeHTTPRawInjector) Inject(_ context.Context, _ fakeHTTPRequest) error {
	return fmt.Errorf("fakehttp raw injection requires linux")
}
