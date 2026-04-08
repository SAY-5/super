package csup

import (
	"slices"

	"github.com/brimdata/super"
	"github.com/brimdata/super/pkg/field"
	"github.com/brimdata/super/pkg/unpack"
	"github.com/brimdata/super/scode"
)

type Metadata interface {
	Len(*Context) uint32
}

type Record struct {
	Kind   string `json:"kind" unpack:""`
	Length uint32
	Fields []Field
}

func (r *Record) Len(*Context) uint32 {
	return r.Length
}

func (r *Record) LookupField(name string) *Field {
	for k, field := range r.Fields {
		if field.Name == name {
			return &r.Fields[k]
		}
	}
	return nil
}

func under(cctx *Context, meta Metadata) Metadata {
	for {
		switch inner := meta.(type) {
		case *Named:
			meta = cctx.Lookup(inner.Values)
		default:
			return meta
		}
	}
}

type Field struct {
	Name   string
	Values ID
	Opt    bool
	Nones  Segment
}

type Array struct {
	Kind    string `json:"kind" unpack:""`
	Length  uint32
	Lengths Segment
	Values  ID
}

func (a *Array) Len(*Context) uint32 {
	return a.Length
}

type Set Array

func (s *Set) Len(*Context) uint32 {
	return s.Length
}

type Map struct {
	Kind    string `json:"kind" unpack:""`
	Length  uint32
	Lengths Segment
	Keys    ID
	Values  ID
}

func (m *Map) Len(*Context) uint32 {
	return m.Length
}

type Union struct {
	Kind   string `json:"kind" unpack:""`
	Length uint32
	Tags   Segment
	Values []ID
}

func (u *Union) Len(*Context) uint32 {
	return u.Length
}

type Named struct {
	Kind   string `json:"kind" unpack:""`
	Name   string
	Values ID
}

func (n *Named) Len(cctx *Context) uint32 {
	return cctx.Lookup(n.Values).Len(cctx)
}

type Error struct {
	Kind   string `json:"kind" unpack:""`
	Values ID
}

func (e *Error) Len(cctx *Context) uint32 {
	return cctx.Lookup(e.Values).Len(cctx)
}

type Fusion struct {
	Kind   string `json:"kind" unpack:""`
	Values ID
	// Subtypes are stored as type IDs in the local context of
	// the CSUP object in which the type appears.  vcache translates
	// these local types to the query sctx.
	Subtypes Segment
}

func (f *Fusion) Len(cctx *Context) uint32 {
	return cctx.Lookup(f.Values).Len(cctx)
}

type Int struct {
	Kind     string `json:"kind" unpack:""`
	TypeID   int
	Location Segment
	Min      int64
	Max      int64
	Count    uint32
}

func (i *Int) Type(*Context, *super.Context) super.Type {
	return primitive(i.TypeID)
}

func primitive(id int) super.Type {
	t, err := super.LookupPrimitiveByID(id)
	if err != nil {
		panic(err)
	}
	return t
}

func (i *Int) Len(*Context) uint32 {
	return i.Count
}

type Uint struct {
	Kind     string `json:"kind" unpack:""`
	TypeID   int
	Location Segment
	Min      uint64
	Max      uint64
	Count    uint32
}

func (u *Uint) Type(*Context, *super.Context) super.Type {
	return primitive(u.TypeID)
}

func (u *Uint) Len(*Context) uint32 {
	return u.Count
}

type Float struct {
	Kind     string `json:"kind" unpack:""`
	TypeID   int
	Location Segment
	Min      float64
	Max      float64
	Count    uint32
}

func (f *Float) Type(*Context, *super.Context) super.Type {
	return primitive(f.TypeID)
}

func (f *Float) Len(*Context) uint32 {
	return f.Count
}

type Bytes struct {
	Kind    string `json:"kind" unpack:""`
	TypeID  int
	Bytes   Segment
	Offsets Segment
	Min     []byte
	Max     []byte
	Count   uint32
}

func (b *Bytes) Type(*Context, *super.Context) super.Type {
	return primitive(b.TypeID)
}

func (b *Bytes) Len(*Context) uint32 {
	return b.Count
}

type Primitive struct {
	Kind     string `json:"kind" unpack:""`
	TypeID   int
	Location Segment
	Min      *super.Value
	Max      *super.Value
	Count    uint32
}

