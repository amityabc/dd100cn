package mailwire

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"

	"golang.org/x/text/encoding/simplifiedchinese"
)

var invalidRecipient = errors.New("invalid native mail recipient")

// CP936's private-use mappings differ from the WHATWG GBK index used by
// x/text. Each range is (first two-byte pointer, last pointer, first scalar).
// The digest of all 23,940 decoded words is checked against the original
// .NET provider in TestStrictRecipientCodePage. Every admitted pair round-trips.
var private936 = [...][3]uint16{
	{6080, 6175, 0xE4C6}, {6270, 6365, 0xE526}, {6376, 6381, 0xE766}, {6432, 6433, 0xE76C},
	{6444, 6445, 0xE76E}, {6458, 6459, 0xE770}, {6460, 6555, 0xE586}, {6650, 6745, 0xE5E6},
	{6829, 6839, 0xE772}, {6840, 6935, 0xE646}, {7022, 7029, 0xE77D}, {7030, 7125, 0xE6A6},
	{7150, 7157, 0xE785}, {7182, 7188, 0xE78D}, {7201, 7202, 0xE794}, {7208, 7208, 0xE796},
	{7211, 7219, 0xE797}, {7220, 7315, 0xE706}, {7349, 7363, 0xE7A0}, {7397, 7409, 0xE7AF},
	{7495, 7505, 0xE7BC}, {7533, 7533, 0xE7C7}, {7536, 7536, 0xE7C8}, {7538, 7541, 0xE7C9},
	{7579, 7599, 0xE7CD}, {7624, 7624, 0xE7E2}, {7627, 7627, 0xE7E3}, {7629, 7631, 0xE7E4},
	{7672, 7684, 0xE7E7}, {7686, 7698, 0xE7F4}, {7775, 7789, 0xE801}, {7886, 7979, 0xE000},
	{8076, 8169, 0xE05E}, {8266, 8359, 0xE0BC}, {8456, 8549, 0xE11A}, {8646, 8739, 0xE178},
	{8836, 8929, 0xE1D6}, {16525, 16529, 0xE810}, {22706, 22799, 0xE234}, {22896, 22989, 0xE292},
	{23086, 23179, 0xE2F0}, {23276, 23369, 0xE34E}, {23466, 23559, 0xE3AC}, {23656, 23749, 0xE40A},
	{23766, 23845, 0xE815}, {23846, 23939, 0xE468},
}

type recipientCodePage struct {
	pairs   [126 * 190]uint16
	encoded [65536]uint16
}

func pairAt(i int) uint16 {
	trail := i%190 + 0x40
	if trail >= 0x7f {
		trail++
	}
	return uint16((0x81+i/190)<<8 | trail)
}

var codePage936 = sync.OnceValue(func() *recipientCodePage {
	var table recipientCodePage
	source := make([]byte, 0, 2*len(table.pairs))
	for i := range table.pairs {
		source = binary.BigEndian.AppendUint16(source, pairAt(i))
	}
	decoded, _ := simplifiedchinese.GBK.NewDecoder().Bytes(source)
	for i, r := range []rune(string(decoded)) {
		table.pairs[i] = uint16(r)
	}
	for _, r := range private936 {
		for i := r[0]; i <= r[1]; i++ {
			table.pairs[i] = r[2] + i - r[0]
		}
	}
	for i, r := range table.pairs {
		table.encoded[r] = pairAt(i)
	}
	table.encoded['€'] = 0x80
	table.encoded['\uf8f5'] = 0xff
	return &table
})

func decode936(b []byte) (string, error) {
	var text strings.Builder
	for len(b) > 0 {
		first := b[0]
		b = b[1:]
		switch {
		case first == 0:
			return "", invalidRecipient
		case first < 0x80:
			text.WriteByte(first)
		case first == 0x80:
			text.WriteRune('€')
		case first == 0xff:
			text.WriteRune('\uf8f5')
		default:
			if len(b) == 0 || b[0] < 0x40 || b[0] == 0x7f || b[0] == 0xff {
				return "", invalidRecipient
			}
			trail := int(b[0]) - 0x40
			if b[0] > 0x7f {
				trail--
			}
			text.WriteRune(rune(codePage936().pairs[int(first-0x81)*190+trail]))
			b = b[1:]
		}
	}
	return text.String(), nil
}

func encode936(name string) ([]byte, error) {
	b := make([]byte, 0, 19)
	for _, r := range name {
		switch {
		case r == 0 || r > 0xffff:
			return nil, invalidRecipient
		case r < 0x80:
			b = append(b, byte(r))
		default:
			v := codePage936().encoded[r]
			if v == 0 {
				return nil, invalidRecipient
			}
			if v > 0xff {
				b = append(b, byte(v>>8))
			}
			b = append(b, byte(v))
		}
		if len(b) > 19 {
			return nil, invalidRecipient
		}
	}
	return b, nil
}

type RecipientRequest struct {
	Name string
	Mode byte
}

func (r RecipientRequest) Ordinary() bool { return r.Mode == 0 && strings.TrimSpace(r.Name) != "" }

// ReadRecipient consumes the unpadded native fields before the old Host
// checks the account lease and then applies its ordinary-mode rule.
func ReadRecipient(body []byte) (RecipientRequest, error) {
	if len(body) < 5 || len(body) > 24 || int(le.Uint32(body)) != len(body)-5 {
		return RecipientRequest{}, invalidRecipient
	}
	name, err := decode936(body[4 : len(body)-1])
	return RecipientRequest{Name: name, Mode: body[len(body)-1]}, err
}

func RecipientAccepted(name string, level uint16, job, grow byte) ([]byte, error) {
	raw, err := encode936(name)
	if err != nil || len(raw) == 0 {
		return nil, invalidRecipient
	}
	b := le.AppendUint32([]byte{1}, uint32(len(raw)))
	b = append(b, raw...)
	b = append(b, 6) // LocalPlayerMailModule.Routes() uses the retail group 6.
	b = le.AppendUint16(b, level)
	return append(b, job, grow, 0, 0, 0), nil
}
