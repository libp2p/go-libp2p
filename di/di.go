// A minimal reflection-based dependency injection helper.
//
// Usage pattern:
//
//	type Config struct {
//	    Logger func() (*zap.Logger, error)
//	    Repo   func(*zap.Logger) (*Repo, error)
//	    // Pre-supplied dependency (non-func, non-zero fields are treated as already available):
//	    Clock  time.Clock
//	}
//
//	type Result struct {
//	    Logger *zap.Logger
//	    Repo   *Repo
//	}
//
//	var cfg Config
//	var res Result
//	if err := di.Build(cfg, &res); err != nil { ... }
//
// The Build function:
//   - Treats every function field in the config struct as a constructor.
//   - A constructor must return either (T) or (T, error).
//   - Constructor parameters are resolved from already constructed / supplied values.
//   - Non-function, non-zero fields in the config struct are treated as pre-supplied instances.
//   - Pre-populated (non-zero) fields in the result struct are also treated as pre-supplied.
//   - After a constructor runs, the produced value is stored; any zero field in the result
//     struct whose type is assignable from the produced value's type will be filled.
//   - Reuses singletons: one instance per produced type. Duplicate producers of the
//     exact same concrete type are rejected.
//   - Detects unsatisfied dependencies / cycles (when no progress can be made).
//
// This is intentionally minimal and opinionated; it is not a full-featured DI container.
package di

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

type Provide[Out any] struct {
	fOrV any
}

// SideEffect is a sentinel value representing a constructor used only for side
// effects such as introducing two components together without a circular
// dependency.
type SideEffect struct{}

func NewSideEffect(f any) (Provide[SideEffect], error) {
	return NewProvide[SideEffect](f)
	// t := reflect.TypeOf(f)
	// if t.Kind() == reflect.Func && t.NumOut() == 1 {
	// 	if isErrorType(t.Out(0)) {
	// 		return Provide[SideEffect]{fOrV: f}, nil
	// 	}
	// }

	// return Provide[SideEffect]{fOrV: f}, fmt.Errorf("NewSideEffect: Invalid signature. Requires a function that returns only an error")
}

func MustSideEffect(f any) Provide[SideEffect] {
	return Must(NewSideEffect(f))
}

type provideI interface {
	diOutType() reflect.Type
	diPayload() any
}

func (p Provide[Out]) diOutType() reflect.Type { return typeOf[Out]() }
func (p Provide[Out]) diPayload() any          { return p.fOrV }

func Must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}

func MustProvide[Out any](ctorOrVal any) Provide[Out] {
	return Must(NewProvide[Out](ctorOrVal))
}

func NewProvide[Out any](ctorOrVal any) (Provide[Out], error) {
	outType := typeOf[Out]()

	if ctorOrVal == nil {
		if canBeNil(outType) {
			return Provide[Out]{fOrV: nil}, nil
		}
		return Provide[Out]{}, fmt.Errorf("Provide[%v]: nil not valid for non-nilable type", outType)
	}

	t := reflect.TypeOf(ctorOrVal)

	// Case 1: function returning Out or (Out, error). Arbitrary args allowed.
	if t.Kind() == reflect.Func {
		nout := t.NumOut()
		switch nout {
		case 1:
			if !t.Out(0).AssignableTo(outType) {
				return Provide[Out]{}, fmt.Errorf("Provide[%v]: function return %v is not assignable to %v",
					outType, t.Out(0), outType)
			}
			return Provide[Out]{fOrV: ctorOrVal}, nil

		case 2:
			if !t.Out(0).AssignableTo(outType) {
				return Provide[Out]{}, fmt.Errorf("Provide[%v]: first return %v is not assignable to %v",
					outType, t.Out(0), outType)
			}
			if !isErrorType(t.Out(1)) {
				return Provide[Out]{}, fmt.Errorf("Provide[%v]: second return must be error, got %v",
					outType, t.Out(1))
			}
			return Provide[Out]{fOrV: ctorOrVal}, nil

		default:
			return Provide[Out]{}, fmt.Errorf("Provide[%v]: function must return Out or (Out, error); got %d returns",
				outType, nout)
		}
	}

	// Case 2: value assignable to Out (covers interface satisfaction)
	if !t.AssignableTo(outType) {
		return Provide[Out]{}, fmt.Errorf("Provide[%v]: value of type %v is not assignable to %v", outType, t, outType)
	}
	return Provide[Out]{fOrV: ctorOrVal}, nil
}

