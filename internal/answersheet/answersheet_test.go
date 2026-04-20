package answersheet

import (
	"math/rand"
	"testing"
	"unicode/utf8"
)

func TestBuildAnswerForQuestionHonorsCheckboxSelectionRules(t *testing.T) {
	t.Parallel()

	answer, ok := BuildAnswerForQuestion(Question{
		Code: "q_checkbox",
		Type: QuestionTypeCheckbox,
		Options: []Option{
			{Code: "A"},
			{Code: "B"},
			{Code: "C"},
			{Code: "D"},
		},
		ValidationRules: []ValidationRule{
			{RuleType: "min_selections", TargetValue: "2"},
			{RuleType: "max_selections", TargetValue: "2"},
		},
	}, rand.New(rand.NewSource(1)))
	if !ok {
		t.Fatalf("expected checkbox answer to be generated")
	}

	values, ok := answer.Value.([]string)
	if !ok {
		t.Fatalf("expected []string checkbox value, got %T", answer.Value)
	}
	if len(values) != 2 {
		t.Fatalf("expected 2 selected values, got %d (%v)", len(values), values)
	}
}

func TestBuildAnswerForQuestionHonorsTextLengthRules(t *testing.T) {
	t.Parallel()

	answer, ok := BuildAnswerForQuestion(Question{
		Code: "q_text",
		Type: QuestionTypeText,
		ValidationRules: []ValidationRule{
			{RuleType: "min_length", TargetValue: "6"},
			{RuleType: "max_length", TargetValue: "6"},
		},
	}, rand.New(rand.NewSource(2)))
	if !ok {
		t.Fatalf("expected text answer to be generated")
	}

	value, ok := answer.Value.(string)
	if !ok {
		t.Fatalf("expected string text value, got %T", answer.Value)
	}
	if got := utf8.RuneCountInString(value); got != 6 {
		t.Fatalf("expected text length 6, got %d (%q)", got, value)
	}
}

func TestBuildAnswerForQuestionHonorsNumberBounds(t *testing.T) {
	t.Parallel()

	answer, ok := BuildAnswerForQuestion(Question{
		Code: "q_number",
		Type: QuestionTypeNumber,
		ValidationRules: []ValidationRule{
			{RuleType: "min_value", TargetValue: "8"},
			{RuleType: "max_value", TargetValue: "18"},
		},
	}, rand.New(rand.NewSource(3)))
	if !ok {
		t.Fatalf("expected numeric answer to be generated")
	}

	value, ok := answer.Value.(float64)
	if !ok {
		t.Fatalf("expected float64 numeric value, got %T", answer.Value)
	}
	if value < 8 || value > 18 {
		t.Fatalf("expected numeric value in [8,18], got %v", value)
	}
}
