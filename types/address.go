package types

import (
	"fmt"
	"strings"

	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmZp3eKdYQHHAneECmeK6HhiMwTPufmjC8DuuaGKv3unvx/blake2b-simd"
	"gx/ipfs/QmcrriCMhjb5ZWzmPNxmP53px47tSPcXBNaMtLdgcKFJYk/refmt/obj/atlas"
)

func init() {
	cbor.RegisterCborType(AddressAtlasEntry)
}

var AddressAtlasEntry = atlas.BuildEntry(Address{}).Transform().
	TransformMarshal(atlas.MakeMarshalTransformFunc(
		func(a Address) (string, error) {
			return a.String(), nil
		})).
	TransformUnmarshal(atlas.MakeUnmarshalTransformFunc(
		func(x string) (Address, error) {
			return NewAddressFromString(x)
		})).
	Complete()

var addressHashConfig = &blake2b.Config{Size: AddressHashLength}

// AddressHash hashes the given input using the default address hashing function,
// Blake2b 160.
func AddressHash(input []byte) ([]byte, error) {
	hasher, err := blake2b.New(addressHashConfig)
	if err != nil {
		return nil, err
	}
	if _, err := hasher.Write(input); err != nil {
		return nil, err
	}
	return hasher.Sum(nil), nil
}

// MakeTestAddress creates a new testnet address by hashing the given string
func MakeTestAddress(input string) Address {
	hash, err := AddressHash([]byte(input))
	if err != nil {
		panic(err)
	}

	return NewTestnetAddress(hash)
}

// Network represents which network an address belongs to.
type Network = byte

const (
	// Mainnet is the main network.
	Mainnet Network = iota
	// Testnet is the test network.
	Testnet
)

var (
	// ErrUnknownNetwork is returned when encountering an unknown network in an address.
	ErrUnknownNetwork = errors.New("unknown network")
	// ErrUnknownVersion is returned when encountering an unknown address version.
	ErrUnknownVersion = errors.New("unknown version")
	// ErrInvalidBytes is returned when encountering an invalid byte format.
	ErrInvalidBytes = errors.New("invalid bytes")
)

// NetworkFromString tries to convert the string representation of a network to
// its binary representation.
func NetworkFromString(input string) (Network, error) {
	switch input {
	case "fc":
		return Mainnet, nil
	case "tf":
		return Testnet, nil
	default:
		return 0, ErrUnknownNetwork
	}
}

// NetworkToString creates a human readable representation of the network.
func NetworkToString(n Network) string {
	switch n {
	case Mainnet:
		return "fc"
	case Testnet:
		return "tf"
	default:
		panic("invalid network")
	}
}

// Address is the go type that represents an address in the filecoin network.
type Address [AddressLength]byte

// NewMainnetAddress constructs a new mainnet address.
func NewMainnetAddress(hash []byte) Address {
	return NewAddress(Mainnet, hash)
}

// NewTestnetAddress constructs a new testnet address.
func NewTestnetAddress(hash []byte) Address {
	return NewAddress(Testnet, hash)
}

// NewAddress constructs a new address for the given nework.
func NewAddress(network Network, hash []byte) Address {
	var addr [AddressLength]byte
	addr[0] = network
	addr[1] = AddressVersion
	copy(addr[2:], hash)
	return addr
}

// NewAddressFromString tries to parse a given string into a filecoin address.
func NewAddressFromString(s string) (Address, error) {
	networkString, version, hash, err := decode(s)
	if err != nil {
		return Address{}, err
	}

	network, err := NetworkFromString(networkString)
	if err != nil {
		return Address{}, err
	}

	if version != AddressVersion {
		return Address{}, ErrUnknownVersion
	}

	return NewAddress(network, hash), nil
}

// NewAddressFromBytes tries to create an address from the given bytes.
func NewAddressFromBytes(raw []byte) (Address, error) {
	if len(raw) != 22 {
		return Address{}, ErrInvalidBytes
	}

	network := raw[0]
	if network > Testnet {
		return Address{}, ErrUnknownNetwork
	}

	version := raw[1]
	if version != AddressVersion {
		return Address{}, ErrUnknownVersion
	}

	return NewAddress(network, raw[2:]), nil
}

// ParseError checks if the given address parses as a valid filecoin address.
func ParseError(addr string) error {
	hrp, version, data, err := decode(addr)
	if err != nil {
		return errors.Wrap(err, "unable to decode")
	}

	if _, err := NetworkFromString(hrp); err != nil {
		return errors.Wrap(err, "invalid network")
	}

	if version != AddressVersion {
		return fmt.Errorf("invalid version: version=%d", version)
	}

	if len(data) != AddressHashLength {
		return fmt.Errorf("invalid data length: len=%d", len(data))
	}

	return nil
}

// Empty returns true if the address is empty.
func (a Address) Empty() bool {
	return a == (Address{})
}

// Network returns the network of the address.
func (a Address) Network() Network {
	return a[0]
}

// Version returns the version of the address.
func (a Address) Version() byte {
	return a[1]
}

// Hash returns the hash part of the address.
func (a Address) Hash() []byte {
	return a[2:]
}

