package answersheet

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	QuestionTypeRadio    = "Radio"
	QuestionTypeCheckbox = "Checkbox"
	QuestionTypeText     = "Text"
	QuestionTypeTextarea = "Textarea"
	QuestionTypeNumber   = "Number"
	QuestionTypeSection  = "Section"
)

type Questionnaire struct {
	Code      string
	Title     string
	Version   string
	Type      string
	Questions []Question
}

type Question struct {
	Code            string
	Type            string
	Title           string
	Options         []Option
	ValidationRules []ValidationRule
}

type Option struct {
	Code    string
	Content string
	Score   int32
}

type ValidationRule struct {
	RuleType    string
	TargetValue string
}

type Answer struct {
	QuestionCode string
	QuestionType string
	Score        uint32
	Value        interface{}
}

func BuildAnswers(q Questionnaire, rng *rand.Rand) []Answer {
	answers := make([]Answer, 0, len(q.Questions))
	for _, question := range q.Questions {
		answer, ok := BuildAnswerForQuestion(question, rng)
		if !ok {
			continue
		}
		answers = append(answers, answer)
	}
	return answers
}

func BuildAnswerForQuestion(question Question, rng *rand.Rand) (Answer, bool) {
	resolvedType := ResolveQuestionType(question)
	normalizedType := NormalizeQuestionType(resolvedType)

	switch normalizedType {
	case strings.ToLower(QuestionTypeRadio):
		if len(question.Options) == 0 {
			return Answer{}, false
		}
		opt := question.Options[rng.Intn(len(question.Options))]
		value := opt.Code
		if value == "" {
			value = opt.Content
		}
		if value == "" {
			return Answer{}, false
		}
		return Answer{
			QuestionCode: question.Code,
			QuestionType: QuestionTypeRadio,
			Score:        0,
			Value:        value,
		}, true

	case strings.ToLower(QuestionTypeCheckbox):
		if len(question.Options) == 0 {
			return Answer{}, false
		}
		minSelections := 1
		if ruleValue, ok := intRuleValue(question, "min_selections"); ok && ruleValue > minSelections {
			minSelections = ruleValue
		}
		if hasRequiredRule(question) && minSelections < 1 {
			minSelections = 1
		}
		maxSelections := len(question.Options)
		if ruleValue, ok := intRuleValue(question, "max_selections"); ok && ruleValue > 0 && ruleValue < maxSelections {
			maxSelections = ruleValue
		}
		if minSelections > len(question.Options) {
			minSelections = len(question.Options)
		}
		if maxSelections < minSelections {
			maxSelections = minSelections
		}

		count := minSelections
		if maxSelections > minSelections {
			count += rng.Intn(maxSelections - minSelections + 1)
		}

		selectedIndices := make(map[int]bool)
		selectedValues := make([]string, 0, count)
		for len(selectedValues) < count {
			idx := rng.Intn(len(question.Options))
			if selectedIndices[idx] {
				continue
			}
			selectedIndices[idx] = true
			opt := question.Options[idx]
			value := opt.Code
			if value == "" {
				value = opt.Content
			}
			if value != "" {
				selectedValues = append(selectedValues, value)
			}
		}
		if len(selectedValues) == 0 {
			return Answer{}, false
		}
		return Answer{
			QuestionCode: question.Code,
			QuestionType: QuestionTypeCheckbox,
			Score:        0,
			Value:        selectedValues,
		}, true

	case strings.ToLower(QuestionTypeText), strings.ToLower(QuestionTypeTextarea):
		textValue, ok := buildTextAnswer(question, rng)
		if !ok {
			return Answer{}, false
		}
		return Answer{
			QuestionCode: question.Code,
			QuestionType: resolvedType,
			Score:        0,
			Value:        textValue,
		}, true

	case strings.ToLower(QuestionTypeNumber):
		numberValue, ok := buildNumberAnswer(question, rng)
		if !ok {
			return Answer{}, false
		}
		return Answer{
			QuestionCode: question.Code,
			QuestionType: QuestionTypeNumber,
			Score:        0,
			Value:        numberValue,
		}, true

	case strings.ToLower(QuestionTypeSection):
		return Answer{}, false
	default:
		return Answer{}, false
	}
}

func Validate(q Questionnaire, answers []Answer) []map[string]interface{} {
	questionMap := make(map[string]Question, len(q.Questions))
	for _, question := range q.Questions {
		questionMap[question.Code] = question
	}

	invalidAnswers := make([]map[string]interface{}, 0)
	for _, answer := range answers {
		question, exists := questionMap[answer.QuestionCode]
		if !exists {
			invalidAnswers = append(invalidAnswers, map[string]interface{}{
				"question_code": answer.QuestionCode,
				"reason":        "question not found in questionnaire",
			})
			continue
		}
		invalidAnswers = append(invalidAnswers, validateAnswer(question, answer)...)
	}
	return invalidAnswers
}

