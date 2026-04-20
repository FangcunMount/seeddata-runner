package seedprofile

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
	"time"

	"github.com/mozillazg/go-pinyin"
)

type Generator struct {
	phonePrefix string
	emailDomain string
}

type Profile struct {
	Index         int
	RunDate       time.Time
	GuardianName  string
	GuardianPhone string
	GuardianEmail string
	ChildName     string
	ChildDOB      string
	ChildGender   uint8
}

func New(phonePrefix, emailDomain string) *Generator {
	return &Generator{
		phonePrefix: strings.TrimSpace(phonePrefix),
		emailDomain: strings.TrimPrefix(strings.ToLower(strings.TrimSpace(emailDomain)), "@"),
	}
}

func (g *Generator) Generate(runDate time.Time, idx int) Profile {
	rng := rand.New(rand.NewSource(int64(newSeed(fmt.Sprintf("profile:%s:%d", runDate.Format("20060102"), idx)))))

	guardianGender := uint8(1)
	if idx%2 == 1 {
		guardianGender = 2
	}
	childGender := uint8(1)
	if rng.Intn(2) == 1 {
		childGender = 2
	}

	guardianName := generateChineseName(rng, guardianGender, true)
	childName := generateChineseName(rng, childGender, false)

	phoneSuffix := fmt.Sprintf("%02d%02d%04d", int(runDate.Month()), runDate.Day(), idx+1)
	phone := strings.TrimSpace(g.phonePrefix) + phoneSuffix

	emailLocal := normalizedEmailLocal(guardianName)
	if emailLocal == "" {
		emailLocal = fmt.Sprintf("dailyguardian%04d", idx+1)
	}
	emailDomain := g.emailDomain
	if emailDomain == "" {
		emailDomain = "fangcunmount.com"
	}
	email := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%s_%s_%04d@%s", emailLocal, runDate.Format("20060102"), idx+1, emailDomain)))

	childDOB := generateChildDOB(rng, runDate)

	return Profile{
		Index:         idx + 1,
		RunDate:       runDate,
		GuardianName:  guardianName,
		GuardianPhone: phone,
		GuardianEmail: email,
		ChildName:     childName,
		ChildDOB:      childDOB,
		ChildGender:   childGender,
	}
}

var (
	chineseSurnames = []string{
		"王", "李", "张", "刘", "陈", "杨", "黄", "赵", "吴", "周",
		"徐", "孙", "马", "朱", "胡", "郭", "何", "高", "林", "罗",
		"郑", "梁", "谢", "宋", "唐", "许", "韩", "冯", "邓", "曹",
	}
	adultMaleGivenNames = []string{
		"伟", "磊", "洋", "勇", "军", "杰", "涛", "超", "斌", "强",
		"鹏", "辉", "峰", "健", "俊", "浩", "博", "诚", "凯", "辰",
	}
	adultFemaleGivenNames = []string{
		"敏", "静", "丽", "艳", "娟", "颖", "丹", "洁", "婷", "雪",
		"琳", "倩", "萍", "娜", "佳", "欣", "瑶", "悦", "宁", "雯",
	}
	childMaleGivenNames = []string{
		"子轩", "浩然", "宇辰", "梓豪", "博文", "俊宇", "嘉乐", "昊天", "奕辰", "晨阳",
		"梓轩", "铭泽", "思远", "景辰", "一鸣", "承泽", "皓宇", "嘉树", "子墨", "逸凡",
	}
	childFemaleGivenNames = []string{
		"欣怡", "若涵", "诗涵", "雨桐", "梦瑶", "语彤", "梓萱", "依诺", "可欣", "雨薇",
		"芷晴", "欣妍", "沐瑶", "佳宁", "心妍", "思妍", "可馨", "子晴", "书瑶", "雅婷",
	}
)

func generateChineseName(rng *rand.Rand, gender uint8, adult bool) string {
	surname := pickChineseNamePart(rng, chineseSurnames)
	var givenName string
	switch {
	case adult && gender == 2:
		givenName = pickChineseNamePart(rng, adultFemaleGivenNames)
	case adult:
		givenName = pickChineseNamePart(rng, adultMaleGivenNames)
	case gender == 2:
		givenName = pickChineseNamePart(rng, childFemaleGivenNames)
	default:
		givenName = pickChineseNamePart(rng, childMaleGivenNames)
	}
	name := strings.TrimSpace(surname + givenName)
	if name == "" {
		if adult {
			return "模拟家长"
		}
		return "模拟儿童"
	}
	return name
}

func pickChineseNamePart(rng *rand.Rand, values []string) string {
	if len(values) == 0 {
		return ""
	}
	if rng == nil {
		return values[0]
	}
	return values[rng.Intn(len(values))]
}

func generateChildDOB(rng *rand.Rand, runDate time.Time) string {
	oldestDOB := runDate.AddDate(-18, 0, 0)
	youngestDOB := runDate.AddDate(-8, 0, 0)
	if youngestDOB.Before(oldestDOB) {
		oldestDOB, youngestDOB = youngestDOB, oldestDOB
	}
	daySpan := int(youngestDOB.Sub(oldestDOB).Hours() / 24)
	if daySpan < 0 {
		daySpan = 0
	}
	offsetDays := 0
	if rng != nil && daySpan > 0 {
		offsetDays = rng.Intn(daySpan + 1)
	}
	return oldestDOB.AddDate(0, 0, offsetDays).Format("2006-01-02")
}

func normalizedEmailLocal(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	args := pinyin.NewArgs()
	args.Style = pinyin.Normal
	parts := pinyin.LazyPinyin(name, args)
	if len(parts) == 0 {
		parts = []string{name}
	}

	var builder strings.Builder
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}

func newSeed(seed string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(seed))
	return hash.Sum64()
}
