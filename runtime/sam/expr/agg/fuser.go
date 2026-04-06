package agg

import (
	"slices"

	"github.com/brimdata/super"
)

// Fuser constructs a fused supertype for all the types passed to Fuse.
type Fuser struct {
	sctx     *super.Context
	complete bool

	typ   super.Type
	types map[super.Type]struct{}
}

// XXX this is used by type checker but I think we can use the other one
func NewFuser(sctx *super.Context, complete bool) *Fuser {
	return &Fuser{sctx: sctx, complete: complete, types: make(map[super.Type]struct{})}
}

func (f *Fuser) Fuse(t super.Type) {
	if _, ok := f.types[t]; ok {
		return
	}
	f.types[t] = struct{}{}
	if f.typ == nil {
		f.typ = f.fuseMono(t)
	} else {
		f.typ = f.fuse(f.typ, t)
	}
}

// Type returns the computed supertype.
func (f *Fuser) Type() super.Type {
	return f.typ
}

func (f *Fuser) fuse(a, b super.Type) super.Type {
	if a == b {
		return a
	}
	if typ, ok := a.(*super.TypeFusion); ok {
		return f.fusion(f.fuse(typ.Type, b))
	}
	if typ, ok := b.(*super.TypeFusion); ok {
		return f.fusion(f.fuse(a, typ.Type))
	}
	switch a := a.(type) {
	case *super.TypeRecord:
		if b, ok := b.(*super.TypeRecord); ok {
			fields := slices.Clone(a.Fields)
			// First change all fields to optional that are in "a" but not in "b".
			for k, field := range fields {
				if _, ok := indexOfField(b.Fields, field.Name); !ok {
					fields[k].Opt = true
				}
			}
			// Now fuse all the fields in "b" that are also in "a" and add the fields
			// that are in "b" but not in "a" as they appear in "b".
			for _, field := range b.Fields {
				i, ok := indexOfField(fields, field.Name)
				if ok {
					fields[i].Type = f.fuse(fields[i].Type, field.Type)
					if field.Opt {
						fields[i].Opt = true
					}
				} else {
					fields = append(fields, super.NewFieldWithOpt(field.Name, field.Type, true))
				}
			}
			fusedRec := f.sctx.MustLookupTypeRecord(fields)
			if recChanged(a, fusedRec) || recChanged(b, fusedRec) {
				return f.fusion(fusedRec)
			}
			return fusedRec
		}
	case *super.TypeArray:
		if b, ok := b.(*super.TypeArray); ok {
			return f.fusion(f.sctx.LookupTypeArray(f.fuse(a.Type, b.Type)))
		}
	case *super.TypeSet:
		if b, ok := b.(*super.TypeSet); ok {
			return f.fusion(f.sctx.LookupTypeSet(f.fuse(a.Type, b.Type)))
		}
	case *super.TypeMap:
		if b, ok := b.(*super.TypeMap); ok {
			keyType := f.fuse(a.KeyType, b.KeyType)
			valType := f.fuse(a.ValType, b.ValType)
			return f.fusion(f.sctx.LookupTypeMap(keyType, valType))
		}
	case *super.TypeUnion:
		out := f.extendUnion(a, b)
		if out == a {
			return a
		}
		return f.fusion(out)
	case *super.TypeEnum:
		if b, ok := b.(*super.TypeEnum); ok {
			var newSymbols []string
			for _, s := range b.Symbols {
				if !slices.Contains(a.Symbols, s) {
					newSymbols = append(newSymbols, s)
				}
			}
			if len(newSymbols) == 0 {
				return a
			}
			symbols := append(slices.Clone(a.Symbols), newSymbols...)
			return f.fusion(f.sctx.LookupTypeEnum(symbols))
		}
	case *super.TypeError:
		if b, ok := b.(*super.TypeError); ok {
			return f.fusion(f.sctx.LookupTypeError(f.fuse(a.Type, b.Type)))
		}
	case *super.TypeNamed:
		if b, ok := b.(*super.TypeNamed); ok && a.Name == b.Name {
			named, err := f.sctx.LookupTypeNamed(a.Name, f.fuse(a.Type, b.Type))
			if err != nil {
				panic(err)
			}
			return f.fusion(named)
		}
	}
	if _, ok := b.(*super.TypeUnion); ok {
		return f.fuse(b, a)
	}
	union, ok := f.sctx.LookupTypeUnion([]super.Type{a, b})
	if !ok {
		panic("a or b can't be anonymous unions at this point")
	}
	return f.fusion(union)
}