func buildTextAnswer(question Question, rng *rand.Rand) (string, bool) {
	minLength := 2
	if ruleValue, ok := intRuleValue(question, "min_length"); ok && ruleValue > minLength {
		minLength = ruleValue
	}
	maxLength := 0
	if ruleValue, ok := intRuleValue(question, "max_length"); ok {
		maxLength = ruleValue
	}
	if maxLength > 0 && maxLength < minLength {
		minLength = maxLength
	}

	pattern, hasPattern := stringRuleValue(question, "pattern")
	candidates := []string{
		"情况稳定",
		"状态良好",
		"需要关注",
		"测试填写",
		"学习正常",
		"睡眠正常",
		"情绪平稳",
		"测试123",
		"123456",
		"13812345678",
		"test@example.com",
	}

	start := 0
	if len(candidates) > 0 {
		start = rng.Intn(len(candidates))
	}
	for i := 0; i < len(candidates); i++ {
		candidate := normalizeTextLength(candidates[(start+i)%len(candidates)], minLength, maxLength)
		if candidate == "" {
			continue
		}
		if hasPattern && !matchesPattern(candidate, pattern) {
			continue
		}
		return candidate, true
	}

	fallback := normalizeTextLength(strings.Repeat("测", maxInt(minLength, 1)), minLength, maxLength)
	if fallback == "" {
		return "", false
	}
	if hasPattern && !matchesPattern(fallback, pattern) {
		for _, candidate := range []string{"123456", "13812345678", "test@example.com"} {
			candidate = normalizeTextLength(candidate, minLength, maxLength)
			if candidate != "" && matchesPattern(candidate, pattern) {
				return candidate, true
			}
		}
		return "", false
	}
	return fallback, true
}

func buildNumberAnswer(question Question, rng *rand.Rand) (float64, bool) {
	minValue := 1.0
	if ruleValue, ok := floatRuleValue(question, "min_value"); ok {
		minValue = ruleValue
	}
	maxValue := 100.0
	if ruleValue, ok := floatRuleValue(question, "max_value"); ok {
		maxValue = ruleValue
	}
	if maxValue < minValue {
		maxValue = minValue
	}
	if maxValue == minValue {
		return minValue, true
	}

	rangeSize := int(maxValue-minValue) + 1
	if rangeSize <= 1 {
		return minValue, true
	}
	return minValue + float64(rng.Intn(rangeSize)), true
}

func validateAnswer(question Question, answer Answer) []map[string]interface{} {
	invalid := make([]map[string]interface{}, 0)
	resolvedType := NormalizeQuestionType(ResolveQuestionType(question))
	optionSet := questionOptionSet(question)

	switch value := answer.Value.(type) {
	case string:
		if resolvedType == strings.ToLower(QuestionTypeRadio) && len(optionSet) > 0 && !optionSet[value] {
			invalid = append(invalid, map[string]interface{}{
				"question_code": answer.QuestionCode,
				"value_type":    "string",
				"reason":        "option not found in question",
			})
		}
		if reason := validateScalarRules(question, value); reason != "" {
			invalid = append(invalid, map[string]interface{}{
				"question_code": answer.QuestionCode,
				"value_type":    "string",
				"reason":        reason,
			})
		}
	case []string:
		for _, item := range value {
			if len(optionSet) > 0 && !optionSet[item] {
				invalid = append(invalid, map[string]interface{}{
					"question_code": answer.QuestionCode,
					"value_type":    "string_slice",
					"reason":        "option not found in question",
				})
			}
		}
		if reason := validateSelectionRules(question, len(value)); reason != "" {
			invalid = append(invalid, map[string]interface{}{
				"question_code": answer.QuestionCode,
				"value_type":    "string_slice",
				"reason":        reason,
			})
		}
	case float64:
		if reason := validateNumberRules(question, value); reason != "" {
			invalid = append(invalid, map[string]interface{}{
				"question_code": answer.QuestionCode,
				"value_type":    "number",
				"reason":        reason,
			})
		}
	default:
		invalid = append(invalid, map[string]interface{}{
			"question_code": answer.QuestionCode,
			"value_type":    fmt.Sprintf("%T", value),
			"reason":        fmt.Sprintf("unsupported answer value type %T", value),
		})
	}

	return invalid
}

func validateScalarRules(question Question, value string) string {
	if hasRequiredRule(question) && strings.TrimSpace(value) == "" {
		return "required rule violated"
	}
	runeCount := utf8.RuneCountInString(value)
	if minLength, ok := intRuleValue(question, "min_length"); ok && runeCount < minLength {
		return fmt.Sprintf("value shorter than min_length %d", minLength)
	}
	if maxLength, ok := intRuleValue(question, "max_length"); ok && runeCount > maxLength {
		return fmt.Sprintf("value longer than max_length %d", maxLength)
	}
	if pattern, ok := stringRuleValue(question, "pattern"); ok && !matchesPattern(value, pattern) {
		return "pattern rule violated"
	}
	return ""
}

