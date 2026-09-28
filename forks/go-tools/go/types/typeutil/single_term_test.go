package typeutil

import (
	"go/types"
	"testing"
)

func TestSingleTermMatchesNormalTerms(t *testing.T) {
	pkg := types.NewPackage("pkg", "pkg")
	tInt := types.Typ[types.Int]
	named := types.NewNamed(types.NewTypeName(0, pkg, "MyInt", nil), tInt, nil)
	iface := types.NewInterfaceType(nil, nil)
	namedIface := types.NewNamed(types.NewTypeName(0, pkg, "Iface", nil), iface, nil)
	union := types.NewInterfaceType(nil, []types.Type{types.NewUnion([]*types.Term{
		types.NewTerm(true, tInt),
		types.NewTerm(false, types.Typ[types.String]),
	})})
	tparam := types.NewTypeParam(types.NewTypeName(0, pkg, "T", nil), union)
	alias := types.NewAlias(types.NewTypeName(0, pkg, "A", nil), named)

	cases := map[string]types.Type{
		"basic":             tInt,
		"untyped":           types.Typ[types.UntypedInt],
		"invalid":           types.Typ[types.Invalid],
		"named":             named,
		"alias":             alias,
		"pointer":           types.NewPointer(named),
		"slice":             types.NewSlice(tInt),
		"send chan":         types.NewChan(types.SendOnly, tInt),
		"struct":            types.NewStruct(nil, nil),
		"signature":         types.NewSignatureType(nil, nil, nil, nil, nil, false),
		"empty interface":   iface,
		"named interface":   namedIface,
		"union interface":   union,
		"type parameter":    tparam,
		"pointer to tparam": types.NewPointer(tparam),
	}
	for name, typ := range cases {
		t.Run(name, func(t *testing.T) {
			got, want := NewTypeSet(typ), normalTypeSet(typ)
			if got.empty != want.empty || len(got.Terms) != len(want.Terms) {
				t.Fatalf("NewTypeSet = %v (empty %t), want %v (empty %t)", got.Terms, got.empty, want.Terms, want.empty)
			}
			for i := range got.Terms {
				if got.Terms[i].Tilde() != want.Terms[i].Tilde() || !types.Identical(got.Terms[i].Type(), want.Terms[i].Type()) {
					t.Errorf("term %d = %v, want %v", i, got.Terms[i], want.Terms[i])
				}
			}
			gotCore, wantCore := CoreType(typ), want.CoreType()
			if (gotCore == nil) != (wantCore == nil) || gotCore != nil && !types.Identical(gotCore, wantCore) {
				t.Errorf("CoreType = %v, want %v", gotCore, wantCore)
			}
		})
	}
}

func TestCoreTypeSingleTermDoesNotAllocate(t *testing.T) {
	named := types.NewNamed(types.NewTypeName(0, types.NewPackage("pkg", "pkg"), "MyInt", nil), types.Typ[types.Int], nil)
	if allocs := testing.AllocsPerRun(100, func() { CoreType(named) }); allocs != 0 {
		t.Errorf("CoreType allocates %.0f times per call, want 0", allocs)
	}
}