func (p *Primitive) Type(*Context, *super.Context) super.Type {
	return primitive(p.TypeID)
}

func (p *Primitive) Len(*Context) uint32 {
	return p.Count
}

type Const struct {
	Kind   string `json:"kind" unpack:""`
	TypeID int
	Bytes  []byte
	Count  uint32
}

func (c *Const) Type(*Context, *super.Context) super.Type {
	return primitive(c.TypeID)
}

func (c *Const) Len(*Context) uint32 {
	return c.Count
}

func (c *Const) Value() super.Value {
	return super.NewValue(c.Type(nil, nil), c.Bytes)
}

type Dict struct {
	Kind   string `json:"kind" unpack:""`
	Values ID
	Counts Segment
	Index  Segment
	Length uint32
}

func (d *Dict) Len(*Context) uint32 {
	return d.Length
}

type Dynamic struct {
	Kind   string `json:"kind" unpack:""`
	Tags   Segment
	Values []ID
	Length uint32
}

var _ Metadata = (*Dynamic)(nil)

func (*Dynamic) Type(*Context, *super.Context) super.Type {
	panic("Type should not be called on Dynamic")
}

func (d *Dynamic) Len(*Context) uint32 {
	return d.Length
}

func metadataValue(cctx *Context, sctx *super.Context, b *scode.Builder, id ID, projection field.Projection) super.Type {
	m := cctx.Lookup(id)
	switch m := under(cctx, m).(type) {
	case *Dict:
		return metadataValue(cctx, sctx, b, m.Values, projection)
	case *Record:
		var fields []super.Field
		b.BeginContainer()
		if len(projection) == 0 {
			for _, f := range m.Fields {
				typ := metadataValue(cctx, sctx, b, f.Values, nil)
				fields = append(fields, super.NewFieldWithOpt(f.Name, typ, f.Opt))
			}
		} else {
			for _, node := range projection {
				if k := indexOfField(node.Name, m.Fields); k >= 0 {
					typ := metadataValue(cctx, sctx, b, m.Fields[k].Values, node.Proj)
					fields = append(fields, super.NewFieldWithOpt(node.Name, typ, m.Fields[k].Opt))
				}
			}
		}
		b.EndContainer()
		return sctx.MustLookupTypeRecord(fields)
	case *Primitive:
		min, max := super.Null, super.Null
		if m.Min != nil {
			min = *m.Min
		}
		if m.Max != nil {
			max = *m.Max
		}
		return metadataLeaf(sctx, b, min, max)
	case *Int:
		return metadataLeaf(sctx, b, super.NewInt(primitive(m.TypeID), m.Min), super.NewInt(primitive(m.TypeID), m.Max))
	case *Uint:
		return metadataLeaf(sctx, b, super.NewUint(primitive(m.TypeID), m.Min), super.NewUint(primitive(m.TypeID), m.Max))
	case *Float:
		return metadataLeaf(sctx, b, super.NewFloat(primitive(m.TypeID), m.Min), super.NewFloat(primitive(m.TypeID), m.Max))
	case *Bytes:
		return metadataLeaf(sctx, b, super.NewValue(primitive(m.TypeID), m.Min), super.NewValue(primitive(m.TypeID), m.Max))
	case *Const:
		val := m.Value()
		return metadataLeaf(sctx, b, val, val)
	default:
		b.Append(nil)
		return super.TypeNull
	}
}

func metadataLeaf(sctx *super.Context, b *scode.Builder, min, max super.Value) super.Type {
	b.BeginContainer()
	b.Append(min.Bytes())
	b.Append(max.Bytes())
	b.EndContainer()
	return sctx.MustLookupTypeRecord([]super.Field{
		super.NewField("min", min.Type()),
		super.NewField("max", max.Type()),
	})
}

func indexOfField(name string, fields []Field) int {
	return slices.IndexFunc(fields, func(f Field) bool {
		return f.Name == name
	})
}

var unpacker = unpack.New(
	Record{},
	Array{},
	Set{},
	Map{},
	Union{},
	Int{},
	Uint{},
	Float{},
	Bytes{},
	Primitive{},
	Named{},
	Error{},
	Const{},
	Dict{},
	Dynamic{},
	Fusion{},
)