func validateSelectionRules(question Question, count int) string {
	if hasRequiredRule(question) && count == 0 {
		return "required rule violated"
	}
	if minSelections, ok := intRuleValue(question, "min_selections"); ok && count < minSelections {
		return fmt.Sprintf("selection count below min_selections %d", minSelections)
	}
	if maxSelections, ok := intRuleValue(question, "max_selections"); ok && count > maxSelections {
		return fmt.Sprintf("selection count above max_selections %d", maxSelections)
	}
	return ""
}

func validateNumberRules(question Question, value float64) string {
	if minValue, ok := floatRuleValue(question, "min_value"); ok && value < minValue {
		return fmt.Sprintf("value below min_value %v", minValue)
	}
	if maxValue, ok := floatRuleValue(question, "max_value"); ok && value > maxValue {
		return fmt.Sprintf("value above max_value %v", maxValue)
	}
	return ""
}

func hasRequiredRule(question Question) bool {
	target, ok := stringRuleValue(question, "required")
	if !ok {
		return false
	}
	target = strings.TrimSpace(strings.ToLower(target))
	return target == "" || target == "true" || target == "1" || target == "yes"
}

func intRuleValue(question Question, ruleType string) (int, bool) {
	target, ok := stringRuleValue(question, ruleType)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(target))
	if err != nil {
		return 0, false
	}
	return value, true
}

func floatRuleValue(question Question, ruleType string) (float64, bool) {
	target, ok := stringRuleValue(question, ruleType)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(target), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func stringRuleValue(question Question, ruleType string) (string, bool) {
	for _, rule := range question.ValidationRules {
		if strings.EqualFold(strings.TrimSpace(rule.RuleType), strings.TrimSpace(ruleType)) {
			return rule.TargetValue, true
		}
	}
	return "", false
}

func normalizeTextLength(value string, minLength, maxLength int) string {
	if strings.TrimSpace(value) == "" {
		value = "测试"
	}
	for utf8.RuneCountInString(value) < minLength {
		value += "测"
	}
	if maxLength > 0 {
		runes := []rune(value)
		if len(runes) > maxLength {
			value = string(runes[:maxLength])
		}
	}
	return value
}

func matchesPattern(value, pattern string) bool {
	if strings.TrimSpace(pattern) == "" {
		return true
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return regex.MatchString(value)
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func questionOptionSet(question Question) map[string]bool {
	optionSet := make(map[string]bool, len(question.Options)*2)
	for _, option := range question.Options {
		if option.Code != "" {
			optionSet[option.Code] = true
		}
		if option.Content != "" {
			optionSet[option.Content] = true
		}
	}
	return optionSet
}

func NormalizeQuestionType(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func ResolveQuestionType(question Question) string {
	switch NormalizeQuestionType(question.Type) {
	case strings.ToLower(QuestionTypeRadio):
		return QuestionTypeRadio
	case strings.ToLower(QuestionTypeCheckbox):
		return QuestionTypeCheckbox
	case strings.ToLower(QuestionTypeText):
		return QuestionTypeText
	case strings.ToLower(QuestionTypeTextarea):
		return QuestionTypeTextarea
	case strings.ToLower(QuestionTypeNumber):
		return QuestionTypeNumber
	case strings.ToLower(QuestionTypeSection):
		return QuestionTypeSection
	}
	if len(question.Options) > 0 {
		return QuestionTypeRadio
	}
	return QuestionTypeSection
}

func CollectQuestionTypes(q Questionnaire) []string {
	if len(q.Questions) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(q.Questions))
	out := make([]string, 0, len(q.Questions))
	for _, question := range q.Questions {
		typ := strings.TrimSpace(question.Type)
		if typ == "" {
			typ = fmt.Sprintf("<empty:%s>", ResolveQuestionType(question))
		}
		if _, exists := seen[typ]; exists {
			continue
		}
		seen[typ] = struct{}{}
		out = append(out, typ)
	}
	return out
}

func Truncate(value string, max int) string {
	if max <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

func PreviewQuestionnaire(q Questionnaire, max int) []map[string]string {
	if len(q.Questions) == 0 || max <= 0 {
		return nil
	}
	if len(q.Questions) < max {
		max = len(q.Questions)
	}
	out := make([]map[string]string, 0, max)
	for i := 0; i < max; i++ {
		question := q.Questions[i]
		out = append(out, map[string]string{
			"code":          question.Code,
			"type":          question.Type,
			"resolved_type": ResolveQuestionType(question),
			"option_count":  strconv.Itoa(len(question.Options)),
			"title_preview": Truncate(question.Title, 30),
		})
	}
	return out
}
