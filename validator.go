package validator

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"unsafe"
)

// per validate construct
type validate struct {
	v              *Validate
	top            reflect.Value
	ns             []byte
	actualNs       []byte
	errs           ValidationErrors
	includeExclude map[string]struct{} // reset only if StructPartial or StructExcept are called, no need otherwise
	ffn            FilterFunc
	slflParent     reflect.Value // StructLevel & FieldLevel
	slCurrent      reflect.Value // StructLevel & FieldLevel
	flField        reflect.Value // StructLevel & FieldLevel
	cf             *cField       // StructLevel & FieldLevel
	ct             *cTag         // StructLevel & FieldLevel
	misc           []byte        // misc reusable
	str1           string        // misc reusable
	str2           string        // misc reusable
	fldIsPointer   bool          // StructLevel & FieldLevel
	isPartial      bool
	hasExcludes    bool
	nameFn         FieldNameFunc // per StructNamed call only, never written back to the shared Validate or its caches
	segs           []FieldPathSegment
}

// parent and current will be the same the first run of validateStruct
func (v *validate) validateStruct(ctx context.Context, parent reflect.Value, current reflect.Value, typ reflect.Type, ns []byte, structNs []byte, segs []FieldPathSegment, ct *cTag) {
	cs, ok := v.v.structCache.Get(typ)
	if !ok {
		cs = v.v.extractStructCache(current, typ.Name())
	}

	if len(ns) == 0 && len(cs.name) != 0 {
		ns = append(ns, cs.name...)
		ns = append(ns, '.')

		structNs = append(structNs, cs.name...)
		structNs = append(structNs, '.')

		if v.nameFn != nil {
			segs = append(segs, FieldPathSegment{Kind: FieldPathSegmentStruct, Name: cs.name, DisplayName: cs.name})
		}
	}

	// ct is nil on top level struct, and structs as fields that have no tag info
	// so if nil or if not nil and the structonly tag isn't present
	if ct == nil || ct.typeof != typeStructOnly {
		var f *cField

		for i := 0; i < len(cs.fields); i++ {
			f = cs.fields[i]

			if v.isPartial {
				if v.ffn != nil {
					// used with StructFiltered
					if v.ffn(append(structNs, f.name...)) {
						continue
					}
				} else {
					// used with StructPartial & StructExcept
					_, ok = v.includeExclude[string(append(structNs, f.name...))]

					if (ok && v.hasExcludes) || (!ok && !v.hasExcludes) {
						continue
					}
				}
			}

			cf := v.resolveNamedField(typ, structNs, f)

			if v.nameFn != nil {
				segs = append(segs, FieldPathSegment{
					Kind:        FieldPathSegmentStruct,
					Name:        f.name,
					DisplayName: cf.altName,
				})
			}

			v.traverseField(ctx, current, current.Field(f.idx), ns, structNs, segs, cf, f.cTags)

			if v.nameFn != nil {
				segs = segs[:len(segs)-1]
			}
		}
	}

	// check if any struct level validations, after all field validations already checked.
	// first iteration will have no info about nostructlevel tag, and is checked prior to
	// calling the next iteration of validateStruct called from traverseField.
	if cs.fn != nil {
		v.slflParent = parent
		v.slCurrent = current
		v.ns = ns
		v.actualNs = structNs
		v.segs = segs

		cs.fn(ctx, v)
	}
}

// resolveNamedField returns the cached cField as-is unless this validation call
// provided a FieldNameFunc, in which case an ephemeral copy with the call's
// display name is returned. Shared per-type caches are never mutated.
func (v *validate) resolveNamedField(typ reflect.Type, structNs []byte, f *cField) *cField {
	if v.nameFn == nil {
		return f
	}

	info := FieldInfo{
		Struct: typ,
		Field:  typ.Field(f.idx),
		Index:  f.idx,
		Path:   string(append(append([]byte{}, structNs...), f.name...)),
	}

	name := v.nameFn(info)
	if name == "" {
		return f
	}

	cf := *f
	cf.altName = name
	cf.namesEqual = f.name == name
	return &cf
}

// errorSegments snapshots the display-aware path walked so far, including the
// current field/index/key segment. It returns nil for validation entries
// without a FieldNameFunc so the common path stays allocation free.
func (v *validate) errorSegments(segs []FieldPathSegment) []FieldPathSegment {
	if v.nameFn == nil {
		return nil
	}
	out := make([]FieldPathSegment, len(segs))
	copy(out, segs)
	return out
}

