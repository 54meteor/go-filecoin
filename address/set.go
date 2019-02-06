package address

import (
	"sort"

	cbor "gx/ipfs/QmRoARq3nkUb13HSKZGepCZSWe5GrVPwx7xURJGZ7KWv9V/go-ipld-cbor"
	"gx/ipfs/QmfWqohMtbivn5NRJvtrLzCW3EU4QmoLvVNtmvo9vbdtVA/refmt/obj/atlas"
)

func init() {
	cbor.RegisterCborType(addrSetEntry)
}

// Set is a set of addresses
type Set map[Address]struct{}

const Length = 22

var addrSetEntry = atlas.BuildEntry(Set{}).Transform().
	TransformMarshal(atlas.MakeMarshalTransformFunc(
		func(s Set) ([]byte, error) {
			out := make([]string, 0, len(s))
			for k := range s {
				out = append(out, string(k.Bytes()))
			}

			sort.Strings(out)

			bytes := make([]byte, 0, len(out)*Length)
			for _, k := range out {
				bytes = append(bytes, []byte(k)...)
			}
			return bytes, nil
		})).
	TransformUnmarshal(atlas.MakeUnmarshalTransformFunc(
		func(vals []byte) (Set, error) {
			out := make(Set)
			for i := 0; i < len(vals); i += Length {
				end := i + Length
				if end > len(vals) {
					end = len(vals)
				}
				out[Address(vals[i:end])] = struct{}{}
			}
			return out, nil
		})).
	Complete()
