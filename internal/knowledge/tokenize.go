package knowledge

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Conversation scaffolding is not a topic anchor. In particular, English
// request words must not make a sample question outrank the document it names.
// Keep actual product/protocol words for explicitTopic's no-fallback behavior.
var stopWords = makeWordSet(`a about an and are as at be by can could do does for from how i in is it me my of on or please that the this to what when where which who why will with would you your
tell tells telling told show shows showing shown explain explains explaining explained describe describes describing described give gives giving provide provides providing summarize summarise summary overview information detail details documentation document documents brief briefly more like know want need
것 거 게 좀 수 등 및 의 이 그 저 제 내 우리 사내 해당 관련 대한 대해 내용 문서 자료 정보 방법 방식 어떻게 무엇 뭐 어떤 알려 알려줘 알려주세요 알려주십시오 알려줄 설명 설명해 설명해줘 설명해주세요 해 해줘 해주세요 해줄 있나 있나요 있어 있으면 있는 없다 없나요 궁금 궁금해 싶어 싶습니다 필요 필요한 하는 하려면 하려고 하고 하나요 하나 알려주실 확인 확인해 확인해주세요 알려주세요 부탁 부탁해 부탁드립니다 합니다 입니다 인가요 요`)

// These words can help rank an identified topic but cannot establish relevance
// alone: an unknown-topic question must not match every "설정" heading.
var weakWords = makeWordSet(`설정 구성 접속 연결 사용 이용 작동 실행 설치 변경 추가 삭제 조회 수정 생성 관리 운영 적용 진행 절차 가이드 안내 해결 오류 문제 포트 주소 번호 서비스 종류 신청 보내 보내는 설치방법 사용방법 구성방법 접속방법 연결방법 신청방법 설정방법 configure configuration setup install installation use usage connect connection guide help error issue port address service type request`)

// Small explicit vocabulary aliases improve infrastructure questions without
// embedding dependencies. Each alias set is scored as one query concept.
var aliases = map[string][]string{
	"메일":    {"메일", "이메일", "email", "mail", "smtp"},
	"이메일":   {"메일", "이메일", "email", "mail", "smtp"},
	"email": {"email", "이메일", "메일", "smtp"},
	// An explicit protocol name is more specific than mail in general.
	"smtp":       {"smtp"},
	"스토리지":       {"스토리지", "storage"},
	"storage":    {"storage", "스토리지"},
	"쿠버네티스":      {"쿠버네티스", "kubernetes", "k8s"},
	"kubernetes": {"kubernetes", "쿠버네티스", "k8s"},
	"k8s":        {"k8s", "kubernetes", "쿠버네티스"},
}

func makeWordSet(value string) map[string]bool {
	m := make(map[string]bool)
	for _, word := range strings.Fields(value) {
		m[word] = true
	}
	return m
}

func tokens(text string) []string {
	var result []string
	var current []rune
	kind := 0
	flush := func() {
		if len(current) != 0 {
			result = append(result, string(current))
			current = nil
		}
	}
	for _, r := range strings.ToLower(text) {
		next := 0
		if unicode.In(r, unicode.Hangul) {
			next = 1
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			next = 2
		}
		if next == 0 {
			flush()
			kind = 0
			continue
		}
		if kind != 0 && next != kind {
			flush()
		}
		current = append(current, r)
		kind = next
	}
	flush()
	return result
}

func koreanStem(word string) string {
	if !hasHangul(word) {
		return word
	}
	// Long suffixes precede short particles. Keep at least two syllables to
	// avoid turning useful nouns (for example 서비스) into one-character terms.
	for _, suffix := range []string{"으로부터", "에서는", "에게서", "이라고", "으로는", "에서", "으로", "에게", "까지", "부터", "처럼", "이란", "이라", "이랑", "이나", "들은", "들을", "하는", "하려면", "하려고", "하고", "하나요", "은", "는", "을", "를", "의", "이", "가", "에", "와", "과", "도", "만"} {
		if strings.HasSuffix(word, suffix) {
			base := strings.TrimSuffix(word, suffix)
			if utf8.RuneCountInString(base) >= 2 {
				return base
			}
		}
	}
	return word
}

func hasHangul(word string) bool {
	for _, r := range word {
		if unicode.In(r, unicode.Hangul) {
			return true
		}
	}
	return false
}

func queryWords(query string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, word := range tokens(query) {
		if stopWords[word] {
			continue
		}
		word = koreanStem(word)
		if stopWords[word] || seen[word] || utf8.RuneCountInString(word) < 2 {
			continue
		}
		seen[word] = true
		out = append(out, word)
		if len(out) == 32 {
			break
		}
	}
	return out
}

type queryTerm struct {
	word   string
	weight float64
}

// Named Latin products/protocols and configured bilingual concepts are explicit
// topics. Other nouns can refine ranking but must not replace a named topic.
func explicitTopic(word string) bool {
	if weakWords[word] || stopWords[word] {
		return false
	}
	if _, ok := aliases[word]; ok {
		return true
	}
	latin := 0
	for _, r := range word {
		if r >= 'a' && r <= 'z' {
			latin++
		}
	}
	return latin >= 2
}

func expandWord(word string, s *snapshot) []queryTerm {
	words := aliases[word]
	if len(words) == 0 {
		words = []string{word}
	}
	set := make(map[string]float64)
	add := func(term string, weight float64) {
		if weight > set[term] {
			set[term] = weight
		}
	}
	// Always retain the literal query even if a synonym list is configured.
	if _, ok := s.terms[word]; ok {
		add(word, 1)
	}
	for _, w := range words {
		weight := .55
		if w == word {
			weight = 1
		}
		if _, ok := s.terms[w]; ok {
			add(w, weight)
		}
		if hasHangul(w) && utf8.RuneCountInString(w) >= 2 {
			for _, term := range s.vocab {
				if strings.Contains(term, w) {
					// A small substring of a long compound (메일 in
					// 메일그룹사용정책) is weaker evidence than a near match.
					ratio := float64(utf8.RuneCountInString(w)) / float64(utf8.RuneCountInString(term))
					partialWeight := weight * .8 * math.Sqrt(ratio)
					if term == w {
						partialWeight = weight
					}
					add(term, partialWeight)
				}
				if len(set) >= 128 {
					break
				}
			}
		}
	}
	out := make([]queryTerm, 0, len(set))
	for term, weight := range set {
		out = append(out, queryTerm{term, weight})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].word < out[j].word })
	return out
}