func (f *Fuser) fuseMono(typ super.Type) super.Type {
	if typ, ok := typ.(*super.TypeFusion); ok {
		return f.fusion(f.fuseMono(typ.Type))
	}
	var out super.Type
	switch typ := typ.(type) {
	case *super.TypeRecord:
		fields := slices.Clone(typ.Fields)
		for i, field := range fields {
			fields[i].Type = f.fuseMono(field.Type)
		}
		out = f.sctx.MustLookupTypeRecord(fields)
	case *super.TypeArray:
		out = f.sctx.LookupTypeArray(f.fuseMono(typ.Type))
	case *super.TypeSet:
		out = f.sctx.LookupTypeSet(f.fuseMono(typ.Type))
	case *super.TypeMap:
		out = f.fusion(f.sctx.LookupTypeMap(f.fuseMono(typ.KeyType), f.fuseMono(typ.ValType)))
	case *super.TypeUnion:
		types := make([]super.Type, 0, len(typ.Types))
		for _, t := range typ.Types {
			types = append(types, noFusion(f.fuseMono(t)))
		}
		var ok bool
		out, ok = f.sctx.LookupTypeUnion(super.Flatten(types))
		if !ok {
			panic(types)
		}
	case *super.TypeEnum:
		return typ
	case *super.TypeError:
		out = f.sctx.LookupTypeError(f.fuseMono(typ.Type))
	case *super.TypeNamed:
		out, _ = f.sctx.LookupTypeNamed(typ.Name, f.fuseMono(typ.Type))
	default:
		out = typ
	}
	if out != typ {
		out = f.fusion(out)
	}
	return out
}

func (f *Fuser) extendUnion(union *super.TypeUnion, typ super.Type) *super.TypeUnion {
	if fusion, ok := typ.(*super.TypeFusion); ok {
		return f.extendUnion(union, fusion.Type)
	}
	if union.TagOf(typ) >= 0 {
		return union
	}
	if union, ok := f.maybeExtendNamed(union, typ); ok {
		return union
	}
	if union, ok := typ.(*super.TypeUnion); ok {
		for _, t := range union.Types {
			union = f.extendUnion(union, t)
		}
		return union
	}
	out := slices.Clone(union.Types)
	typKind := typ.Kind()
	if typKind != super.PrimitiveKind {
		for i, t := range union.Types {
			if typKind == t.Kind() {
				out[i] = noFusion(f.fuse(t, typ))
				union, ok := f.sctx.LookupTypeUnion(super.UniqueTypes(out))
				if !ok {
					panic(out)
				}
				return union
			}
		}
	}
	union, ok := f.sctx.LookupTypeUnion(super.UniqueTypes(append(out, typ)))
	if !ok {
		panic(typ)
	}
	return union
}

func (f *Fuser) maybeExtendNamed(union *super.TypeUnion, typ super.Type) (*super.TypeUnion, bool) {
	named, ok := typ.(*super.TypeNamed)
	if !ok {
		return nil, false
	}
	for i, t := range union.Types {
		if existingNamed, ok := t.(*super.TypeNamed); ok && existingNamed.Name == named.Name {
			out := slices.Clone(union.Types)
			fused := noFusion(f.fuse(existingNamed.Type, noFusion(named.Type)))
			var err error
			out[i], err = f.sctx.LookupTypeNamed(named.Name, fused)
			if err != nil {
				panic(err)
			}
			union, ok := f.sctx.LookupTypeUnion(out)
			if !ok {
				panic(out)
			}
			return union, true
		}
	}
	return nil, false
}

func noFusion(typ super.Type) super.Type {
	if s, ok := typ.(*super.TypeFusion); ok {
		return s.Type
	}
	return typ
}

func (f *Fuser) fusion(typ super.Type) super.Type {
	if !f.complete {
		return typ
	}
	if typ, ok := typ.(*super.TypeFusion); ok {
		return typ
	}
	return f.sctx.LookupTypeFusion(typ)
}

func indexOfField(fields []super.Field, name string) (int, bool) {
	for i, f := range fields {
		if f.Name == name {
			return i, true
		}
	}
	return -1, false
}

// recChanged returns true iff the two record types are different
// enough after fusing that they need to be wrapped in a fusion type.
// As long as all the fields names and optionality are the same, then
// any type differences in the fused type of the child fields will be
// captured by a fusion wrapper somewhere in the descendent type.
func recChanged(a, b *super.TypeRecord) bool {
	if len(a.Fields) != len(b.Fields) {
		return true
	}
	for k, af := range a.Fields {
		bf := b.Fields[k]
		if af.Name != bf.Name || af.Opt != bf.Opt {
			return true
		}
	}
	return false
}
