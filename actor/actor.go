// Package actor implements tooling to write and manipulate actors in go.
package actor

import (
	"bytes"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"

	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/exec"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"

	"github.com/attic-labs/noms/go/chunks"
	"github.com/attic-labs/noms/go/hash"
	"github.com/attic-labs/noms/go/marshal"
	noms "github.com/attic-labs/noms/go/types"
)

func init() {
	cbor.RegisterCborType(nomsStorage{})
}

// MakeTypedExport finds the correct method on the given actor and returns it.
// The returned function is wrapped such that it takes care of serialization and type checks.
//
// TODO: the work of creating the wrapper should be ideally done at compile time, otherwise at least only once + cached
// TODO: find a better name, naming is hard..
// TODO: Ensure the method is not empty. We need to be paranoid we're not calling methods on transfer messages.
func MakeTypedExport(actor exec.ExecutableActor, method string) exec.ExportedFunc {
	f, ok := reflect.TypeOf(actor).MethodByName(strings.Title(method))
	if !ok {
		panic(fmt.Sprintf("MakeTypedExport could not find passed in method in actor: %s", method))
	}

	exports := actor.Exports()
	signature, ok := exports[method]
	if !ok {
		panic(fmt.Sprintf("MakeTypedExport could not find passed in method in exports: %s", method))
	}

	val := f.Func
	t := f.Type

	badImpl := func() {
		params := []string{"exec.VMContext"}
		for _, p := range signature.Params {
			params = append(params, p.String())
		}
		ret := []string{}
		for _, r := range signature.Return {
			ret = append(ret, r.String())
		}
		ret = append(ret, "uint8", "error")
		sig := fmt.Sprintf("func (Actor, %s) (%s)", strings.Join(params, ", "), strings.Join(ret, ", "))
		panic(fmt.Sprintf("MakeTypedExport must receive a function with signature: %s, but got: %s", sig, t))
	}

	if t.Kind() != reflect.Func || t.NumIn() != 2+len(signature.Params) || t.NumOut() != 2+len(signature.Return) {
		badImpl()
	}

	for i, p := range signature.Params {
		if !abi.TypeMatches(p, t.In(i+2)) {
			badImpl()
		}
	}

	for i, r := range signature.Return {
		if !abi.TypeMatches(r, t.Out(i)) {
			badImpl()
		}
	}

	exitType := reflect.Uint8
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	if t.Out(t.NumOut()-2).Kind() != exitType {
		badImpl()
	}

	if !t.Out(t.NumOut() - 1).Implements(errorType) {
		badImpl()
	}

	return func(ctx exec.VMContext) ([]byte, uint8, error) {
		params, err := abi.DecodeValues(ctx.Message().Params, signature.Params)
		if err != nil {
			return nil, 1, errors.RevertErrorWrap(err, "invalid params")
		}

		args := []reflect.Value{
			reflect.ValueOf(actor),
			reflect.ValueOf(ctx),
		}

		for _, param := range params {
			args = append(args, reflect.ValueOf(param.Val))
		}

		toInterfaces := func(v []reflect.Value) []interface{} {
			r := make([]interface{}, 0, len(v))
			for _, vv := range v {
				r = append(r, vv.Interface())
			}
			return r
		}

		out := toInterfaces(val.Call(args))

		exitCode, ok := out[len(out)-2].(uint8)
		if !ok {
			panic("invalid return value")
		}

		var retVal []byte
		outErr, ok := out[len(out)-1].(error)
		if ok {
			if !(errors.ShouldRevert(outErr) || errors.IsFault(outErr)) {
				panic("you are a bad person: error must be either a reverterror or a fault")
			}
		} else {
			// The value of the returned error was nil.
			outErr = nil

			retVal, err = abi.ToEncodedValues(out[:len(out)-2]...)
			if err != nil {
				return nil, 1, errors.FaultErrorWrap(err, "failed to marshal output value")
			}
		}

		return retVal, exitCode, outErr
	}
}