// Format implements the Formatter interface.
func (a Address) Format(f fmt.State, c rune) {
	switch c {
	case 'v':
		fmt.Fprintf(f, "[%s - %x - %x]", NetworkToString(a.Network()), a.Version(), a.Hash())
	case 's':
		fmt.Fprintf(f, "%s", a.String())
	default:
		fmt.Fprintf(f, "%"+string(c), a.Bytes())
	}
}

// MarshalText implements the TextMarshaler interface.
func (a Address) MarshalText() ([]byte, error) {
	if a == (Address{}) {
		return nil, nil
	}

	return []byte(a.String()), nil
}

// UnmarshalText implements the TextUnmarshaler interface.
func (a *Address) UnmarshalText(in []byte) error {
	if len(in) == 0 {
		return nil
	}

	out, err := NewAddressFromString(string(in))
	if err != nil {
		return err
	}

	copy(a[:], out[:])

	return nil
}

func (a Address) String() string {
	out, err := encode(NetworkToString(a.Network()), a.Version(), a.Hash())
	if err != nil {
		// should really not happen
		panic(err)
	}

	return out
}

// Bytes returns the byte representation of the address.
func (a Address) Bytes() []byte {
	return a[:]
}

// --
// TODO: find a better place for the things below

// encode encodes hrp(human-readable part) a version(byte) and data(32bit data array)
func encode(hrp string, version byte, data []byte) (string, error) {
	if len(hrp) != 2 {
		return "", fmt.Errorf("hrp has invalid length: hrp=%d", len(hrp))
	}
	if len(data) != AddressHashLength {
		return "", fmt.Errorf("data is malformed: data length=%d", len(data))
	}
	for p, c := range hrp {
		if c < 33 || c > 126 {
			return "", fmt.Errorf("invalid character human-readable part : hrp[%d]=%d", p, c)
		}
	}
	if version > 31 {
		return "", fmt.Errorf("version too large")
	}

	hrp = strings.ToLower(hrp)
	combined := append([]byte{version}, Base32.EncodeToBytes(data)...)
	sum := createChecksum(hrp, combined)

	toEncode := append(combined, sum...)
	encoded := make([]byte, len(toEncode))

	for i, p := range toEncode {
		if p >= 32 {
			return "", fmt.Errorf("invalid data")
		}
		encoded[i] += Base32Charset[p]
	}

	return hrp + string(encoded), nil
}

// decode decodes a filecoin address and returns
// the hrp(human-readable part), version (1byte) and data(32bit data array)
// or an error.
func decode(addr string) (string, byte, []byte, error) {
	if len(addr) < 2 {
		return "", 0, nil, fmt.Errorf("address too short")
	}
	if len(addr) > 41 {
		return "", 0, nil, fmt.Errorf("too long: len=%d", len(addr))
	}
	if strings.ToLower(addr) != addr && strings.ToUpper(addr) != addr {
		return "", 0, nil, fmt.Errorf("mixed case")
	}
	addr = strings.ToLower(addr)
	hrp := addr[0:2]
	for p, c := range hrp {
		if c < 33 || c > 126 {
			return "", 0, nil, fmt.Errorf("invalid character human-readable part: addr[%d]=%d", p, c)
		}
	}

	// check data
	data := addr[2:]
	dataBytes := []byte{}
	for _, b := range data {
		// alphanumeric only
		if !((b >= int32('0') && b <= int32('9')) || (b >= int32('A') && b <= int32('Z')) || (b >= int32('a') && b <= int32('z'))) {
			return "", 0, nil, fmt.Errorf("non alphanumeric character")
		}

		// exclude [1,b,i,o]
		if b == int32('1') || b == int32('b') || b == int32('i') || b == int32('o') {
			return "", 0, nil, fmt.Errorf("invalid character")
		}

		dataBytes = append(dataBytes, byte(Base32CharsetReverse[int(b)]))
	}

	if !verifyChecksum(hrp, dataBytes) {
		return "", 0, nil, fmt.Errorf("invalid checksum")
	}

	decodedBytes, err := Base32.DecodeFromBytes(dataBytes[1 : len(dataBytes)-6])
	if err != nil {
		return "", 0, nil, err
	}
	if len(decodedBytes) != AddressHashLength {
		return "", 0, nil, fmt.Errorf("invalid data size: len=%d", len(decodedBytes))
	}
	return hrp, dataBytes[0], decodedBytes, nil
}

var generator = []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func polymod(values []byte) uint32 {
	chk := uint32(1)
	var b byte
	for _, v := range values {
		b = byte(chk >> 25)
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (b>>uint(i))&1 == 1 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []byte {
	out := []byte{}
	for _, char := range hrp {
		out = append(out, byte(char>>5))
	}

	out = append(out, 0)

	for _, char := range hrp {
		out = append(out, byte(char&31))
	}

	return out
}

func verifyChecksum(hrp string, data []byte) bool {
	return polymod(append(hrpExpand(hrp), data...)) == 1
}

func createChecksum(hrp string, data []byte) []byte {
	values := append(append(hrpExpand(hrp), data...), []byte{0, 0, 0, 0, 0, 0}...)
	mod := polymod(values) ^ 1

	checksum := make([]byte, 6)
	for p := range checksum {
		checksum[p] = byte((mod >> uint32(5*(5-p))) & 0x1f & 31)
	}

	return checksum
}
