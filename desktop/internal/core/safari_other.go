//go:build !darwin

package core

import "errors"

const SafariSupported = false

type SafariSession struct {
	Profile string
	All     bool
}

func StartSafari(cfg *Config, p *Profile, all bool, urls []string, onEnd func(error)) (*SafariSession, error) {
	return nil, errors.New("Safari mode is only available on macOS")
}

func (s *SafariSession) Stop() error { return nil }

func SafariRestore() error  { return nil }
func SafariPending() string { return "" }
