package commerce

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"s9a1/server/internal/catalogprep/gm"
	"s9a1/server/internal/storage"
)

const scriptLimit = 1 << 20

var mailLifetime = regexp.MustCompile("(?i)^(expiration date|usable period|period|expire time|lifetime|equipment duration|daily delete item|weekly delete item|monthly delete item)$")

func (i *Items) MailAttachment(id uint32, template *ItemTemplate) (*storage.MailAttachmentPolicy, error) {
	if template == nil || id == 0 {
		return nil, nil
	}
	r, err := i.Source(id)
	if err != nil {
		return nil, err
	}
	return mailAttachment(r, template)
}

func mailAttachment(r ItemSource, t *ItemTemplate) (*storage.MailAttachmentPolicy, error) {
	if r.Hash != t.ResourceHash {
		return nil, fmt.Errorf("mail attachment and ordinary template resource identities differ for %d", r.Id)
	}
	rows, err := gm.ItemFields(r.Text)
	if err != nil {
		return nil, err
	}
	attach, err := commerceSection(r.Text, "attach type")
	if err != nil {
		return nil, err
	}
	out := storage.MailAttachmentPolicy{TemplateID: r.Id, ResourceHash: r.Hash}
	switch attach {
	case "`[free]`":
		out.Rule = 1
	case "`[account]`", "`[account bound]`", "`[account-bind]`":
		out.Rule = 2
	case "`[sealing]`", "`[seal]`":
		out.Rule = 3
	case "`[trade limit]`":
		out.Rule = 4
	}
	for _, row := range rows {
		if row.Name == "trade limit" || row.Name == "trade limit max" {
			out.Rule = 4
		}
		if mailLifetime.MatchString(row.Name) {
			out.LifetimeRestricted = true
		}
	}
	return &out, nil
}

func (i *Items) WeightsAndMail(ids []uint32, pinned map[uint32]*ItemTemplate) (map[uint32]int64, map[uint32]storage.MailAttachmentPolicy, error) {
	weights := make(map[uint32]int64, len(ids))
	mail := make(map[uint32]storage.MailAttachmentPolicy, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, done := weights[id]; done {
			continue
		}
		r, err := i.Source(id)
		if err != nil {
			return nil, nil, err
		}
		weights[id], err = r.Weight()
		if err != nil {
			return nil, nil, err
		}
		t := pinned[id]
		if t == nil {
			t, _ = r.Ordinary()
			if !r.Equipment && t == nil && strings.Contains(r.Text, "[teleport potion]") {
				if kind, _ := gm.ItemField(r.Text, "stackable type"); kind == "`[teleport potion]` 0" {
					t, _, _ = r.GM()
				}
			}
			if !r.Equipment && t == nil && usesGiftBeforeCommerce(id, r.Text) {
				t, _ = r.Gift()
			}
		}
		if t == nil {
			continue
		}
		p, err := mailAttachment(r, t)
		if err != nil {
			return nil, nil, err
		}
		mail[id] = *p
	}
	return weights, mail, nil
}

func usesGiftBeforeCommerce(id uint32, text string) bool {
	switch id {
	case 10312870, 490015880, 10313330, 490015881, 10313331, 490015882, 10313949, 490015883, 1034, 1035, 1036, 1037, 1231, 1232, 490014289:
		return true
	}
	return false
}

func GoldCarryLimits(a gm.ItemArchive) (map[byte]int64, error) {
	text, err := a.ReadText("etc/goldlimitbylevel.etc", scriptLimit)
	if err != nil {
		return nil, err
	}
	fields, err := gm.ItemFields(text)
	if err != nil || len(fields) != 2 || fields[0].Name != "gold limit from level" || fields[1].Name != "/gold limit from level" || fields[1].Value != "" {
		return nil, fmt.Errorf("missing complete authoritative gold limit table")
	}
	values := strings.Fields(fields[0].Value)
	if len(values) != 202 {
		return nil, fmt.Errorf("gold limit table requires exactly level 0..100 pairs")
	}
	out := make(map[byte]int64, 101)
	for i := range 101 {
		level, e := strconv.ParseInt(values[2*i], 10, 32)
		n, ne := unsigned(values[2*i+1], 31)
		if e != nil || ne != nil || level != int64(i) || i > 0 && n == 0 {
			return nil, fmt.Errorf("authoritative gold limit level/value is invalid")
		}
		out[byte(i)] = int64(n)
	}
	return out, nil
}

type commerceTextSource interface {
	ReadCommerceText(path string) (string, error)
}

func readCommerceText(a gm.ItemArchive, path string) (string, error) {
	if ext, ok := a.(commerceTextSource); ok {
		return ext.ReadCommerceText(path)
	}
	return a.ReadText(path, scriptLimit)
}

var numericField = regexp.MustCompile("-?[0-9]+(?:\\.[0-9]+)?")

func numbers(text string) []string { return numericField.FindAllString(text, -1) }

func commerceSection(text, name string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	index := -1
	for i, line := range lines {
		if strings.EqualFold(strings.TrimRight(line, " \t"), "["+name+"]") {
			if index >= 0 {
				return "", fmt.Errorf("Ambiguous commerce field: %s", name)
			}
			index = i
		}
	}
	if index < 0 {
		return "", nil
	}
	end := index + 1
	for end < len(lines) && !strings.HasPrefix(strings.TrimLeft(lines[end], " \t"), "[") {
		end++
	}
	return strings.TrimSpace(strings.Join(lines[index+1:end], "\n")), nil
}

func commerceBlock(text, name string) (string, error) {
	text = strings.ReplaceAll(text, "\r", "")
	re := regexp.MustCompile("(?ims)^\\[" + regexp.QuoteMeta(name) + "\\]\\s*(.*?)^\\[/" + regexp.QuoteMeta(name) + "\\]")
	blocks := re.FindAllStringSubmatch(text, 2)
	if len(blocks) > 1 {
		return "", fmt.Errorf("Ambiguous commerce block: %s", name)
	}
	if len(blocks) == 1 {
		return strings.TrimSpace(blocks[0][1]), nil
	}
	return commerceSection(text, name)
}

func unsigned(s string, bits int) (uint64, error) {
	if s == "" || strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseUint(s, 10, bits)
}

func containsNumber(text string, number string) bool { return slices.Contains(numbers(text), number) }