// traverseField validates any field, be it a struct or single field, ensures it's validity and passes it along to be validated via it's tag options
func (v *validate) traverseField(ctx context.Context, parent reflect.Value, current reflect.Value, ns []byte, structNs []byte, segs []FieldPathSegment, cf *cField, ct *cTag) {
	var typ reflect.Type
	var kind reflect.Kind

	current, kind, v.fldIsPointer = v.extractTypeInternal(current, false)

	var isNestedStruct bool

	switch kind {
	case reflect.Ptr, reflect.Interface, reflect.Invalid:

		if ct == nil {
			return
		}

		if ct.typeof == typeOmitEmpty || ct.typeof == typeIsDefault {
			return
		}

		if ct.typeof == typeOmitNil && (kind != reflect.Invalid && current.IsNil()) {
			return
		}

		if ct.typeof == typeOmitZero {
			return
		}

		if ct.hasTag {
			if kind == reflect.Invalid {
				v.str1 = appendAltName(ns, cf.altName)
				if v.v.hasTagNameFunc || v.nameFn != nil {
					v.str2 = string(append(structNs, cf.name...))
				} else {
					v.str2 = v.str1
				}
				v.errs = append(v.errs,
					&fieldError{
						v:              v.v,
						tag:            ct.aliasTag,
						actualTag:      ct.tag,
						ns:             v.str1,
						structNs:       v.str2,
						fieldLen:       uint8(len(cf.altName)),
						structfieldLen: uint8(len(cf.name)),
						segs:           v.errorSegments(segs),
						param:          ct.param,
						kind:           kind,
					},
				)
				return
			}

			v.str1 = appendAltName(ns, cf.altName)
			if v.v.hasTagNameFunc || v.nameFn != nil {
				v.str2 = string(append(structNs, cf.name...))
			} else {
				v.str2 = v.str1
			}
			if !ct.runValidationWhenNil {
				v.errs = append(v.errs,
					&fieldError{
						v:              v.v,
						tag:            ct.aliasTag,
						actualTag:      ct.tag,
						ns:             v.str1,
						structNs:       v.str2,
						fieldLen:       uint8(len(cf.altName)),
						structfieldLen: uint8(len(cf.name)),
						segs:           v.errorSegments(segs),
						value:          getValue(current),
						param:          ct.param,
						kind:           kind,
						typ:            current.Type(),
					},
				)
				return
			}
		}

		if kind == reflect.Invalid {
			return
		}

	case reflect.Struct:
		isNestedStruct = !current.Type().ConvertibleTo(timeType)
		// For backward compatibility before struct level validation tags were supported
		// as there were a number of projects relying on `required` not failing on non-pointer
		// structs. Since it's basically nonsensical to use `required` with a non-pointer struct
		// are explicitly skipping the required validation for it. This WILL be removed in the
		// next major version.
		if isNestedStruct && !v.v.requiredStructEnabled && ct != nil && ct.tag == requiredTag {
			ct = ct.next
		}
	}

	typ = current.Type()

OUTER:
	for {
		if ct == nil || !ct.hasTag || (isNestedStruct && len(cf.name) == 0) {
			// isNestedStruct check here
			if isNestedStruct {
				// if len == 0 then validating using 'Var' or 'VarWithValue'
				// Var - doesn't make much sense to do it that way, should call 'Struct', but no harm...
				// VarWithField - this allows for validating against each field within the struct against a specific value
				//                pretty handy in certain situations
				if len(cf.name) > 0 {
					if len(cf.altName) > 0 {
						ns = append(append(ns, cf.altName...), '.')
					}
					structNs = append(append(structNs, cf.name...), '.')
				}

				v.validateStruct(ctx, parent, current, typ, ns, structNs, segs, ct)
			}
			return
		}

		switch ct.typeof {
		case typeNoStructLevel:
			return

		case typeStructOnly:
			if isNestedStruct {
				// if len == 0 then validating using 'Var' or 'VarWithValue'
				// Var - doesn't make much sense to do it that way, should call 'Struct', but no harm...
				// VarWithField - this allows for validating against each field within the struct against a specific value
				//                pretty handy in certain situations
				if len(cf.name) > 0 {
					if len(cf.altName) > 0 {
						ns = append(append(ns, cf.altName...), '.')
					}
					structNs = append(append(structNs, cf.name...), '.')
				}

				v.validateStruct(ctx, parent, current, typ, ns, structNs, segs, ct)
			}
			return

		case typeOmitEmpty:

			// set Field Level fields
			v.slflParent = parent
			v.flField = current
			v.cf = cf
			v.ct = ct

			if !hasValue(v) {
				return
			}

			ct = ct.next
			continue

		case typeOmitZero:
			v.slflParent = parent
			v.flField = current
			v.cf = cf
			v.ct = ct

			if !hasNotZeroValue(v) {
				return
			}

			ct = ct.next
			continue

		case typeOmitNil:
			v.slflParent = parent
			v.flField = current
			v.cf = cf
			v.ct = ct

			switch field := v.Field(); field.Kind() {
			case reflect.Slice, reflect.Map, reflect.Ptr, reflect.Interface, reflect.Chan, reflect.Func:
				if field.IsNil() {
					return
				}
			default:
				if v.fldIsPointer && getValue(field) == nil {
					return
				}
			}

			ct = ct.next
			continue

		case typeEndKeys:
			return

		case typeDive:

			ct = ct.next

			// traverse slice or map here
			// or panic ;)
			switch kind {
			case reflect.Slice, reflect.Array:

				var i64 int64
				reusableCF := &cField{}
				var indexSegs []FieldPathSegment

				for i := 0; i < current.Len(); i++ {
					i64 = int64(i)

					v.misc = append(v.misc[0:0], cf.name...)
					v.misc = append(v.misc, '[')
					v.misc = strconv.AppendInt(v.misc, i64, 10)
					v.misc = append(v.misc, ']')

					reusableCF.name = string(v.misc)
					indexText := reusableCF.name[len(cf.name):]

					if cf.namesEqual {
						reusableCF.altName = reusableCF.name
					} else {
						v.misc = append(v.misc[0:0], cf.altName...)
						v.misc = append(v.misc, '[')
						v.misc = strconv.AppendInt(v.misc, i64, 10)
						v.misc = append(v.misc, ']')

						reusableCF.altName = string(v.misc)
					}

					if v.nameFn != nil {
						indexSegs = append(segs[:len(segs):len(segs)], FieldPathSegment{
							Kind:        FieldPathSegmentIndex,
							Name:        indexText[1 : len(indexText)-1],
							DisplayName: indexText[1 : len(indexText)-1],
						})
					} else {
						indexSegs = segs
					}

					v.traverseField(ctx, parent, current.Index(i), ns, structNs, indexSegs, reusableCF, ct)
				}

			case reflect.Map:

				var pv string
				reusableCF := &cField{}
				var keySegs []FieldPathSegment

				for _, key := range current.MapKeys() {
					pv = fmt.Sprintf("%v", key)

					v.misc = append(v.misc[0:0], cf.name...)
					v.misc = append(v.misc, '[')
					v.misc = append(v.misc, pv...)
					v.misc = append(v.misc, ']')

					reusableCF.name = string(v.misc)
					keyText := reusableCF.name[len(cf.name):]

					if cf.namesEqual {
						reusableCF.altName = reusableCF.name
					} else {
						v.misc = append(v.misc[0:0], cf.altName...)
						v.misc = append(v.misc, '[')
						v.misc = append(v.misc, pv...)
						v.misc = append(v.misc, ']')

						reusableCF.altName = string(v.misc)
					}

					if v.nameFn != nil {
						keySegs = append(segs[:len(segs):len(segs)], FieldPathSegment{
							Kind:        FieldPathSegmentKey,
							Name:        keyText[1 : len(keyText)-1],
							DisplayName: keyText[1 : len(keyText)-1],
						})
					} else {
						keySegs = segs
					}

					if ct != nil && ct.typeof == typeKeys && ct.keys != nil {
						v.traverseField(ctx, parent, key, ns, structNs, keySegs, reusableCF, ct.keys)
						// can be nil when just keys being validated
						if ct.next != nil {
							v.traverseField(ctx, parent, current.MapIndex(key), ns, structNs, keySegs, reusableCF, ct.next)
						} else {
							// Struct fallback when map values are structs
							val := current.MapIndex(key)
							switch val.Kind() {
							case reflect.Ptr:
								if val.Elem().Kind() == reflect.Struct {
									// Dive into the struct so its own tags run
									v.traverseField(ctx, parent, val, ns, structNs, keySegs, reusableCF, nil)
								}
							case reflect.Struct:
								v.traverseField(ctx, parent, val, ns, structNs, keySegs, reusableCF, nil)
							}
						}
					} else {
						v.traverseField(ctx, parent, current.MapIndex(key), ns, structNs, keySegs, reusableCF, ct)
					}
				}

			default:
				// throw error, if not a slice or map then should not have gotten here
				// bad dive tag
				panic("dive error! can't dive on a non slice or map")
			}

			return

		case typeOr:

			v.misc = v.misc[0:0]

			for {
				// set Field Level fields
				v.slflParent = parent
				v.flField = current
				v.cf = cf
				v.ct = ct

				if ct.fn(ctx, v) {
					if ct.isBlockEnd {
						ct = ct.next
						continue OUTER
					}

					// drain rest of the 'or' values, then continue or leave
					for {
						ct = ct.next

						if ct == nil {
							continue OUTER
						}

						if ct.typeof != typeOr {
							continue OUTER
						}

						if ct.isBlockEnd {
							ct = ct.next
							continue OUTER
						}
					}
				}

				v.misc = append(v.misc, '|')
				v.misc = append(v.misc, ct.tag...)

				if ct.hasParam {
					v.misc = append(v.misc, '=')
					v.misc = append(v.misc, ct.param...)
				}

				if ct.isBlockEnd || ct.next == nil {
					// if we get here, no valid 'or' value and no more tags
					v.str1 = appendAltName(ns, cf.altName)

					if v.v.hasTagNameFunc || v.nameFn != nil {
						v.str2 = string(append(structNs, cf.name...))
					} else {
						v.str2 = v.str1
					}

					if ct.hasAlias {
						v.errs = append(v.errs,
							&fieldError{
								v:              v.v,
								tag:            ct.aliasTag,
								actualTag:      ct.actualAliasTag,
								ns:             v.str1,
								structNs:       v.str2,
								fieldLen:       uint8(len(cf.altName)),
								structfieldLen: uint8(len(cf.name)),
								segs:           v.errorSegments(segs),
								value:          getValue(current),
								param:          ct.param,
								kind:           kind,
								typ:            typ,
							},
						)
					} else {
						tVal := string(v.misc)[1:]

						v.errs = append(v.errs,
							&fieldError{
								v:              v.v,
								tag:            tVal,
								actualTag:      tVal,
								ns:             v.str1,
								structNs:       v.str2,
								fieldLen:       uint8(len(cf.altName)),
								structfieldLen: uint8(len(cf.name)),
								segs:           v.errorSegments(segs),
								value:          getValue(current),
								param:          ct.param,
								kind:           kind,
								typ:            typ,
							},
						)
					}

					return
				}

				ct = ct.next
			}

		default:

			// set Field Level fields
			v.slflParent = parent
			v.flField = current
			v.cf = cf
			v.ct = ct

			if !ct.fn(ctx, v) {
				v.str1 = appendAltName(ns, cf.altName)

				if v.v.hasTagNameFunc || v.nameFn != nil {
					v.str2 = string(append(structNs, cf.name...))
				} else {
					v.str2 = v.str1
				}

				v.errs = append(v.errs,
					&fieldError{
						v:              v.v,
						tag:            ct.aliasTag,
						actualTag:      ct.tag,
						ns:             v.str1,
						structNs:       v.str2,
						fieldLen:       uint8(len(cf.altName)),
						structfieldLen: uint8(len(cf.name)),
						segs:           v.errorSegments(segs),
						value:          getValue(current),
						param:          ct.param,
						kind:           kind,
						typ:            typ,
					},
				)

				return
			}
			ct = ct.next
		}
	}
}

func appendAltName(ns []byte, altName string) string {
	if len(altName) > 0 {
		return string(append(ns, altName...))
	}
	if n := len(ns); n > 0 && ns[n-1] == '.' {
		return string(ns[:n-1])
	}
	return string(ns)
}

func getValue(val reflect.Value) interface{} {
	if val.CanInterface() {
		return val.Interface()
	}

	if val.CanAddr() {
		return reflect.NewAt(val.Type(), unsafe.Pointer(val.UnsafeAddr())).Elem().Interface()
	}

	switch val.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return val.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return val.Uint()
	case reflect.Complex64, reflect.Complex128:
		return val.Complex()
	case reflect.Float32, reflect.Float64:
		return val.Float()
	default:
		return val.String()
	}
}
