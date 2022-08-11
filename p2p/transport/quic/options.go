package libp2pquic

type Option func(opts *config) error

type config struct {
	disableReuseport bool
}

func (cfg *config) apply(opts ...Option) error {
	for _, opt := range opts {
		if err := opt(cfg); err != nil {
			return err
		}
	}

	return nil
}

func DisableReuseport() Option {
	return func(cfg *config) error {
		cfg.disableReuseport = true
		return nil
	}
}
