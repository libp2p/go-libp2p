//go:build !js
// +build !js

package libp2p

import (
	"errors"
	"reflect"

	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"go.uber.org/fx"
)

func QUICReuse(constructor interface{}, opts ...quicreuse.Option) Option {
	return func(cfg *Config) error {
		tag := `group:"quicreuseopts"`
		typ := reflect.ValueOf(constructor).Type()
		numParams := typ.NumIn()
		isVariadic := typ.IsVariadic()

		if !isVariadic && len(opts) > 0 {
			return errors.New("QUICReuse constructor doesn't take any options")
		}

		var params []string
		if isVariadic && len(opts) > 0 {
			// If there are options, apply the tag.
			// Since options are variadic, they have to be the last argument of the constructor.
			params = make([]string, numParams)
			params[len(params)-1] = tag
		}

		cfg.QUICReuse = append(cfg.QUICReuse, fx.Provide(fx.Annotate(constructor, fx.ParamTags(params...))))
		for _, opt := range opts {
			cfg.QUICReuse = append(cfg.QUICReuse, fx.Supply(fx.Annotate(opt, fx.ResultTags(tag))))
		}
		return nil
	}
}