// Helpers

func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

func canBeNil(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Interface, reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	default:
		return false
	}
}

func New[R any, C any](config C) (R, error) {
	var r R
	err := Build(config, &r)
	return r, err
}

// Build resolves only what's needed to populate exported fields in result.
// Now supports arbitrarily nested provider namespaces inside config
// (exported struct / *struct fields are recursively visited).
//
// Supported providers in config (at any nesting depth):
//   - Provide[T]                    // single constructor or value for T
//   - []Provide[T]                  // list of constructors/values contributing to []T
//   - func(...Deps) T / (T,error)   // singular constructor for T
//   - value of type T               // preprovided singular value
//   - []func(...Deps) T/(T,error)   // contributes to []T
//   - []T                           // contributes to []T
//
// Non-func, non-zero exported fields remain prebound instances.
// Pre-populated (non-zero) fields in result are also prebound.
// result must be a pointer to a struct.
func Build[C any, R any](config C, result R) error {
	cfgV := reflect.ValueOf(config)
	for cfgV.IsValid() && cfgV.Kind() == reflect.Pointer {
		if cfgV.IsNil() {
			return errors.New("config pointer is nil")
		}
		cfgV = cfgV.Elem()
	}
	if !cfgV.IsValid() || cfgV.Kind() != reflect.Struct {
		return errors.New("config must be a struct or pointer to struct")
	}

	resV := reflect.ValueOf(result)
	if !resV.IsValid() || resV.Kind() != reflect.Pointer || resV.Elem().Kind() != reflect.Struct {
		return errors.New("result must be a pointer to struct")
	}
	resStruct := resV.Elem()

	type ctor struct {
		name string
		fn   reflect.Value
		out  reflect.Type
	}

	// Singular instances & providers
	values := map[reflect.Type]reflect.Value{} // exact type -> instance
	providers := map[reflect.Type][]ctor{}     // out type -> ctors

	// List providers for []T
	listValues := map[reflect.Type][]reflect.Value{} // elem dynamic type -> instances
	listProviders := map[reflect.Type][]ctor{}       // elem out type -> ctors
	listPresence := map[reflect.Type]bool{}          // elem T present explicitly (even if empty)

	// Seed from pre-populated result fields (treat as prebound)
	{
		resT := resStruct.Type()
		for i := 0; i < resT.NumField(); i++ {
			sf := resT.Field(i)
			if sf.PkgPath != "" {
				continue
			}
			fv := resStruct.Field(i)
			if !fv.IsZero() {
				values[fv.Type()] = fv
			}
		}
	}

	// Collect providers/values recursively from cfgV.
	var collect func(v reflect.Value, path string) error
	collect = func(v reflect.Value, path string) error {
		if !v.IsValid() {
			return nil
		}
		// Deref pointers
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			return nil
		}

		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.PkgPath != "" { // unexported
				continue
			}
			fv := v.Field(i)
			name := sf.Name
			if path != "" {
				name = path + "." + sf.Name
			}

			// Provide[T] (singular) must be recognized BEFORE treating structs as namespaces.
			if pi, ok := asProvide(fv); ok {
				outT := pi.diOutType()
				payload := pi.diPayload()
				if payload == nil {
					values[outT] = reflect.Zero(outT)
					continue
				}
				pt := reflect.TypeOf(payload)
				if pt.Kind() == reflect.Func {
					if err := validateCtorSignature(pt, name); err != nil {
						return err
					}
					providers[pt.Out(0)] = append(providers[pt.Out(0)], ctor{name: name, fn: reflect.ValueOf(payload), out: pt.Out(0)})
				} else {
					values[pt] = reflect.ValueOf(payload)
				}
				continue
			}

			// Namespace recursion for embedded structs
			if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
				// Provide access to the nested config value itself
				values[sf.Type] = fv

				if err := collect(fv, name); err != nil {
					return err
				}
				continue
			}

			// []Provide[T] (list)
			if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Struct {
				if provideElem, ok := reflect.New(fv.Type().Elem()).Elem().Interface().(provideI); ok {
					if fv.Len() == 0 && !fv.IsNil() {
						// Set presence of empty list
						outT := provideElem.diOutType()
						listPresence[outT] = true
					}

					for j := 0; j < fv.Len(); j++ {
						pi := fv.Index(j).Interface().(provideI)
						outT := pi.diOutType()
						listPresence[outT] = true
						payload := pi.diPayload()
						if payload == nil {
							listValues[outT] = append(listValues[outT], reflect.Zero(outT))
							continue
						}
						pt := reflect.TypeOf(payload)
						if pt.Kind() == reflect.Func {
							if err := validateCtorSignature(pt, fmt.Sprintf("%s[%d]", name, j)); err != nil {
								return err
							}
							listProviders[pt.Out(0)] = append(listProviders[pt.Out(0)], ctor{
								name: fmt.Sprintf("%s[%d]", name, j),
								fn:   reflect.ValueOf(payload),
								out:  pt.Out(0),
							})
						} else {
							listValues[pt] = append(listValues[pt], reflect.ValueOf(payload))
						}
					}
					continue
				}
			}

			// Fallback to earlier forms (singular func/value; list of funcs/values)
			switch sf.Type.Kind() {
			case reflect.Func:
				ft := fv.Type()
				if err := validateCtorSignature(ft, name); err != nil {
					return err
				}
				providers[ft.Out(0)] = append(providers[ft.Out(0)], ctor{name: name, fn: fv, out: ft.Out(0)})

			case reflect.Slice:
				elemT := sf.Type.Elem()
				if elemT.Kind() == reflect.Func {
					for j := 0; j < fv.Len(); j++ {
						fn := fv.Index(j)
						ft := fn.Type()
						if err := validateCtorSignature(ft, fmt.Sprintf("%s[%d]", name, j)); err != nil {
							return err
						}
						listProviders[ft.Out(0)] = append(listProviders[ft.Out(0)], ctor{
							name: fmt.Sprintf("%s[%d]", name, j),
							fn:   fn, out: ft.Out(0),
						})
					}
				} else {
					if fv.Len() == 0 {
						listPresence[elemT] = true // explicit empty list present
					}
					for j := 0; j < fv.Len(); j++ {
						vj := fv.Index(j)
						listValues[vj.Type()] = append(listValues[vj.Type()], vj)
					}
				}

			default:
				// preprovided singular instance (non-zero only)
				if !fv.IsZero() {
					values[fv.Type()] = fv
				}
			}
		}
		return nil
	}

	// Provide access to the Config value itself
	values[cfgV.Type()] = cfgV

	if err := collect(cfgV, ""); err != nil {
		return err
	}

	// Resolver with memoization & cycles
	resolving := map[reflect.Type]bool{}

	var resolve func(t reflect.Type) (reflect.Value, error)
	resolve = func(t reflect.Type) (reflect.Value, error) {
		// Cached exact?
		if v, ok := values[t]; ok {
			return v, nil
		}

		// Slice resolution []T
		if t.Kind() == reflect.Slice {
			elem := t.Elem()
			var elems []reflect.Value
			found := false

			// Explicit list values (concrete types assignable to elem)
			for haveT, vals := range listValues {
				if isAssignableOrImpl(haveT, elem) {
					found = true
					elems = append(elems, vals...)
				}
			}
			// From list constructors whose out is assignable to elem
			for outT, ctors := range listProviders {
				if !isAssignableOrImpl(outT, elem) {
					continue
				}
				found = true
				for _, c := range ctors {
					if resolving[t] {
						return reflect.Value{}, fmt.Errorf("dependency cycle detected at %s", t)
					}
					resolving[t] = true
					ft := c.fn.Type()
					args := make([]reflect.Value, ft.NumIn())
					for i := 0; i < ft.NumIn(); i++ {
						paramT := ft.In(i)
						arg, err := resolve(paramT)
						if err != nil {
							delete(resolving, t)
							return reflect.Value{}, fmt.Errorf("%s depends on %s: %w", c.name, paramT, err)
						}
						if !isAssignableOrImpl(arg.Type(), paramT) {
							delete(resolving, t)
							return reflect.Value{}, fmt.Errorf("%s: cannot use %s as %s", c.name, arg.Type(), paramT)
						}
						args[i] = arg
					}
					outs := c.fn.Call(args)
					delete(resolving, t)
					if len(outs) == 2 && !outs[1].IsNil() {
						return reflect.Value{}, fmt.Errorf("%s error: %w", c.name, outs[1].Interface().(error))
					}
					elems = append(elems, outs[0])
				}
			}

			// If an explicit provider for elem T exists (e.g., []Provide[T] or []T present but empty),
			// we should still succeed with an empty slice.
			if !found {
				if listPresence[elem] {
					values[t] = reflect.MakeSlice(t, 0, 0)
					return values[t], nil
				}
				return reflect.Value{}, fmt.Errorf("no provider for %s", t)
			}

			slice := reflect.MakeSlice(t, 0, len(elems))
			for _, e := range elems {
				slice = reflect.Append(slice, e)
			}
			values[t] = slice
			return slice, nil
		}

		// Try existing instances for interface targets (singular)
		if t.Kind() == reflect.Interface {
			for haveT, v := range values {
				if haveT.Implements(t) {
					return v, nil
				}
			}
		}

		// Cycle detection (singular)
		if resolving[t] {
			return reflect.Value{}, fmt.Errorf("dependency cycle detected at %s", t)
		}
		resolving[t] = true
		defer delete(resolving, t)

		// Pick singular provider(s)
		var candidates []ctor
		if ps, ok := providers[t]; ok {
			candidates = append(candidates, ps...)
		} else if t.Kind() == reflect.Interface {
			for outT, ps := range providers {
				if outT.Implements(t) {
					candidates = append(candidates, ps...)
				}
			}
		}

		if len(candidates) == 0 {
			return reflect.Value{}, fmt.Errorf("no provider for %s", t)
		}
		if len(candidates) > 1 {
			var names []string
			for _, c := range candidates {
				names = append(names, c.name+" -> "+c.out.String())
			}
			return reflect.Value{}, fmt.Errorf("ambiguous providers for %s: %s", t, strings.Join(names, ", "))
		}

		impl := candidates[0]
		ft := impl.fn.Type()
		args := make([]reflect.Value, ft.NumIn())
		for i := 0; i < ft.NumIn(); i++ {
			paramT := ft.In(i)
			arg, err := resolve(paramT)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s depends on %s: %w", impl.name, paramT, err)
			}
			if !isAssignableOrImpl(arg.Type(), paramT) {
				return reflect.Value{}, fmt.Errorf("%s: cannot use %s as %s", impl.name, arg.Type(), paramT)
			}
			args[i] = arg
		}
		outs := impl.fn.Call(args)
		if len(outs) == 2 {
			if !outs[1].IsNil() {
				return reflect.Value{}, fmt.Errorf("%s error: %w", impl.name, outs[1].Interface().(error))
			}
			values[impl.out] = outs[0]
			return outs[0], nil
		}
		values[impl.out] = outs[0]
		return outs[0], nil
	}

	// Populate result fields lazily
	var missing []string
	resT := resStruct.Type()
	for i := 0; i < resT.NumField(); i++ {
		sf := resT.Field(i)
		if sf.Name != "_" && sf.PkgPath != "" {
			continue
		}
		fv := resStruct.Field(i)
		if !fv.IsZero() {
			continue
		}
		v, err := resolve(sf.Type)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s): %v", sf.Name, sf.Type, err))
			continue
		}
		if isAssignableOrImpl(v.Type(), sf.Type) {
			if sf.Name != "_" {
				fv.Set(v)
			}
		} else {
			missing = append(missing, fmt.Sprintf("%s (%s): produced %s not assignable", sf.Name, sf.Type, v.Type()))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("failed to build result fields:\n  - %s", strings.Join(missing, "\n  - "))
	}
	return nil
}

// --- helpers ---

func validateCtorSignature(ft reflect.Type, name string) error {
	if ft.IsVariadic() {
		return fmt.Errorf("constructor %q: variadics not supported", name)
	}
	nout := ft.NumOut()
	if nout == 1 {
		return nil
	}
	if nout == 2 && isErrorType(ft.Out(1)) {
		return nil
	}
	return fmt.Errorf("constructor %q: must return (T) or (T, error); got %d returns", name, nout)
}

func isAssignableOrImpl(have, want reflect.Type) bool {
	return have.AssignableTo(want) || (want.Kind() == reflect.Interface && have.Implements(want))
}

func isErrorType(t reflect.Type) bool {
	return t == reflect.TypeOf((*error)(nil)).Elem()
}

// asProvide tries to view v as a Provide[*]. Returns (iface, true) if so.
func asProvide(v reflect.Value) (provideI, bool) {
	if !v.IsValid() {
		return nil, false
	}
	x := v.Interface()
	pi, ok := x.(provideI)
	return pi, ok
}
