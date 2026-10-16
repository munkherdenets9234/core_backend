package mailer

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxAmountWords is the largest amount AmountWords spells out.
const MaxAmountWords = 999_999_999_999

// Mongolian number words in the attributive form, the one used before a
// following word. Every number here is followed by another word (a magnitude
// such as "мянга" or the currency "төгрөг"), so the form never changes with
// position: tens take -н (арван, хорин ...) and the units гурав, дөрөв, тав,
// зургаа, долоо, найм and ес become гурван, дөрвөн, таван, зургаан, долоон,
// найман and есөн. Нэг and хоёр are unchanged.
var (
	mnUnits = [10]string{"", "нэг", "хоёр", "гурван", "дөрвөн", "таван", "зургаан", "долоон", "найман", "есөн"}
	mnTens  = [10]string{"", "арван", "хорин", "гучин", "дөчин", "тавин", "жаран", "далан", "наян", "ерэн"}
)

// mnGroup spells 1..999 as hundreds, tens and units (attributive form).
func mnGroup(g int) []string {
	var w []string
	if h := g / 100; h > 0 {
		w = append(w, mnUnits[h], "зуун")
	}
	if t := g % 100 / 10; t > 0 {
		w = append(w, mnTens[t])
	}
	if u := g % 10; u > 0 {
		w = append(w, mnUnits[u])
	}
	return w
}

// AmountWords spells a whole MNT amount in Mongolian, followed by the word
// "төгрөг", with the first letter capitalised: 80000 gives "Наян мянган
// төгрөг". Only 0 to MaxAmountWords is spelled; any other value returns false.
//
// "мянга" is "мянга" inside a longer number and "мянган" when it comes
// directly before "төгрөг". 1000 is "Нэг мянган төгрөг".
func AmountWords(n int64) (string, bool) {
	if n < 0 || n > MaxAmountWords {
		return "", false
	}
	if n == 0 {
		return "Тэг төгрөг", true
	}
	groups := []struct {
		div  int64
		word string
	}{{1_000_000_000, "тэрбум"}, {1_000_000, "сая"}, {1_000, "мянга"}, {1, ""}}
	var words []string
	for _, g := range groups {
		v := int(n / g.div % 1000)
		if v == 0 {
			continue
		}
		words = append(words, mnGroup(v)...)
		if g.word == "" {
			continue
		}
		w := g.word
		if g.div == 1_000 && n%1000 == 0 {
			w = "мянган"
		}
		words = append(words, w)
	}
	words = append(words, "төгрөг")
	s := strings.Join(words, " ")
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:], true
}
