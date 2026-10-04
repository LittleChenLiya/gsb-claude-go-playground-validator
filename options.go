package validator

// Option represents a configurations option to be applied to validator during initialization.
type Option func(*Validate)

// WithRequiredStructEnabled enables required tag on non-pointer structs to be applied instead of ignored.
//
// This was made opt-in behaviour in order to maintain backward compatibility with the behaviour previous
// to being able to apply struct level validations on struct fields directly.
//
// It is recommended you enabled this as it will be the default behaviour in v11+
func WithRequiredStructEnabled() Option {
	return func(v *Validate) {
		v.requiredStructEnabled = true
	}
}

// WithPrivateFieldValidation activates validation for unexported fields via the use of the `unsafe` package.
//
// By opting into this feature you are acknowledging that you are aware of the risks and accept any current or future
// consequences of using this feature.
func WithPrivateFieldValidation() Option {
	return func(v *Validate) {
		v.privateFieldValidation = true
	}
}

// WithTagNameFuncBlankOmit makes a blank return from a RegisterTagNameFunc omit
// the field from the error namespace instead of silently falling back to the
// struct field name.
//
// This was made opt-in behaviour to maintain backward compatibility with
// existing callers that rely on the fallback for error namespaces.
//
// It is recommended you enable this as it will be the default behaviour in v11+.
func WithTagNameFuncBlankOmit() Option {
	return func(v *Validate) {
		v.omitBlankFieldNames = true
	}
}

// WithCollectionContext enables tracking of the enclosing collection while
// diving into slices, arrays and maps, so custom validation functions can
// access sibling elements and the current index via FieldLevel.Collection
// and FieldLevel.CollectionIndex instead of relying on global state.
//
// Each nested 'dive' gets its own collection scope and the tracking state
// is kept per validation run, so concurrent StructCtx (and friends) calls
// never share collection context. When enabled, cancellation of the
// context.Context passed to the *Ctx entry points also stops iteration
// over the remaining collection elements.
//
// This is opt-in behaviour in order to keep the default behaviour and
// performance unchanged.
func WithCollectionContext() Option {
	return func(v *Validate) {
		v.collectionCtxEnabled = true
	}
}