// MarshalValue serializes a given go type into a byte slice.
// The returned format matches the format that is expected to be interoperapble between VM and
// the rest of the system.
func MarshalValue(val interface{}) ([]byte, error) {
	switch t := val.(type) {
	case *big.Int:
		if t == nil {
			return []byte{}, nil
		}
		return t.Bytes(), nil
	case *types.ChannelID:
		if t == nil {
			return []byte{}, nil
		}
		return t.Bytes(), nil
	case *types.BlockHeight:
		if t == nil {
			return []byte{}, nil
		}
		return t.Bytes(), nil
	case []byte:
		return t, nil
	case string:
		return []byte(t), nil
	case types.Address:
		if t == (types.Address{}) {
			return []byte{}, nil
		}
		return t.Bytes(), nil
	default:
		return nil, fmt.Errorf("unknown type: %s", reflect.TypeOf(t))
	}
}

// --
// Below are helper functions that are used to implement actors.

// MarshalStorage encodes the passed in data into bytes.
func MarshalStorage(in interface{}) ([]byte, error) {
	return cbor.DumpObject(in)
}

func MarshalStorageNoms(in interface{}, vs *noms.ValueStore) ([]byte, error) {
	newRoot := marshal.MustMarshal(vs, in)
	vs.WriteValue(newRoot)
	vs.Commit(newRoot.Hash(), vs.Root())

	ns := nomsStorage{}
	h := newRoot.Hash()
	ns.Head = h[:]
	cs := vs.ChunkStore().(*chunkStore)
	for _, c := range cs.data {
		ns.Values = append(ns.Values, c.Data())
	}

	// Sort because cs.data is a map and iteration is non-deterministic
	sort.Slice(ns.Values, func(i, j int) bool {
		return bytes.Compare(ns.Values[i], ns.Values[j]) < 0
	})

	data, err := MarshalStorage(ns)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// UnmarshalStorage decodes the passed in bytes into the given object.
func UnmarshalStorage(raw []byte, to interface{}) error {
	return cbor.DecodeInto(raw, to)
}

func UnmarshalStorageNoms(raw []byte, to interface{}) (vs *noms.ValueStore, err error) {
	// Temp hack: Read the serialized storage from the old storage system
	var ns nomsStorage
	if err = UnmarshalStorage(raw, &ns); err != nil {
		return nil, err
	}

	// Populate the chunkstore
	vs = NewValueStore()
	for _, vb := range ns.Values {
		vs.ChunkStore().Put(chunks.NewChunk(vb))
	}
	vs.Commit(hash.New(ns.Head), vs.Root())

	if !vs.Root().IsEmpty() {
		if err = marshal.Unmarshal(vs.ReadValue(vs.Root()), to); err != nil {
			return nil, err
		}
	}

	return vs, nil
}

// WithStorage is a helper method that makes dealing with storage serialization
// easier for implementors.
// It is designed to be used like:
//
// var st MyStorage
// ret, err := WithStorage(ctx, &st, func() (interface{}, error) {
//   fmt.Println("hey look, my storage is loaded: ", st)
//   return st.Thing, nil
// })
//
// Note that if 'f' returns an error, modifications to the storage are not
// saved.
func WithStorage(ctx exec.VMContext, st interface{}, f func() (interface{}, error)) (interface{}, error) {
	if err := UnmarshalStorage(ctx.ReadStorage(), st); err != nil {
		return nil, err
	}

	ret, err := f()
	if err != nil {
		return nil, err
	}

	data, err := MarshalStorage(st)
	if err != nil {
		return nil, err
	}

	if err := ctx.WriteStorage(data); err != nil {
		return nil, err
	}

	return ret, nil
}

func WithStorageNoms(ctx exec.VMContext, st interface{}, f func(vrw noms.ValueReadWriter) (interface{}, error)) error {
	vs, err := UnmarshalStorageNoms(ctx.ReadStorage(), st)
	if err != nil {
		return err
	}

	// Invoke user code
	newRoot, err := f(vs)
	if err != nil {
		return err
	}

	// If return value is nil, nothing to do
	if newRoot == nil {
		return nil
	}

	// Otherwise write the new root to the value store
	data, err := MarshalStorageNoms(newRoot, vs)
	if err := ctx.WriteStorage(data); err != nil {
		return err
	}

	return nil
}

// PresentStorage returns a representation of an actor's storage in a domain-specific form suitable for conversion to
// JSON.
// TODO: Can this go away with Noms?
func PresentStorage(act exec.ExecutableActor, mem []byte) interface{} {
	s := act.NewStorage()
	err := UnmarshalStorage(mem, s)
	if err != nil {
		return nil
	}
	return s
}

type nomsStorage struct {
	Head   []byte
	Values [][]byte
}
